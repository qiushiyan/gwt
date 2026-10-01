package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCommandContract(t *testing.T) {
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
	// Inspect and path are read-only and use the same config as create.
	configPath := filepath.Join(home, "config/gwt/config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("base = \"main\"\nworktree_root = \"~/custom\"\ncopy_globs = []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if rc := call("config", "show", "--json"); rc != 0 {
		t.Fatalf("%d: %s", rc, &stderr)
	}
	var cfg struct {
		Base    string
		Sources map[string]string
	}
	if err := json.Unmarshal(stdout.Bytes(), &cfg); err != nil || cfg.Base != "main" || cfg.Sources["base"] != configPath {
		t.Fatalf("%+v %v", cfg, err)
	}
	if rc := call("path", "planned"); rc != 0 || stdout.String() != filepath.Join(home, "custom/project/planned")+"\n" {
		t.Fatalf("%d: %s %s", rc, &stdout, &stderr)
	}
	if _, err := os.Stat(filepath.Join(home, "custom")); !os.IsNotExist(err) {
		t.Fatal("path created a directory")
	}
	if rc := call("path"); rc != 0 || stdout.String() != filepath.Join(home, "custom/project")+"\n" {
		t.Fatalf("%d %s", rc, &stdout)
	}
	// Removal uses the registration even after the root was changed.
	if rc := call("remove", "agent/one", "--json"); rc != 0 {
		t.Fatalf("%d: %s %s", rc, &stdout, &stderr)
	}
	var removed struct {
		OK              bool
		WorktreeRemoved bool `json:"worktree_removed"`
		BranchDeleted   bool `json:"branch_deleted"`
		Error           string
	}
	if err := json.Unmarshal(stdout.Bytes(), &removed); err != nil || !removed.OK || !removed.WorktreeRemoved || !removed.BranchDeleted {
		t.Fatalf("%+v %v", removed, err)
	}
	if rc := call("remove", "agent/one", "--json"); rc != 1 {
		t.Fatalf("missing removal: %d", rc)
	}
	if err := json.Unmarshal(stdout.Bytes(), &removed); err != nil || removed.OK || removed.Error == "" {
		t.Fatalf("%+v %v", removed, err)
	}
	// Several targets print one JSON line each; a failed target neither stops
	// the ones after it nor lets the command succeed.
	if rc := call("create", "agent/three", "--non-interactive"); rc != 0 {
		t.Fatalf("%d: %s", rc, &stderr)
	}
	if rc := call("remove", "gone/a", "agent/three", "./gone-b", "--json"); rc != 1 {
		t.Fatalf("batch: %d", rc)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	var batch [3]struct {
		Target, Error string
		OK            bool
		Removed       bool `json:"worktree_removed"`
	}
	for i := range lines[:min(3, len(lines))] {
		json.Unmarshal([]byte(lines[i]), &batch[i])
	}
	if len(lines) != 3 || batch[0].Target != "gone/a" || batch[0].Error == "" ||
		batch[1].Target != "agent/three" || !batch[1].OK || !batch[1].Removed ||
		batch[2].Target != "./gone-b" || batch[2].Error == "" {
		t.Fatalf("batch JSON: %q", &stdout)
	}
	// Even --no-copy must not hide a malformed config file.
	if err := os.WriteFile(configPath, []byte("copy_globs = [\"[\"]"), 0600); err != nil {
		t.Fatal(err)
	}
	if rc := call("create", "bad-config", "main", "--no-copy"); rc != 1 || stdout.Len() != 0 {
		t.Fatalf("%d %s", rc, &stdout)
	}
	if _, err := os.Stat(filepath.Join(home, "dev/.worktrees/project/bad-config")); !os.IsNotExist(err) {
		t.Fatal("invalid config created worktree")
	}
	if rc := call("remove", "agent/two", "--json"); rc != 1 {
		t.Fatalf("invalid config removal: %d", rc)
	}
	if err := json.Unmarshal(stdout.Bytes(), &removed); err != nil || removed.OK || removed.WorktreeRemoved || removed.Error == "" {
		t.Fatalf("config failure JSON: %+v %v", removed, err)
	}
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	t.Chdir(home)
	if rc := call("config", "show", "--json"); rc != 0 {
		t.Fatalf("global config outside repo: %d %s", rc, &stderr)
	}
	if rc := call("remove", "agent/two", "--json"); rc != 1 {
		t.Fatalf("outside repo removal: %d", rc)
	}
	if err := json.Unmarshal(stdout.Bytes(), &removed); err != nil || removed.OK || removed.Error == "" {
		t.Fatalf("outside repo JSON: %+v %v", removed, err)
	}
}

func TestInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{}, {"--bogus"}, {"create"}, {"a", "b", "c"}, {"resolve", "a", "b"},
		{"resolve", "a", "--new"}, {"resolve", "a", "--no-copy"},
		{"config"}, {"config", "show", "--no-fetch"}, {"path", "a", "b"}, {"create", "a", "--force"}, {"remove"},
		{"remove", "a", "b", "--expect-head", "HEAD"}, {"remove", "a", "--expect-head"}, {"create", "a", "--keep-branch"},
		{"list", "--discard-dirty"}, {"merged", "a", "--expect-head=HEAD"},
		{"--cd", "a"}, {"create", "a", "--cd"}, {"remove", "a", "--cd"}, {"path", "--cd"},
		{"path", "a", "--no-clipboard"}, {"remove", "a", "--no-clipboard"},
	} {
		var out, stderr bytes.Buffer
		if rc := run(context.Background(), args, strings.NewReader(""), &out, &stderr); rc != 2 || out.Len() != 0 {
			t.Fatalf("%v: %d %s %s", args, rc, &out, &stderr)
		}
	}
}

