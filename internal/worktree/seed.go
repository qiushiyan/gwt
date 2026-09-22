package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func (r *Repo) seed(ctx context.Context, dest string) (int, []string) {
	patterns := r.config.CopyGlobs
	if len(patterns) == 0 {
		return 0, nil
	}
	files, err := git(ctx, r.main, "ls-files", "-oi", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return 0, []string{err.Error()}
	}
	copied := 0
	var warnings []string
	for _, rel := range strings.Split(files, "\x00") {
		rel = strings.TrimSuffix(rel, "/")
		if rel == "" {
			continue
		}
		match := false
		for _, pattern := range patterns {
			if ok, _ := filepath.Match(pattern, filepath.Base(rel)); ok {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		if err := copyIgnored(ctx, r.main, dest, rel); err != nil {
			warnings = append(warnings, fmt.Sprintf("could not seed %s: %v", rel, err))
		} else {
			copied++
		}
	}
	return copied, warnings
}

func copyIgnored(ctx context.Context, main, dest, rel string) error {
	if !filepath.IsLocal(rel) {
		return fmt.Errorf("invalid relative path")
	}
	target := filepath.Join(dest, rel)
	if err := safeParents(dest, filepath.Dir(target)); err != nil {
		return err
	}
	// A file ignored on main may be tracked on the target branch. Preserve the
	// checkout, including symlinks and files created by a post-checkout hook.
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return fmt.Errorf("destination already exists; kept checkout version")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	// Native cp preserves modes, timestamps, and symlinks without following them.
	cmd := exec.CommandContext(ctx, "cp", "-pPR", filepath.Join(main, rel), target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("cp: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
