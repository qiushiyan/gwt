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
)

func TestCommandContract(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("WORKTREE_COPY_GLOBS", "off")
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	repo := filepath.Join(home, "project")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %s: %v", out, err)
		}
	}
	git("init", "-q", "-b", "main", repo)
	git("-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "initial")
	t.Chdir(repo)
	wantCwd, _ := os.Getwd()
	var stdout, stderr bytes.Buffer
	call := func(args ...string) int {
		stdout.Reset()
		stderr.Reset()
		return run(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
	}
	if rc := call("create", "agent/one", "--non-interactive"); rc != 0 {
		t.Fatalf("%d: %s", rc, &stderr)
	}
	want := filepath.Join(home, "dev", ".worktrees", "project", "agent/one")
	if stdout.String() != want+"\n" {
		t.Fatalf("stdout: %q", &stdout)
	}
	if got, _ := os.Getwd(); got != wantCwd {
		t.Fatal("changed working directory")
	}
	if rc := call("agent/two", "main", "--json"); rc != 0 {
		t.Fatalf("%d: %s", rc, &stderr)
	}
	var result struct{ Path, Branch, Action, Base string }
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Branch != "agent/two" || result.Action != "created" || result.Base != "main" {
		t.Fatalf("%+v", result)
	}
	if rc := call("resolve", "agent/two", "--no-fetch"); rc != 0 || stdout.String() != "local\n" {
		t.Fatalf("%d %q %s", rc, &stdout, &stderr)
	}
	if rc := call("decline"); rc != 1 || stdout.Len() != 0 {
		t.Fatalf("%d %q", rc, &stdout)
	}
	if !strings.Contains(stderr.String(), "--non-interactive") {
		t.Fatal(&stderr)
	}
	if rc := call("create", "agent/one", "--non-interactive"); rc != 1 || stdout.Len() != 0 {
		t.Fatalf("retry: %d %q", rc, &stdout)
	}
	if rc := call("--help"); rc != 0 || !strings.Contains(stdout.String(), "Usage:") {
		t.Fatal("help")
	}
}

func TestInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{}, {"--bogus"}, {"create"}, {"a", "b", "c"}, {"resolve", "a", "b"},
		{"resolve", "a", "--new"}, {"resolve", "a", "--no-copy"},
	} {
		var out, stderr bytes.Buffer
		if rc := run(context.Background(), args, strings.NewReader(""), &out, &stderr); rc != 2 || out.Len() != 0 {
			t.Fatalf("%v: %d %s %s", args, rc, &out, &stderr)
		}
	}
}
