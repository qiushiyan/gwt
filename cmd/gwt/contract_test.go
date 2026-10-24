package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The output other programs read, byte for byte, for the argv each one
// sends: brief (resolve, path, create --json, the remove commands closeout
// emits), drive (merged --json), the tmux popup (list --json, trunk --fetch
// --json, create, remove --json, merged --json), rex-worktree and the /wt mod
// (create), the clean-worktrees scripts (merged --into, remove --keep-branch
// --expect-head, config show --json), and agents reading the text forms.
// AGENTS.md names each consumer. Help pages are TestHelpPages'.
//
// Each case runs in process, through the entry main uses. With
// GWT_CONTRACT_BIN=/abs/path/to/gwt it runs that binary instead, which is how
// a build is checked against the files, the old one before a change that must
// keep its bytes included. A file that changes changes what those callers
// read, so change them in step: GWT_CONTRACT_UPDATE=1 rewrites the files, and
// the diff is reviewed like code.
func TestContracts(t *testing.T) {
	c := contractWorld(newWorld(t))
	tip := map[string]string{}
	for _, b := range []string{"landed", "open", "origin/main", "origin/remote-only"} {
		tip[b] = c.sha(b)
	}

	// Resolution, before creation turns remote names into local branches.
	c.pin("resolve-local", "resolve", "open")
	c.pin("resolve-remote", "resolve", "remote-only")
	c.pin("resolve-ambiguous", "resolve", "twice")
	c.pin("resolve-absent", "resolve", "nothing-here")
	c.pin("resolve-absent-json", "resolve", "nothing-here", "--no-fetch", "--json")
	c.pin("path", "path", "feat/claim")
	c.pin("path-root", "path")
	c.pin("path-json", "path", "feat/claim", "--json")
	c.pin("config-show", "config", "show")
	c.pin("config-show-json", "config", "show", "--json")
	c.pin("config-json-show", "config", "--json", "show")
	c.expect(!c.exists(c.path("feat/claim")), "path created the checkout's directory")

	// Creation in each form callers use: brief's flags after positionals, the
	// popup's and agents' plain path line, the bare form a person types.
	c.pin("create-brief-json", "create", "--non-interactive", "feat/new", "main", "--new", "--json")
	c.expect(c.exists(filepath.Join(c.path("feat/new"), ".env")) && c.hasRef("refs/heads/feat/new"), "feat/new was not created and seeded")
	c.pin("create-plain", "create", "--non-interactive", "feat/plain")
	c.pin("create-bare", "feat/bare", "main")
	c.pin("create-local", "create", "-n", "--no-copy", "open", "main")
	c.expect(!c.exists(filepath.Join(c.path("open"), ".env")), "--no-copy seeded the checkout")
	c.pin("create-remote-json", "create", "--non-interactive", "remote-only", "--json")
	c.pin("create-ambiguous", "create", "--non-interactive", "twice")
	c.pin("create-occupied", "create", "--non-interactive", "feat/plain")
	c.pin("create-declined", "decline")
	c.expect(!c.hasRef("refs/heads/decline") && !c.exists(c.path("decline")), "a declined creation created")
	c.pin("create-cd", "--cd", "-n", "feat/cd")
	c.expect(!c.exists(c.path("feat/cd")), "--cd created despite refusing")

	// Listing and verdicts: a dirty checkout, a locked one, merged and not.
	c.write(filepath.Join(c.path("feat/plain"), "scratch.txt"), "work in progress\n", 0o644)
	c.git(c.repo, "worktree", "lock", "--reason", "pinned", c.path("feat/bare"))
	c.pin("list-json", "list", "--json")
	c.pin("list", "list")
	c.pin("trunk", "trunk")
	c.pin("trunk-fetch-fresh-json", "trunk", "--fetch", "--json")
	c.pin("merged-json", "merged", "landed", "squashed", "open", "nope", tip["open"], "--json")
	c.pin("merged", "merged", "landed", "open")
	c.pin("merged-into-json", "merged", "--json", "--into", tip["origin/main"], tip["landed"])

	// A stale trunk: shown as such, then refreshed by --fetch.
	c.age("FETCH_HEAD")
	c.pin("trunk-stale", "trunk")
	c.pin("trunk-fetch-stale-json", "trunk", "--fetch", "--json")

	// Removal in each form its callers send.
	c.pin("remove-merged-json", "remove", "feat/new", "--json")
	c.expect(!c.exists(c.path("feat/new")) && !c.hasRef("refs/heads/feat/new"), "feat/new survived its removal")
	out := c.pin("remove-force-expect-head-json", "remove", "open", "--force", "--expect-head", tip["open"], "--json")
	c.expect(!c.exists(c.path("open")) && !c.hasRef("refs/heads/open"), "open survived its removal")
	c.expect(c.sha(c.recoveryRef(out)) == tip["open"], "the recovery ref does not hold open's tip")
	c.pin("remove-keep-branch-runner-json", "remove", "--keep-branch", "--expect-head", tip["origin/remote-only"], "--json", c.path("remote-only"))
	c.expect(!c.exists(c.path("remote-only")) && c.hasRef("refs/heads/remote-only"), "--keep-branch took the branch or left the checkout")
	c.pin("remove-batch-json", "remove", "--json", "gone/a", "feat/plain", "./gone-b")
	c.expect(c.exists(filepath.Join(c.path("feat/plain"), "scratch.txt")), "a refused target lost its files")
	plain := c.sha("feat/plain")
	out = c.pin("remove-discard-dirty-json", "remove", "--json", "--keep-branch", "--discard-dirty", c.path("feat/plain"))
	ref := c.recoveryRef(out)
	c.expect(!c.exists(c.path("feat/plain")) && c.hasRef("refs/heads/feat/plain"), "--discard-dirty --keep-branch left the checkout or took the branch")
	c.expect(c.sha(ref+"^1") == plain && c.git(c.repo, "show", ref+":scratch.txt") == "work in progress", "the snapshot does not hold the discarded work on its HEAD")
	c.pin("remove-branch-only", "remove", "squashed")
	c.expect(!c.hasRef("refs/heads/squashed"), "squashed survived its removal")
	c.pin("remove-locked", "remove", "feat/bare")
	c.expect(c.exists(c.path("feat/bare")), "a locked checkout was removed")
	c.ok("create", "-n", "done")
	done := c.commit(c.path("done"), "done.txt", "done\n", "unmerged work")
	out = c.pin("remove-force", "remove", "done", "--force")
	_, kept, _ := strings.Cut(out, "Kept ")
	ref, _, _ = strings.Cut(kept, ";")
	c.expect(c.sha(ref) == done, "the text recovery line does not name done's tip")

	// What a command reports per target on stderr when one fails.
	c.pin("merged-missing", "merged", "landed", "nope")
	c.ok("create", "-n", "broken")
	unreadable := filepath.Join(c.path("broken"), ".git")
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(unreadable, 0o644) })
	c.pin("list-unreadable", "list")
	c.pin("list-unreadable-json", "list", "--json")

	// The first word picks the command: anywhere else, a command's name is a
	// branch, so a misplaced flag cannot turn creation into removal.
	c.pin("create-command-name-later", "-n", "remove", "landed")
	c.expect(c.sha("remove") == tip["landed"] && c.hasRef("refs/heads/landed"), "-n remove landed did not create remove from landed")

	// Invalid argv: refused before anything runs.
	c.pin("usage-empty")
	c.pin("usage-unknown-flag", "--bogus")
	c.pin("usage-expect-head-batch", "remove", "a", "b", "--expect-head", "HEAD")
	c.pin("usage-into-empty", "merged", "--into=", "landed")
	c.pin("usage-into-fetch", "merged", "landed", "--into", "main", "--fetch")
	c.pin("usage-list-argument", "list", "x")
	c.pin("usage-create-remove-flag", "create", "a", "--force")
	c.expect(!c.hasRef("refs/heads/a"), "a refused creation created")

	// Outside any repository: removal still prints one result per target,
	// configuration shows the global layer, and argv errors come first.
	t.Chdir(c.home)
	c.pin("outside-remove-json", "remove", "x", "./y", "--json")
	c.pin("outside-config-show-json", "config", "show", "--json")
	c.pin("outside-list-json", "list", "--json")
	c.pin("outside-create-cd", "--cd", "-n", "x")
	c.pin("outside-remove-expect-head-batch", "remove", "a", "b", "--expect-head", "HEAD")
}

