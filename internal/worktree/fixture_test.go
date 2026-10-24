package worktree

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
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

func (f *fixture) create(o CreateOptions) Result {
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

func byBranch(t *testing.T, l Listing) map[string]Worktree {
	t.Helper()
	m := map[string]Worktree{}
	for _, w := range l.Worktrees {
		key := w.Branch
		if key == "" {
			key = "(detached) " + filepath.Base(w.Path)
		}
		m[key] = w
	}
	return m
}

func isTrue(b *bool) bool { return b != nil && *b }

// remoteFixture gives the repository an origin whose HEAD is develop, and a
// second clone standing in for GitHub, where merges land without our fetch.
func remoteFixture(t *testing.T, f *fixture) (origin, upstream string) {
	t.Helper()
	origin = filepath.Join(f.home, "origin.git")
	f.mustGit(f.home, "init", "-q", "--bare", "-b", "main", origin)
	f.mustGit(f.dir, "remote", "add", "origin", origin)
	f.mustGit(f.dir, "branch", "develop")
	f.mustGit(f.dir, "push", "-q", "origin", "main", "develop")
	f.mustGit(origin, "symbolic-ref", "HEAD", "refs/heads/develop")
	f.mustGit(f.dir, "remote", "set-head", "origin", "develop")
	upstream = filepath.Join(f.home, "github")
	f.mustGit(f.home, "clone", "-q", "-b", "develop", origin, upstream)
	f.mustGit(upstream, "config", "user.name", "GitHub")
	f.mustGit(upstream, "config", "user.email", "github@example.invalid")
	return origin, upstream
}

func remove1(r *Repo, target string, o RemoveOptions) (Removal, error) {
	x := r.Remove(context.Background(), []string{target}, o)[0]
	if !x.OK {
		return x, errors.New(x.Error)
	}
	return x, nil
}

func (f *fixture) exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func (f *fixture) hasRef(ref string) bool {
	_, err := git(context.Background(), f.dir, "show-ref", "--verify", "--quiet", ref)
	return err == nil
}
