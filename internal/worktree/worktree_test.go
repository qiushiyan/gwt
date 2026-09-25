package worktree

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	t         *testing.T
	home, dir string
	r         *Repo
	log       bytes.Buffer
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, home: t.TempDir()}
	// Neither configuration, hooks, remotes nor HOME-backed paths reach live state.
	t.Setenv("HOME", f.home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.home, "config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GWT_CONFIG", "")
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR"} {
		t.Setenv(key, "")
	}
	// Git treats an empty GIT_DIR as set, so remove these variables completely.
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR"} {
		os.Unsetenv(key)
	}
	f.dir = filepath.Join(f.home, "repo with spaces")
	f.mustGit(f.home, "init", "-q", "-b", "main", f.dir)
	f.mustGit(f.dir, "config", "user.name", "Gwt Test")
	f.mustGit(f.dir, "config", "user.email", "test@example.invalid")
	f.mustGit(f.dir, "commit", "-qm", "initial", "--allow-empty")
	var err error
	f.r, err = Open(context.Background(), f.dir, f.home, &f.log)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) mustGit(dir string, args ...string) string {
	f.t.Helper()
	s, err := git(context.Background(), dir, args...)
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *fixture) configure(contents string) {
	f.t.Helper()
	write(f.t, filepath.Join(f.r.common, "gwt.toml"), contents, 0644)
	r, err := Open(context.Background(), f.r.dir, f.home, &f.log)
	if err != nil {
		f.t.Fatal(err)
	}
	f.r = r
}

func (f *fixture) create(o Options) Result {
	f.t.Helper()
	o.NonInteractive = true
	x, err := f.r.Create(context.Background(), o)
	if err != nil {
		f.t.Fatal(err)
	}
	return x
}

