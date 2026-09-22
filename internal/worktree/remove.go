package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Removal struct {
	OK              bool   `json:"ok"`
	Path            string `json:"path,omitempty"`
	Branch          string `json:"branch"`
	WorktreeRemoved bool   `json:"worktree_removed"`
	BranchDeleted   bool   `json:"branch_deleted"`
	Error           string `json:"error,omitempty"`
}

// Remove is deliberately non-interactive. The named branch identifies a Git
// registration, not a directory guessed from config (roots can change).
// Force permits deleting unmerged commits, never a dirty or locked checkout.
func (r *Repo) Remove(ctx context.Context, branch string, force bool) (Removal, error) {
	result := Removal{Branch: branch}
	if err := r.validate(ctx, branch); err != nil {
		return result, err
	}
	list, err := git(ctx, r.dir, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return result, err
	}
	for _, record := range strings.Split(list, "\x00\x00") {
		var path, ref string
		locked := false
		for _, field := range strings.Split(record, "\x00") {
			switch {
			case strings.HasPrefix(field, "worktree "):
				path = strings.TrimPrefix(field, "worktree ")
			case strings.HasPrefix(field, "branch "):
				ref = strings.TrimPrefix(field, "branch ")
			case field == "locked" || strings.HasPrefix(field, "locked "):
				locked = true
			}
		}
		if ref != "refs/heads/"+branch {
			continue
		}
		if result.Path != "" {
			return result, fmt.Errorf("branch %q is checked out in multiple worktrees; inspect git worktree list before choosing a checkout to remove", branch)
		}
		result.Path = path
		if locked {
			return result, fmt.Errorf("worktree is locked: %s; unlock it explicitly before removal", path)
		}
	}
	if result.Path == "" {
		return result, fmt.Errorf("no registered worktree for branch %q; inspect git worktree list and git branch --list before choosing a cleanup action", branch)
	}
	path, err := filepath.EvalSymlinks(result.Path)
	if err != nil {
		return result, fmt.Errorf("cannot access registered worktree %s: %w", result.Path, err)
	}
	main, err := filepath.EvalSymlinks(r.main)
	if err != nil {
		return result, err
	}
	if path == main {
		return result, fmt.Errorf("refusing to remove the main worktree: %s", path)
	}
	cwd, err := filepath.EvalSymlinks(r.dir)
	if err != nil {
		return result, err
	}
	if cwd == path || strings.HasPrefix(cwd, path+string(filepath.Separator)) {
		return result, fmt.Errorf("refusing to remove the current worktree; run gwt remove from another checkout")
	}
	status, err := git(ctx, path, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return result, err
	}
	if status != "" {
		return result, fmt.Errorf("worktree has uncommitted or untracked changes: %s; commit or move them before removal", path)
	}
	if !force {
		if _, err := git(ctx, r.dir, "rev-parse", "--verify", "--end-of-options", r.config.Base+"^{commit}"); err != nil {
			return result, err
		}
		if _, err := git(ctx, r.dir, "merge-base", "--is-ancestor", "refs/heads/"+branch, r.config.Base); err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 1 {
				return result, fmt.Errorf("branch %q is not an ancestor of %s (squash merges also fail this check); use --force only to discard the branch's commits", branch, r.config.Base)
			}
			return result, err
		}
	}
	// Git rechecks dirt, locking, and registration immediately before removal.
	if _, err := git(ctx, r.dir, "worktree", "remove", "--", result.Path); err != nil {
		return result, err
	}
	result.WorktreeRemoved = true
	flag := "-d"
	if force {
		flag = "-D"
	}
	if _, err := git(ctx, r.dir, "branch", flag, "--", branch); err != nil {
		return result, fmt.Errorf("worktree removed, but branch %q remains: %w; inspect git branch -v and resolve the deletion error before deleting the branch directly", branch, err)
	}
	result.BranchDeleted, result.OK = true, true
	// Clean only empty parents under the current configured root. A worktree
	// created under an older root can still be removed without sweeping that root.
	root, err := filepath.EvalSymlinks(r.root)
	if err == nil {
		for parent := filepath.Dir(path); strings.HasPrefix(parent, root+string(filepath.Separator)); parent = filepath.Dir(parent) {
			if err := os.Remove(parent); err != nil {
				break
			}
		}
	}
	return result, nil
}
