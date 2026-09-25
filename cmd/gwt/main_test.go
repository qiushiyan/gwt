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
	if rc := call("path", "planned", "--group", "ux"); rc != 0 || stdout.String() != filepath.Join(home, "custom/project/ux/planned")+"\n" {
		t.Fatalf("%d: %s %s", rc, &stdout, &stderr)
	}
	if rc := call("path", "--group=ux", "planned"); rc != 0 || stdout.String() != filepath.Join(home, "custom/project/ux/planned")+"\n" {
		t.Fatalf("%d: %s %s", rc, &stdout, &stderr)
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
		{"config"}, {"config", "show", "--no-fetch"}, {"path", "a", "b"}, {"create", "a", "--force"}, {"remove"}, {"remove", "a", "b"},
		{"remove", "a", "b", "--json"},
		{"a", "--group"}, {"a", "--group="}, {"path", "--group", "ux"}, {"resolve", "a", "--group", "ux"}, {"remove", "a", "--group", "ux"},
	} {
		var out, stderr bytes.Buffer
		if rc := run(context.Background(), args, strings.NewReader(""), &out, &stderr); rc != 2 || out.Len() != 0 {
			t.Fatalf("%v: %d %s %s", args, rc, &out, &stderr)
		}
	}
}