func write(t *testing.T, p, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestCreateFromCurrentBranchWithoutTracking(t *testing.T) {
	f := setup(t)
	f.mustGit(f.dir, "checkout", "-qb", "topic")
	f.mustGit(f.dir, "commit", "-qm", "topic", "--allow-empty")
	first := f.create(Options{Branch: "feat/nested"})
	if first.Action != "created" || first.Base != "topic" || f.mustGit(first.Path, "rev-parse", "HEAD") != f.mustGit(f.dir, "rev-parse", "topic") {
		t.Fatalf("wrong result: %+v", first)
	}
	if _, err := git(context.Background(), first.Path, "rev-parse", "@{upstream}"); err == nil {
		t.Fatal("new branch inherited tracking")
	}
}

func TestExplicitBaseAndMainIdentity(t *testing.T) {
	f := setup(t)
	a := f.create(Options{Branch: "feat/first"})
	f.mustGit(a.Path, "commit", "-qm", "work", "--allow-empty")
	r, err := Open(context.Background(), a.Path, f.home, &f.log)
	if err != nil {
		t.Fatal(err)
	}
	f.r = r
	b := f.create(Options{Branch: "fix/second", Base: "main"})
	if b.Path != filepath.Join(f.home, "dev", ".worktrees", filepath.Base(f.dir), "fix/second") {
		t.Fatal(b.Path)
	}
	if f.mustGit(b.Path, "rev-parse", "HEAD") != f.mustGit(f.dir, "rev-parse", "main") {
		t.Fatal("explicit base ignored")
	}
	if _, err := git(context.Background(), b.Path, "rev-parse", "--abbrev-ref", "@{upstream}"); err == nil {
		t.Fatal("new branch inherited tracking")
	}
}

func TestConfiguredBaseRootAndLinkedOverrides(t *testing.T) {
	f := setup(t)
	f.mustGit(f.dir, "branch", "chosen")
	f.mustGit(f.dir, "commit", "--allow-empty", "-qm", "advance main")
	f.configure(`base = "chosen"
worktree_root = "~/custom trees"
copy_globs = []
`)
	// A configured concrete base needs no confirmation, even interactively.
	x, err := f.r.Create(context.Background(), Options{Branch: "configured", Confirm: func(_, _ string) bool { t.Fatal("prompted for configured base"); return false }})
	if err != nil || x.Base != "chosen" || x.Path != filepath.Join(f.home, "custom trees", filepath.Base(f.dir), "configured") {
		t.Fatalf("%+v %v", x, err)
	}
	if f.mustGit(x.Path, "rev-parse", "HEAD") != f.mustGit(f.dir, "rev-parse", "chosen") {
		t.Fatal("wrong base")
	}
	r, err := Open(context.Background(), x.Path, f.home, &f.log)
	if err != nil {
		t.Fatal(err)
	}
	if r.config.Base != "chosen" || r.root != f.r.root || r.config.Files.Repository != f.r.config.Files.Repository {
		t.Fatalf("linked config differs: %+v", r.config)
	}
	y, err := r.Create(context.Background(), Options{Branch: "explicit", Base: "main"})
	if err != nil || y.Base != "main" {
		t.Fatalf("%+v %v", y, err)
	}
	if f.mustGit(y.Path, "rev-parse", "HEAD") != f.mustGit(f.dir, "rev-parse", "main") {
		t.Fatal("explicit base did not win")
	}
	f.configure(`base = "missing-ref"`)
	if _, err := f.r.Create(context.Background(), Options{Branch: "must-not-fallback", NonInteractive: true}); err == nil {
		t.Fatal("missing configured ref fell back")
	}
}

func TestConfirmation(t *testing.T) {
	f := setup(t)
	calls := 0
	confirm := func(branch, base string) bool { calls++; return false }
	if _, err := f.r.Create(context.Background(), Options{Branch: "declined", Confirm: confirm}); err == nil {
		t.Fatal("created after decline")
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	if _, err := os.Stat(f.r.root); !os.IsNotExist(err) {
		t.Fatal("declined creation made directories")
	}
	f.mustGit(f.dir, "branch", "existing")
	if _, err := f.r.Create(context.Background(), Options{Branch: "existing", Confirm: confirm}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("existing branch prompted")
	}
	if _, err := f.r.Create(context.Background(), Options{Branch: "explicit", Base: "main", Confirm: confirm}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("explicit base prompted")
	}
	if _, err := f.r.Create(context.Background(), Options{Branch: "accepted", Confirm: func(_, _ string) bool { return true }}); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteResolutionAndForcedNew(t *testing.T) {
	f := setup(t)
	remote := filepath.Join(f.home, "remote.git")
	f.mustGit(f.home, "init", "--bare", "-q", remote)
	f.mustGit(f.dir, "remote", "add", "origin", remote)
	f.mustGit(f.dir, "push", "-q", "origin", "main:main", "main:feat/remote", "main:feat/long/child", "main:forced")
	// Clear local tracking refs to reproduce a branch pushed after the last fetch.
	f.mustGit(f.dir, "update-ref", "-d", "refs/remotes/origin/feat/remote")
	x := f.create(Options{Branch: "feat/remote", Base: "missing-base"})
	if x.Action != "remote" || x.Upstream != "origin/feat/remote" {
		t.Fatalf("%+v", x)
	}
	if got := f.mustGit(x.Path, "rev-parse", "--abbrev-ref", "@{upstream}"); got != x.Upstream {
		t.Fatal(got)
	}
	f.mustGit(f.dir, "commit", "-qm", "advance main", "--allow-empty")
	y := f.create(Options{Branch: "forced", ForceNew: true})
	if y.Action != "created" || f.mustGit(y.Path, "rev-parse", "HEAD") != f.mustGit(f.dir, "rev-parse", "HEAD") {
		t.Fatal(y)
	}
	if _, err := git(context.Background(), y.Path, "rev-parse", "@{upstream}"); err == nil {
		t.Fatal("forced new tracks remote")
	}
	// Exact suffix matching: a longer branch must not look like its parent.
	v, err := f.r.Resolve(context.Background(), "feat/long", false)
	if err != nil || v.Kind != "absent" {
		t.Fatalf("%+v %v", v, err)
	}
	f.mustGit(f.dir, "update-ref", "refs/remotes/other/ambiguous", "main")
	f.mustGit(f.dir, "update-ref", "refs/remotes/origin/ambiguous", "main")
	if _, err := f.r.Create(context.Background(), Options{Branch: "ambiguous", NonInteractive: true}); err == nil || !strings.Contains(err.Error(), "multiple remotes") {
		t.Fatal(err)
	}
	f.mustGit(f.dir, "branch", "ambiguous")
	if x := f.create(Options{Branch: "ambiguous"}); x.Action != "local" {
		t.Fatal(x)
	}
}

func TestNoFetchAndFailedFetch(t *testing.T) {
	f := setup(t)
	f.mustGit(f.dir, "remote", "add", "origin", filepath.Join(f.home, "missing.git"))
	if _, err := f.r.Resolve(context.Background(), "new", false); err != nil {
		t.Fatal(err)
	}
	if f.log.Len() != 0 {
		t.Fatal("--no-fetch attempted refresh")
	}
	if v, err := f.r.Resolve(context.Background(), "new", true); err != nil || v.Kind != "absent" {
		t.Fatalf("%+v %v", v, err)
	}
	if !strings.Contains(f.log.String(), "fetch failed") {
		t.Fatal("fetch failure hidden")
	}
	f.log.Reset()
	write(t, filepath.Join(f.r.common, "FETCH_HEAD"), "recent fetch\n", 0644)
	f.r.Resolve(context.Background(), "new", true)
	if f.log.Len() != 0 {
		t.Fatal("fresh refs fetched")
	}
	write(t, filepath.Join(f.r.common, "FETCH_HEAD"), "", 0644)
	f.r.Resolve(context.Background(), "new", true)
	if !strings.Contains(f.log.String(), "refreshing") {
		t.Fatal("empty FETCH_HEAD considered fresh")
	}
}

func TestSlotSafetyAndValidation(t *testing.T) {
	f := setup(t)
	for _, branch := range []string{"../escape", "/absolute", "foo/../../escape", "-option", "HEAD", "@{-1}", "bad name"} {
		if _, err := f.r.Create(context.Background(), Options{Branch: branch, NonInteractive: true}); err == nil {
			t.Fatalf("accepted %q", branch)
		}
	}
	if _, err := os.Stat(f.r.root); !os.IsNotExist(err) {
		t.Fatal("invalid branch created directories")
	}
	write(t, filepath.Join(f.r.root, "occupied", "precious"), "keep", 0644)
	write(t, filepath.Join(f.r.root, "file"), "keep", 0644)
	if err := os.Symlink(filepath.Join(f.r.root, "missing"), filepath.Join(f.r.root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.home, filepath.Join(f.r.root, "parent")); err != nil {
		t.Fatal(err)
	}
	for _, branch := range []string{"occupied", "file", "link", "parent/escaped"} {
		if _, err := f.r.Create(context.Background(), Options{Branch: branch, NonInteractive: true}); err == nil {
			t.Fatalf("accepted %q", branch)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(f.r.root, "occupied", "precious")); string(got) != "keep" {
		t.Fatal("slot changed")
	}
	if err := os.MkdirAll(filepath.Join(f.r.root, "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	f.create(Options{Branch: "empty"})
	// A registered missing checkout is still Git's responsibility; never force it.
	x := f.create(Options{Branch: "stale"})
	if err := os.RemoveAll(x.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.Create(context.Background(), Options{Branch: "stale", NonInteractive: true}); err == nil {
		t.Fatal("overrode stale registration")
	}
}

func TestGroupPlacesUnderFolderWithoutRenamingBranch(t *testing.T) {
	f := setup(t)
	x := f.create(Options{Branch: "cut-turn", Group: "ux"})
	want := filepath.Join(f.r.root, "ux", "cut-turn")
	if x.Path != want || x.Branch != "cut-turn" || f.mustGit(x.Path, "rev-parse", "--abbrev-ref", "HEAD") != "cut-turn" {
		t.Fatalf("%+v", x)
	}
	if p, err := f.r.Path(context.Background(), "ux", "cut-turn"); err != nil || p != want {
		t.Fatalf("path %q %v", p, err)
	}
	for _, group := range []string{"..", "../escape", "/absolute", "a//b", ".hidden", "bad name"} {
		if _, err := f.r.Path(context.Background(), group, "b"); err == nil {
			t.Fatalf("accepted group %q", group)
		}
		if _, err := f.r.Create(context.Background(), Options{Branch: "b", Group: group, NonInteractive: true}); err == nil {
			t.Fatalf("created under group %q", group)
		}
	}
	// A group named like an existing checkout would nest one worktree in another.
	f.create(Options{Branch: "ux-host"})
	if _, err := f.r.Create(context.Background(), Options{Branch: "inner", Group: "ux-host", NonInteractive: true}); err == nil || !strings.Contains(err.Error(), "parent is a checkout") {
		t.Fatalf("nested inside a checkout: %v", err)
	}
	if f.mustGit(f.dir, "branch", "--list", "inner") != "" {
		t.Fatal("refused creation still made the branch")
	}
}

func TestUnreadableSlot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a mode-000 directory")
	}
	f := setup(t)
	dest := filepath.Join(f.r.root, "unreadable")
	if err := os.MkdirAll(dest, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dest, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dest, 0755) })
	if _, err := f.r.Create(context.Background(), Options{Branch: "unreadable", NonInteractive: true}); err == nil {
		t.Fatal("unreadable directory was treated as empty")
	}
}

func TestSeedIgnoredAndPreserveCheckout(t *testing.T) {
	f := setup(t)
	write(t, filepath.Join(f.dir, ".gitignore"), ".env*\n.npmrc\nscripts.local/\nnode_modules/\n", 0644)
	f.mustGit(f.dir, "add", ".gitignore")
	f.mustGit(f.dir, "commit", "-qm", "ignore prerequisites")
	write(t, filepath.Join(f.dir, ".env"), "secret", 0600)
	write(t, filepath.Join(f.dir, "app", ".env.local"), "nested", 0600)
	write(t, filepath.Join(f.dir, "scripts.local", "run"), "executable", 0755)
	write(t, filepath.Join(f.dir, "node_modules", ".env"), "dependency", 0644)
	write(t, filepath.Join(f.dir, "wip"), "untracked", 0644)
	if err := os.Symlink(".env", filepath.Join(f.dir, ".env.link")); err != nil {
		t.Fatal(err)
	}
	x := f.create(Options{Branch: "seed"})
	if x.Copied != 4 || len(x.Warnings) != 0 {
		t.Fatalf("%+v", x)
	}
	if got, _ := os.ReadFile(filepath.Join(x.Path, "app", ".env.local")); string(got) != "nested" {
		t.Fatal(string(got))
	}
	if info, err := os.Stat(filepath.Join(x.Path, ".env")); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("%v %v", info, err)
	}
	if info, err := os.Stat(filepath.Join(x.Path, "scripts.local", "run")); err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("%v %v", info, err)
	}
	if got, err := os.Readlink(filepath.Join(x.Path, ".env.link")); err != nil || got != ".env" {
		t.Fatalf("%s %v", got, err)
	}
	for _, absent := range []string{"wip", "node_modules"} {
		if _, err := os.Stat(filepath.Join(x.Path, absent)); !os.IsNotExist(err) {
			t.Fatal("copied", absent)
		}
	}
	// Different branches can track paths that main ignores.
	write(t, filepath.Join(x.Path, ".env"), "tracked on target", 0600)
	f.mustGit(x.Path, "add", "-f", ".env")
	f.mustGit(x.Path, "commit", "-qm", "track env")
	y := f.create(Options{Branch: "preserve", Base: "seed"})
	if len(y.Warnings) != 1 {
		t.Fatalf("%+v", y)
	}
	if got, _ := os.ReadFile(filepath.Join(y.Path, ".env")); string(got) != "tracked on target" {
		t.Fatal("overwrote checkout")
	}
	if z := f.create(Options{Branch: "no-copy", NoCopy: true}); z.Copied != 0 {
		t.Fatal(z)
	}
	f.configure(`copy_globs = []`)
	if z := f.create(Options{Branch: "off"}); z.Copied != 0 {
		t.Fatal(z)
	}
	f.configure(`copy_globs = ["scripts.local"]`)
	if z := f.create(Options{Branch: "custom"}); z.Copied != 1 {
		t.Fatal(z)
	}
}

func TestFetchDeadlineKillsChild(t *testing.T) {
	f := setup(t)
	// A local Git remote helper reproduces an SSH/credential child that hangs.
	bin := filepath.Join(f.home, "bin")
	write(t, filepath.Join(bin, "git-remote-hang"), "#!/bin/sh\nsleep 30\n", 0755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	f.configure(`fetch.timeout = "200ms"`)
	f.mustGit(f.dir, "remote", "add", "origin", "hang::test")
	start := time.Now()
	if _, err := f.r.Resolve(context.Background(), "new", true); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("fetch hung: %v", elapsed)
	}
	if !strings.Contains(f.log.String(), "deadline exceeded") {
		t.Fatal(f.log.String())
	}
}

func TestMissingGitIsNotAbsent(t *testing.T) {
	f := setup(t)
	t.Setenv("PATH", t.TempDir())
	if _, err := f.r.Resolve(context.Background(), "new", false); err == nil {
		t.Fatal("failed query returned absent")
	}
}

func TestGitStderrDoesNotPolluteResult(t *testing.T) {
	f := setup(t)
	// Real Git worktree add normally prints HEAD to stdout; only the result path
	// should escape the engine. A checkout hook's stdout must be captured too.
	write(t, filepath.Join(f.r.common, "hooks", "post-checkout"), "#!/bin/sh\necho noisy-hook\n", 0755)
	x := f.create(Options{Branch: "hook"})
	if strings.Contains(x.Path, "noisy") {
		t.Fatal(x)
	}
}

func TestLinkedCreationUsesCallerHeadAndMainPrerequisites(t *testing.T) {
	f := setup(t)
	write(t, filepath.Join(f.dir, ".gitignore"), ".env\n", 0644)
	f.mustGit(f.dir, "add", ".gitignore")
	f.mustGit(f.dir, "commit", "-qm", "ignore prerequisite")
	caller := f.create(Options{Branch: "topic"})
	f.mustGit(caller.Path, "commit", "--allow-empty", "-qm", "topic work")
	write(t, filepath.Join(f.dir, ".env"), "main prerequisite", 0600)
	r, err := Open(context.Background(), caller.Path, f.home, &f.log)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Create(context.Background(), Options{Branch: "next-topic", NonInteractive: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Base != "topic" || f.mustGit(result.Path, "rev-parse", "HEAD") != f.mustGit(caller.Path, "rev-parse", "HEAD") {
		t.Fatalf("caller HEAD ignored: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(result.Path, ".env"))
	if err != nil || string(data) != "main prerequisite" {
		t.Fatalf("main seeding: %q %v", data, err)
	}
}