// Without the zsh wrapper, --cd must refuse before creating and name the fix.
func TestCDWithoutShellFunction(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("GWT_CONFIG", "")
	repo := filepath.Join(home, "project")
	if out, err := exec.Command("git", "init", "-q", "-b", "main", repo).CombinedOutput(); err != nil {
		t.Fatalf("git: %s: %v", out, err)
	}
	t.Chdir(repo)
	var out, stderr bytes.Buffer
	if rc := run(context.Background(), []string{"--cd", "-n", "feat/cd"}, strings.NewReader(""), &out, &stderr); rc != 2 {
		t.Fatalf("%d %s %s", rc, &out, &stderr)
	}
	if !strings.Contains(stderr.String(), "zsh gwt function") || !strings.Contains(stderr.String(), "zshreload") {
		t.Fatalf("stderr: %s", &stderr)
	}
	if _, err := os.Stat(filepath.Join(home, "dev", ".worktrees", "project", "feat/cd")); !os.IsNotExist(err) {
		t.Fatal("worktree created despite refused --cd")
	}
}

// Creation copies its path only for a person at a terminal, and a failed copy
// still succeeds with the path on stdout.
func TestClipboard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GWT_CONFIG", "")
	bin, clip := filepath.Join(home, "bin"), filepath.Join(home, "clipboard")
	if err := os.Mkdir(bin, 0755); err != nil {
		t.Fatal(err)
	}
	toclip := func(script string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(bin, "toclip"), []byte("#!/bin/sh\n"+script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	toclip(`[ "$1" = -q ] && cat > "$HOME/clipboard"` + "\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	repo := filepath.Join(home, "project")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "initial"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s: %v", out, err)
		}
	}
	t.Chdir(repo)
	var stdout, stderr bytes.Buffer
	create := func(branch string, flags ...string) string {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		os.Remove(clip)
		if rc := run(context.Background(), append([]string{"create", "-n", branch}, flags...), strings.NewReader(""), &stdout, &stderr); rc != 0 {
			t.Fatalf("%s: %d %s", branch, rc, &stderr)
		}
		want := filepath.Join(home, "dev", ".worktrees", "project", branch)
		var result struct{ Path string }
		if slices.Contains(flags, "--json") {
			json.Unmarshal(stdout.Bytes(), &result)
		} else if stdout.String() == want+"\n" {
			result.Path = want
		}
		if result.Path != want {
			t.Fatalf("%s stdout: %q", branch, &stdout)
		}
		return want
	}
	copied := func() string {
		got, err := os.ReadFile(clip)
		if os.IsNotExist(err) {
			return "<untouched>"
		}
		return string(got)
	}

	// Captured stderr, a pipe, and /dev/null are not a person watching.
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	for _, stream := range []io.Writer{&stderr, devNull, w} {
		if attended(stream) {
			t.Fatalf("attended(%T)", stream)
		}
	}
	create("captured")
	if got := copied(); got != "<untouched>" {
		t.Fatalf("captured stderr copied %q", got)
	}

	defer func(before func(io.Writer) bool) { attended = before }(attended)
	attended = func(io.Writer) bool { return true }
	if path := create("watched", "--json"); copied() != path {
		t.Fatalf("clipboard %q, want %q", copied(), path)
	}
	if path := create("watched-plain"); copied() != path || strings.Contains(stderr.String(), "clipboard") {
		t.Fatalf("clipboard %q, want %q; stderr %s", copied(), path, &stderr)
	}
	if create("opted-out", "--no-clipboard"); copied() != "<untouched>" {
		t.Fatalf("--no-clipboard copied %q", copied())
	}
	toclip("echo 'no terminal to send to' >&2\nexit 1\n")
	create("copy-fails")
	if !strings.Contains(stderr.String(), "path not copied to the clipboard: toclip: exit status 1: no terminal to send to") {
		t.Fatalf("stderr: %s", &stderr)
	}
}