type contract struct {
	*world
	origin string
}

// contractWorld gives the repository an origin standing in for GitHub, where
// landed and squashed work reached main, a branch that exists only there, one
// that two remotes share, an unmerged local branch, and an ignored .env that
// creation seeds.
func contractWorld(w *world) *contract {
	c := &contract{world: w, origin: filepath.Join(w.home, "origin.git")}
	w.write(filepath.Join(w.repo, ".gitignore"), ".env\n", 0o644)
	w.git(w.repo, "add", ".gitignore")
	w.git(w.repo, "commit", "-qm", "ignore .env")
	w.write(filepath.Join(w.repo, ".env"), "TOKEN=test\n", 0o600)

	w.git(w.home, "init", "-q", "--bare", "-b", "main", c.origin)
	w.git(w.repo, "remote", "add", "origin", c.origin)
	w.git(w.repo, "push", "-q", "origin", "main")

	w.git(w.repo, "switch", "-qc", "landed")
	w.commit(w.repo, "landed.txt", "landed\n", "landed work")
	w.git(w.repo, "push", "-q", "origin", "landed:main")
	w.git(w.repo, "switch", "-qc", "squashed", "main")
	w.commit(w.repo, "squashed.txt", "one\n", "squashed work, part one")
	w.commit(w.repo, "squashed.txt", "one\ntwo\n", "squashed work, part two")
	w.git(w.repo, "switch", "-qc", "open", "main")
	w.commit(w.repo, "open.txt", "open\n", "open work")
	w.git(w.repo, "push", "-q", "origin", "open:twice")
	fork := filepath.Join(w.home, "fork.git")
	w.git(w.home, "init", "-q", "--bare", "-b", "main", fork)
	w.git(w.repo, "remote", "add", "fork", fork)
	w.git(w.repo, "push", "-q", "fork", "open:twice")
	w.git(w.repo, "switch", "-q", "main")

	github := filepath.Join(w.home, "github")
	w.git(w.home, "clone", "-q", c.origin, github)
	w.identify(github, "GitHub")
	w.commit(github, "squashed.txt", "one\ntwo\n", "Squashed work (#2)")
	w.git(github, "push", "-q", "origin", "main")
	w.git(github, "switch", "-qc", "remote-only")
	w.commit(github, "remote.txt", "remote\n", "remote-only work")
	w.git(github, "push", "-q", "origin", "remote-only")

	// A fetch seconds old: fresh against the five-minute window until age().
	w.git(w.repo, "fetch", "-q", "--all")
	w.git(w.repo, "remote", "set-head", "origin", "main")
	return c
}

