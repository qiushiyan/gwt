// Package config loads personal defaults once per invocation, before Git writes.
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type Fetch struct {
	MaxAge  time.Duration
	Timeout time.Duration
}

// JSON uses seconds so callers need not implement Go's duration grammar.
func (f Fetch) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		MaxAge  float64 `json:"max_age"`
		Timeout float64 `json:"timeout"`
	}{f.MaxAge.Seconds(), f.Timeout.Seconds()})
}

type Config struct {
	Base         string            `json:"base"`
	WorktreeRoot string            `json:"worktree_root"`
	CopyGlobs    []string          `json:"copy_globs"`
	Fetch        Fetch             `json:"fetch"`
	Sources      map[string]string `json:"sources"`
	Files        Files             `json:"files"`
}

type Files struct {
	Global     string `json:"global"`
	Repository string `json:"repository,omitempty"`
}

func defaults(home string) Config {
	return Config{
		Base:         "HEAD",
		WorktreeRoot: filepath.Join(home, "dev", ".worktrees"),
		CopyGlobs:    []string{".env*", ".npmrc", "scripts.local", ".duet", "docs.local"},
		Fetch:        Fetch{MaxAge: 5 * time.Minute, Timeout: 8 * time.Second},
		Sources: map[string]string{
			"base": "builtin", "worktree_root": "builtin", "copy_globs": "builtin",
			"fetch.max_age": "builtin", "fetch.timeout": "builtin",
		},
	}
}

// Load reads global config, then the shared Git directory's optional gwt.toml.
// An explicit GWT_CONFIG replaces only the global file and must exist.
// Repository config is shared by linked worktrees, independent of cwd/branch.
func Load(home, common string) (Config, error) {
	c := defaults(home)
	global := os.Getenv("GWT_CONFIG")
	explicit := global != ""
	if !explicit {
		dir := os.Getenv("XDG_CONFIG_HOME")
		if dir == "" {
			dir = filepath.Join(home, ".config")
		}
		global = filepath.Join(dir, "gwt", "config.toml")
	}
	var err error
	global, err = expandPath(global, home)
	if err != nil {
		return Config{}, fmt.Errorf("global config path: %w", err)
	}
	c.Files.Global = global
	if common != "" {
		c.Files.Repository = filepath.Join(common, "gwt.toml")
	}
	if err := c.read(global, home, !explicit); err != nil {
		return Config{}, err
	}
	if c.Files.Repository != "" {
		if err := c.read(c.Files.Repository, home, true); err != nil {
			return Config{}, err
		}
	}
	return c, nil
}

// Pointer fields distinguish an omitted setting from an explicit empty value.
// Arrays replace lower-precedence arrays, including [] to disable copying.
type fileConfig struct {
	Base         *string   `toml:"base"`
	WorktreeRoot *string   `toml:"worktree_root"`
	CopyGlobs    *[]string `toml:"copy_globs"`
	Fetch        struct {
		MaxAge  *string `toml:"max_age"`
		Timeout *string `toml:"timeout"`
	} `toml:"fetch"`
}

func (c *Config) read(path, home string, optional bool) error {
	data, err := os.ReadFile(path)
	if optional && os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	var f fileConfig
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	if keys := md.Undecoded(); len(keys) > 0 {
		return fmt.Errorf("config %s: unknown key %s", path, keys[0])
	}
	invalid := func(key string, err error) error { return fmt.Errorf("config %s: %s: %w", path, key, err) }
	if f.Base != nil {
		if *f.Base == "" || strings.TrimSpace(*f.Base) != *f.Base || strings.HasPrefix(*f.Base, "-") {
			return invalid("base", fmt.Errorf("use HEAD or a Git ref such as origin/main"))
		}
		c.Base, c.Sources["base"] = *f.Base, path
	}
	if f.WorktreeRoot != nil {
		root, err := expandPath(*f.WorktreeRoot, home)
		if err != nil {
			return invalid("worktree_root", err)
		}
		c.WorktreeRoot, c.Sources["worktree_root"] = root, path
	}
	if f.CopyGlobs != nil {
		for _, pattern := range *f.CopyGlobs {
			if pattern == "" || strings.Contains(pattern, "/") {
				return invalid("copy_globs", fmt.Errorf("%q must be a nonempty basename glob", pattern))
			}
			if _, err := filepath.Match(pattern, ""); err != nil {
				return invalid("copy_globs", fmt.Errorf("%q: %w", pattern, err))
			}
		}
		c.CopyGlobs = append([]string{}, (*f.CopyGlobs)...)
		c.Sources["copy_globs"] = path
	}
	for _, d := range []struct {
		key   string
		value *string
		dst   *time.Duration
	}{
		{"fetch.max_age", f.Fetch.MaxAge, &c.Fetch.MaxAge},
		{"fetch.timeout", f.Fetch.Timeout, &c.Fetch.Timeout},
	} {
		if d.value == nil {
			continue
		}
		value, err := time.ParseDuration(*d.value)
		if err != nil || value <= 0 {
			return invalid(d.key, fmt.Errorf("use a positive duration such as 5m or 8s"))
		}
		*d.dst, c.Sources[d.key] = value, path
	}
	return nil
}

func expandPath(path, home string) (string, error) {
	if path == "~" {
		path = home
	} else if strings.HasPrefix(path, "~/") {
		path = filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("use an absolute path or ~/path")
	}
	return filepath.Clean(path), nil
}

func (c Config) Show(w io.Writer) error {
	_, err := fmt.Fprintf(w, "Global: %s\nRepository: %s\n\nbase = %q  (%s)\nworktree_root = %q  (%s)\ncopy_globs = %q  (%s)\nfetch.max_age = %s  (%s)\nfetch.timeout = %s  (%s)\n",
		c.Files.Global, c.Files.Repository, c.Base, c.Sources["base"], c.WorktreeRoot, c.Sources["worktree_root"], c.CopyGlobs, c.Sources["copy_globs"], c.Fetch.MaxAge, c.Sources["fetch.max_age"], c.Fetch.Timeout, c.Sources["fetch.timeout"])
	return err
}
