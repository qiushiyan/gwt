package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (string, string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GWT_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	return home, filepath.Join(home, "xdg", "gwt", "config.toml"), filepath.Join(home, "repo", ".git")
}

func put(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLayersAndSources(t *testing.T) {
	home, global, common := fixture(t)
	c, err := Load(home, common)
	if err != nil || c.Base != "HEAD" || c.Fetch.Timeout != 8*time.Second || c.Sources["copy_globs"] != "builtin" {
		t.Fatalf("%+v %v", c, err)
	}
	put(t, global, `base = "origin/main"
worktree_root = "~/trees"
copy_globs = [".env*", "global-only"]
[fetch]
max_age = "10m"
timeout = "1.5s"
`)
	repoFile := filepath.Join(common, "gwt.toml")
	put(t, repoFile, `base = "origin/develop"
copy_globs = [".npmrc"]
fetch.timeout = "3s"
`)
	c, err = Load(home, common)
	if err != nil {
		t.Fatal(err)
	}
	if c.Base != "origin/develop" || c.WorktreeRoot != filepath.Join(home, "trees") || !reflect.DeepEqual(c.CopyGlobs, []string{".npmrc"}) || c.Fetch.MaxAge != 10*time.Minute || c.Fetch.Timeout != 3*time.Second {
		t.Fatalf("%+v", c)
	}
	if c.Sources["base"] != repoFile || c.Sources["fetch.max_age"] != global || c.Sources["copy_globs"] != repoFile {
		t.Fatal(c.Sources)
	}
	put(t, repoFile, `copy_globs = []`)
	c, err = Load(home, common)
	if err != nil || c.CopyGlobs == nil || len(c.CopyGlobs) != 0 || c.Base != "origin/main" {
		t.Fatalf("%+v %v", c, err)
	}
	out, _ := json.Marshal(c)
	if !strings.Contains(string(out), `"copy_globs":[]`) || !strings.Contains(string(out), `"timeout":1.5`) {
		t.Fatal(string(out))
	}
}

func TestLocations(t *testing.T) {
	home, global, common := fixture(t)
	put(t, global, `base = "xdg"`)
	put(t, filepath.Join(home, ".config/gwt/config.toml"), `base = "home"`)
	explicit := filepath.Join(home, "alternate.toml")
	put(t, explicit, `base = "alternate"`)
	t.Setenv("GWT_CONFIG", explicit)
	c, err := Load(home, common)
	if err != nil || c.Base != "alternate" || c.Files.Global != explicit {
		t.Fatalf("%+v %v", c, err)
	}
	put(t, filepath.Join(common, "gwt.toml"), `base = "repository"`)
	c, err = Load(home, common)
	if err != nil || c.Base != "repository" {
		t.Fatalf("%+v %v", c, err)
	}
	t.Setenv("GWT_CONFIG", filepath.Join(home, "missing.toml"))
	if _, err := Load(home, common); err == nil {
		t.Fatal("missing explicit config ignored")
	}
	t.Setenv("GWT_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	c, err = Load(home, "")
	if err != nil || c.Base != "home" || c.Files.Repository != "" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestInvalidConfig(t *testing.T) {
	for name, content := range map[string]string{
		"unknown":        `worktree_rooot = "/tmp/worktrees"`,
		"nested unknown": `fetch.timout = "1s"`,
		"empty table":    `[typo]`,
		"syntax":         `base = [`,
		"type":           `copy_globs = "off"`,
		"bad pattern":    `copy_globs = ["["]`,
		"path pattern":   `copy_globs = ["app/.env"]`,
		"empty base":     `base = ""`,
		"relative root":  `worktree_root = "trees"`,
		"empty root":     `worktree_root = ""`,
		"zero time":      `fetch.timeout = "0s"`,
		"negative time":  `fetch.max_age = "-5m"`,
		"bad time":       `fetch.timeout = "soon"`,
	} {
		t.Run(name, func(t *testing.T) {
			home, global, common := fixture(t)
			put(t, global, content)
			if _, err := Load(home, common); err == nil || !strings.Contains(err.Error(), global) {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestLegacyEnvironmentDoesNotOverrideConfig(t *testing.T) {
	home, global, common := fixture(t)
	put(t, global, `copy_globs = [".env*"]`)
	t.Setenv("WORKTREE_COPY_GLOBS", "off")
	t.Setenv("WT_FETCH_TIMEOUT", "99")
	t.Setenv("WT_BASE_MAX_AGE_MIN", "99")
	c, err := Load(home, common)
	if err != nil || len(c.CopyGlobs) != 1 || c.Fetch.Timeout != 8*time.Second || c.Fetch.MaxAge != 5*time.Minute {
		t.Fatalf("%+v %v", c, err)
	}
}