// run calls gwt in process, or the binary GWT_CONTRACT_BIN names, and says
// which process ran it.
func (c *contract) run(args ...string) (stdout, stderr string, code, pid int) {
	bin := os.Getenv("GWT_CONTRACT_BIN")
	if bin == "" {
		stdout, stderr, code = c.world.run(args...)
		return stdout, stderr, code, os.Getpid()
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin = strings.NewReader(c.stdin)
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Run(); err != nil {
		if _, ok := errors.AsType[*exec.ExitError](err); !ok {
			c.t.Fatalf("%s: %v", bin, err)
		}
	}
	return out.String(), errs.String(), cmd.ProcessState.ExitCode(), cmd.Process.Pid
}

// ok runs a setup step through gwt; its output is no contract.
func (c *contract) ok(args ...string) {
	c.t.Helper()
	if _, stderr, code, _ := c.run(args...); code != 0 {
		c.t.Fatalf("gwt %v: exit %d: %s", args, code, stderr)
	}
}

func (c *contract) expect(ok bool, failure string) {
	c.t.Helper()
	if !ok {
		c.t.Error(failure)
	}
}

func (c *contract) sha(rev string) string {
	return c.git(c.repo, "rev-parse", "--verify", "--quiet", rev)
}

func (c *contract) hasRef(ref string) bool {
	return exec.Command("git", "-C", c.repo, "show-ref", "--verify", "--quiet", ref).Run() == nil
}

func (c *contract) exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func (c *contract) path(branch string) string {
	return filepath.Join(c.home, "dev", ".worktrees", "project", branch)
}

// recoveryRef is the recovery_ref of a removal's one JSON line.
func (c *contract) recoveryRef(stdout string) string {
	c.t.Helper()
	var r struct {
		Ref string `json:"recovery_ref"`
	}
	if err := json.Unmarshal([]byte(stdout), &r); err != nil || r.Ref == "" {
		c.t.Fatalf("no recovery_ref in %q: %v", stdout, err)
	}
	return r.Ref
}

// age backdates a file in the shared Git directory past every freshness window.
func (c *contract) age(name string) {
	c.t.Helper()
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(filepath.Join(c.repo, ".git", name), old, old); err != nil {
		c.t.Fatal(err)
	}
}

// contractDir is absolute because every world changes into its repository.
var contractDir, _ = filepath.Abs(filepath.Join("testdata", "contract"))

var batchID = regexp.MustCompile(`refs/wt-trash/([0-9]+)\.([0-9]+)/`)

// pin runs gwt, compares what it printed with the case's file, and returns
// stdout as printed. Paths under the temporary home read $HOME. A removal's
// batch id reads <batch> only once it is shown to be this call's: its epoch
// within the call and its pid the process that ran it; any other id stays,
// and the comparison fails on it.
func (c *contract) pin(name string, args ...string) string {
	c.t.Helper()
	from := time.Now().Unix()
	stdout, stderr, code, pid := c.run(args...)
	to := time.Now().Unix()
	normal := func(s string) string {
		s = strings.ReplaceAll(s, c.real, "$HOME")
		s = strings.ReplaceAll(s, c.home, "$HOME")
		return batchID.ReplaceAllStringFunc(s, func(id string) string {
			m := batchID.FindStringSubmatch(id)
			epoch, _ := strconv.ParseInt(m[1], 10, 64)
			if epoch < from || epoch > to || m[2] != strconv.Itoa(pid) {
				return id
			}
			return "refs/wt-trash/<batch>/"
		})
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = a
		if a == "" || strings.ContainsAny(a, " \t") {
			quoted[i] = fmt.Sprintf("%q", a)
		}
	}
	got := normal(fmt.Sprintf("$ gwt %s\n--- stdout\n%s--- stderr\n%s--- exit %d\n", strings.Join(quoted, " "), stdout, stderr, code))
	file := filepath.Join(contractDir, name+".txt")
	if os.Getenv("GWT_CONTRACT_UPDATE") == "1" {
		c.write(file, got, 0o644)
		return stdout
	}
	want, err := os.ReadFile(file)
	if err != nil {
		c.t.Errorf("%s: %v (GWT_CONTRACT_UPDATE=1 writes it)", name, err)
		return stdout
	}
	if got != string(want) {
		c.t.Errorf("%s differs from %s:\n--- got\n%s--- want\n%s", name, file, got, want)
	}
	return stdout
}
