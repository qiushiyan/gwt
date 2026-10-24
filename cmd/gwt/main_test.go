package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Every command reads the same configuration: path and creation place by it,
// removal finds a checkout by its registration after the root moved, and a
// malformed file stops creation and removal before either acts. No command
// changes the caller's directory.
func TestConfigurationReachesEveryCommand(t *testing.T) {
	w := newWorld(t)
	wantCwd, _ := os.Getwd()
	if out, errs, code := w.run("create", "agent/one", "--non-interactive"); code != 0 || out != filepath.Join(w.home, "dev/.worktrees/project/agent/one")+"\n" {
		t.Fatalf("%d %q %s", code, out, errs)
	}
	if got, _ := os.Getwd(); got != wantCwd {
		t.Fatal("changed working directory")
	}
	if _, errs, code := w.run("create", "agent/two", "--non-interactive"); code != 0 {
		t.Fatalf("%d %s", code, errs)
	}

	configPath := filepath.Join(w.home, "config/gwt/config.toml")
	w.write(configPath, "base = \"main\"\nworktree_root = \"~/custom\"\ncopy_globs = []\n", 0o600)
	out, errs, code := w.run("config", "show", "--json")
	var cfg struct {
		Base    string
		Sources map[string]string
	}
	if err := json.Unmarshal([]byte(out), &cfg); code != 0 || err != nil || cfg.Base != "main" || cfg.Sources["base"] != configPath {
		t.Fatalf("%d %+v %v %s", code, cfg, err, errs)
	}
	if out, _, code := w.run("path", "planned"); code != 0 || out != filepath.Join(w.home, "custom/project/planned")+"\n" {
		t.Fatalf("path: %d %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(w.home, "custom")); !os.IsNotExist(err) {
		t.Fatal("path created a directory")
	}
	if out, _, code := w.run("path"); code != 0 || out != filepath.Join(w.home, "custom/project")+"\n" {
		t.Fatalf("path root: %d %q", code, out)
	}
	type removal struct {
		OK              bool
		WorktreeRemoved bool `json:"worktree_removed"`
		BranchDeleted   bool `json:"branch_deleted"`
		Error           string
	}
	var removed removal
	out, errs, code = w.run("remove", "agent/one", "--json")
	if err := json.Unmarshal([]byte(out), &removed); code != 0 || err != nil || !removed.OK || !removed.WorktreeRemoved || !removed.BranchDeleted {
		t.Fatalf("removal after the root moved: %d %+v %v %s", code, removed, err, errs)
	}

	// Even --no-copy must not hide a malformed config file.
	w.write(configPath, "copy_globs = [\"[\"]", 0o600)
	if out, _, code := w.run("create", "bad-config", "main", "--no-copy"); code != 1 || out != "" {
		t.Fatalf("%d %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(w.home, "dev/.worktrees/project/bad-config")); !os.IsNotExist(err) {
		t.Fatal("invalid config created worktree")
	}
	removed = removal{}
	out, _, code = w.run("remove", "agent/two", "--json")
	if err := json.Unmarshal([]byte(out), &removed); code != 1 || err != nil || removed.OK || removed.WorktreeRemoved || removed.Error == "" {
		t.Fatalf("config failure JSON: %d %+v %v", code, removed, err)
	}
}

// Invalid argv exits 2 with nothing on stdout, whatever the command.
func TestInvalidArguments(t *testing.T) {
	w := newWorld(t)
	for _, args := range [][]string{
		{}, {"--bogus"}, {"create"}, {"a", "b", "c"}, {"resolve", "a", "b"},
		{"resolve", "a", "--new"}, {"resolve", "a", "--no-copy"},
		{"config"}, {"config", "show", "--no-fetch"}, {"path", "a", "b"}, {"create", "a", "--force"}, {"remove"},
		{"remove", "a", "b", "--expect-head", "HEAD"}, {"remove", "a", "--expect-head"}, {"create", "a", "--keep-branch"},
		{"list", "--discard-dirty"}, {"merged", "a", "--expect-head=HEAD"},
		{"--cd", "a"}, {"create", "a", "--cd"}, {"remove", "a", "--cd"}, {"path", "--cd"},
		{"path", "a", "--no-clipboard"}, {"remove", "a", "--no-clipboard"},
		{"list", "x"}, {"trunk", "x"}, {"merged"}, {"create", "a", "--fetch"}, {"remove", "a", "--fetch"}, {"path", "--fetch"},
		{"merged", "landed", "--into"}, {"merged", "--into=", "landed"}, {"merged", "--into", "main", "--fetch", "landed"},
		{"list", "--into", "main"}, {"remove", "landed", "--into", "main"},
	} {
		if out, errs, code := w.run(args...); code != 2 || out != "" {
			t.Errorf("%q: %d %q %q", args, code, out, errs)
		}
	}
}

// Creation copies its path only for a person at a terminal, and a failed copy
// still succeeds with the path on stdout.
func TestClipboard(t *testing.T) {
	w := newWorld(t)
	clip := filepath.Join(w.home, "clipboard")
	toclip := func(script string) {
		t.Helper()
		w.write(filepath.Join(w.home, "bin", "toclip"), "#!/bin/sh\n"+script, 0o755)
	}
	var errs string
	create := func(branch string, flags ...string) string {
		t.Helper()
		os.Remove(clip)
		var out string
		var code int
		out, errs, code = w.run(append([]string{"create", "-n", branch}, flags...)...)
		if code != 0 {
			t.Fatalf("%s: %d %s", branch, code, errs)
		}
		want := filepath.Join(w.home, "dev", ".worktrees", "project", branch)
		var result struct{ Path string }
		if slices.Contains(flags, "--json") {
			json.Unmarshal([]byte(out), &result)
		} else if out == want+"\n" {
			result.Path = want
		}
		if result.Path != want {
			t.Fatalf("%s stdout: %q", branch, out)
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
	r, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer pw.Close()
	for _, stream := range []io.Writer{&strings.Builder{}, devNull, pw} {
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
	if path := create("watched-plain"); copied() != path || strings.Contains(errs, "clipboard") {
		t.Fatalf("clipboard %q, want %q; stderr %s", copied(), path, errs)
	}
	if create("opted-out", "--no-clipboard"); copied() != "<untouched>" {
		t.Fatalf("--no-clipboard copied %q", copied())
	}
	toclip("echo 'no terminal to send to' >&2\nexit 1\n")
	create("copy-fails")
	if !strings.Contains(errs, "path not copied to the clipboard: toclip: exit status 1: no terminal to send to") {
		t.Fatalf("stderr: %s", errs)
	}
}
