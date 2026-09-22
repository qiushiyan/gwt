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
	missing := os.IsNotExist(err)
	if missing {
		path = filepath.Clean(result.Path)
	} else if err != nil {
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
	if !missing {
		status, err := git(ctx, path, "status", "--porcelain", "--untracked-files=all")
		if err != nil {
			return result, err
		}
		if status != "" {
			return result, fmt.Errorf("worktree has uncommitted or untracked changes: %s; commit or move them before removal", path)
		}
	}
	if !force {
		// HEAD as a creation base is caller-relative; cleanup measures integration
		// against the main checkout so changing the caller cannot change the verdict.
		base, err := git(ctx, r.main, "rev-parse", "--verify", "--end-of-options", r.config.Base+"^{commit}")
		if err != nil {
			return result, err
		}
		tip, err := git(ctx, r.dir, "rev-parse", "--verify", "refs/heads/"+branch)
		if err != nil {
			return result, err
		}
		merged, err := r.mergedInto(ctx, tip, base)
		if err != nil {
			return result, err
		}
		if !merged {
			name, err := git(ctx, r.main, "rev-parse", "--abbrev-ref", r.config.Base)
			if err != nil {
				return result, err
			}
			return result, fmt.Errorf("branch %q has work not confirmed in %s (%s); inspect git log --oneline %s..%s and the branch diff before using --force to discard it", branch, name, base[:12], base, tip)
		}
	}
	// Git rechecks dirt, locking, and registration immediately before removal.
	if _, err := git(ctx, r.dir, "worktree", "remove", "--", result.Path); err != nil {
		return result, err
	}
	result.WorktreeRemoved = true
	// Patch-equivalent squash/rebase merges need -D: Git branch -d only checks
	// ancestry. The integration check above owns this decision.
	if _, err := git(ctx, r.dir, "branch", "-D", "--", branch); err != nil {
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

// mergedInto recognizes ancestry, squash merges, and replayed commits, in that
// order. Empty net changes do not prove integration of unmerged commits.
func (r *Repo) mergedInto(ctx context.Context, branch, base string) (bool, error) {
	if _, err := git(ctx, r.dir, "merge-base", "--is-ancestor", branch, base); err == nil {
		return true, nil
	} else {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return false, err
		}
	}
	ancestor, err := git(ctx, r.dir, "merge-base", base, branch)
	if err != nil {
		return false, err
	}
	tree, err := git(ctx, r.dir, "rev-parse", branch+"^{tree}")
	if err != nil {
		return false, err
	}
	ancestorTree, err := git(ctx, r.dir, "rev-parse", ancestor+"^{tree}")
	if err != nil || tree == ancestorTree {
		return false, err
	}
	// This temporary object gives git cherry the branch's combined patch without
	// changing any ref. It needs neither the user's identity nor commit signing.
	squash, err := git(ctx, r.dir, "-c", "user.name=gwt", "-c", "user.email=gwt@localhost", "-c", "commit.gpgSign=false", "commit-tree", tree, "-p", ancestor, "-m", "gwt integration check")
	if err != nil {
		return false, err
	}
	out, err := git(ctx, r.dir, "cherry", base, squash)
	if err != nil {
		return false, err
	}
	if strings.HasPrefix(out, "- ") {
		return true, nil
	}
	// git cherry omits merge commits. Matching their parents' patches cannot
	// prove that edits made in the merge itself reached the base.
	merges, err := git(ctx, r.dir, "rev-list", "--merges", base+".."+branch)
	if err != nil || merges != "" {
		return false, err
	}
	out, err = git(ctx, r.dir, "cherry", base, branch)
	if err != nil || out == "" {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "- ") {
			return false, nil
		}
	}
	return true, nil
}
