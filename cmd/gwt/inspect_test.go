package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiushiyan/gwt/internal/worktree"
)

func TestInspectCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GWT_CONFIG", "")
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	repo := filepath.Join(home, "project")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "initial"},
		{"-C", repo, "branch", "landed"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s: %v", out, err)
		}
	}
	t.Chdir(repo)
	var stdout, stderr bytes.Buffer
	call := func(args ...string) int {
		stdout.Reset()
		stderr.Reset()
		return run(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
	}

	if rc := call("list", "--json"); rc != 0 {
		t.Fatalf("list: %d %s", rc, &stderr)
	}
	var l worktree.Listing
	if err := json.Unmarshal(stdout.Bytes(), &l); err != nil || l.Trunk.Name != "main" || len(l.Worktrees) != 1 || !l.Worktrees[0].Main || !l.Worktrees[0].Current {
		t.Fatalf("%s %v", &stdout, err)
	}
	if rc := call("list"); rc != 0 || !strings.HasPrefix(stdout.String(), "trunk main ") || !strings.Contains(stdout.String(), "» ") {
		t.Fatalf("list text: %d %q", rc, &stdout)
	}
	if rc := call("trunk"); rc != 0 || !strings.HasPrefix(stdout.String(), "main ") {
		t.Fatalf("trunk: %d %q", rc, &stdout)
	}
	// One unknown branch fails the call; known ones still get verdicts.
	if rc := call("merged", "landed", "missing"); rc != 1 || stdout.String() != "merged\tlanded\n" || !strings.Contains(stderr.String(), "missing") {
		t.Fatalf("merged: %d %q %q", rc, &stdout, &stderr)
	}
	for _, args := range [][]string{
		{"list", "x"}, {"trunk", "x"}, {"merged"}, {"create", "a", "--fetch"}, {"remove", "a", "--fetch"}, {"path", "--fetch"},
	} {
		if rc := call(args...); rc != 2 || stdout.Len() != 0 {
			t.Errorf("%q: %d %q %q", args, rc, &stdout, &stderr)
		}
	}
}
