package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// world is one test's sandbox: a temporary HOME holding a repository, with
// global Git config, gwt config and the clipboard kept away from live state.
// Commit dates are fixed, so the same history has the same ids on every run.
type world struct {
	t     *testing.T
	home  string // as created
	real  string // the same directory as Git reports it (macOS /var is /private/var)
	repo  string
	stdin string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	home := t.TempDir()
	real, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, home: home, real: real, repo: filepath.Join(home, "project")}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GWT_CONFIG", "")
	t.Setenv("GIT_AUTHOR_DATE", "2026-09-01T12:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2026-09-01T12:00:00Z")
	t.Setenv("LC_ALL", "C") // Git's own messages, untranslated
	// Git treats an empty GIT_DIR as set, so these go completely.
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	// Creation copies to the clipboard when a person is watching; this toclip
	// keeps the real one out of reach if a test pretends someone is.
	bin := filepath.Join(home, "bin")
	w.write(filepath.Join(bin, "toclip"), "#!/bin/sh\n[ \"$1\" = -q ] && cat > \"$HOME/clipboard\"\n", 0755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	w.git(home, "init", "-q", "-b", "main", w.repo)
	w.identify(w.repo, "Test")
	w.git(w.repo, "commit", "--allow-empty", "-qm", "initial")
	t.Chdir(w.repo)
	return w
}

// run calls gwt as main does and returns what it printed and its status.
func (w *world) run(args ...string) (stdout, stderr string, code int) {
	var out, errs bytes.Buffer
	code = run(context.Background(), args, strings.NewReader(w.stdin), &out, &errs)
	return out.String(), errs.String(), code
}

func (w *world) git(dir string, args ...string) string {
	w.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if exit, ok := err.(*exec.ExitError); ok {
			stderr = string(exit.Stderr)
		}
		w.t.Fatalf("git %v in %s: %v: %s", args, dir, err, stderr)
	}
	return strings.TrimSpace(string(out))
}

func (w *world) identify(dir, name string) {
	w.t.Helper()
	w.git(dir, "config", "user.name", name)
	w.git(dir, "config", "user.email", strings.ToLower(name)+"@example.invalid")
}

func (w *world) write(path, content string, mode os.FileMode) {
	w.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		w.t.Fatal(err)
	}
}

// commit writes one file and commits it in dir.
func (w *world) commit(dir, file, content, message string) string {
	w.t.Helper()
	w.write(filepath.Join(dir, file), content, 0o644)
	w.git(dir, "add", file)
	w.git(dir, "commit", "-qm", message)
	return w.git(dir, "rev-parse", "HEAD")
}
