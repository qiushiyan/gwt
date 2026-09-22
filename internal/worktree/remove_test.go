package worktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveCheckoutAndBranch(t *testing.T) {
	f := setup(t)
	write(t, filepath.Join(f.dir, ".gitignore"), ".env\n", 0644)
	f.mustGit(f.dir, "add", ".gitignore")
	f.mustGit(f.dir, "commit", "-qm", "ignore env")
	write(t, filepath.Join(f.dir, ".env"), "prerequisite", 0600)
	x := f.create(Options{Branch: "done/nested"})
	result, err := f.r.Remove(context.Background(), "done/nested", false)
	if err != nil || !result.OK || !result.WorktreeRemoved || !result.BranchDeleted {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err := os.Stat(x.Path); !os.IsNotExist(err) {
		t.Fatal("checkout remains")
	}
	if _, err := git(context.Background(), f.dir, "show-ref", "--verify", "refs/heads/done/nested"); err == nil {
		t.Fatal("branch remains")
	}
	if _, err := os.Stat(filepath.Dir(x.Path)); !os.IsNotExist(err) {
		t.Fatal("empty parent remains")
	}
	if _, err := f.r.Remove(context.Background(), "done/nested", false); err == nil {
		t.Fatal("missing tree reported success")
	}
}

func TestRemoveUnmergedRequiresForce(t *testing.T) {
	f := setup(t)
	x := f.create(Options{Branch: "unmerged"})
	f.mustGit(x.Path, "commit", "--allow-empty", "-qm", "unmerged work")
	result, err := f.r.Remove(context.Background(), "unmerged", false)
	if err == nil || result.WorktreeRemoved || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err := os.Stat(x.Path); err != nil {
		t.Fatal("preflight removed checkout")
	}
	result, err = f.r.Remove(context.Background(), "unmerged", true)
	if err != nil || !result.OK {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestRemoveProtectsWorktrees(t *testing.T) {
	for _, kind := range []string{"main", "current", "dirty", "untracked", "locked"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			x := f.create(Options{Branch: "protected"})
			branch := "protected"
			switch kind {
			case "main":
				branch = "main"
			case "current":
				subdir := filepath.Join(x.Path, "subdir")
				if err := os.Mkdir(subdir, 0755); err != nil {
					t.Fatal(err)
				}
				r, err := Open(context.Background(), subdir, f.home, &f.log)
				if err != nil {
					t.Fatal(err)
				}
				f.r = r
			case "dirty":
				write(t, filepath.Join(x.Path, "tracked"), "before", 0644)
				f.mustGit(x.Path, "add", "tracked")
				f.mustGit(x.Path, "commit", "-qm", "track file")
				write(t, filepath.Join(x.Path, "tracked"), "after", 0644)
			case "untracked":
				write(t, filepath.Join(x.Path, "precious"), "keep", 0644)
			case "locked":
				f.mustGit(f.dir, "worktree", "lock", x.Path)
			}
			result, err := f.r.Remove(context.Background(), branch, true)
			if err == nil || result.WorktreeRemoved || result.BranchDeleted {
				t.Fatalf("%+v %v", result, err)
			}
			if _, err := os.Stat(x.Path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRemoveUsesRegistrationAfterRootChange(t *testing.T) {
	f := setup(t)
	x := f.create(Options{Branch: "old-root"})
	f.configure(`worktree_root = "~/new-root"`)
	result, err := f.r.Remove(context.Background(), "old-root", false)
	if err != nil || !result.OK || result.Path == filepath.Join(f.r.root, "old-root") {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err := os.Stat(x.Path); !os.IsNotExist(err) {
		t.Fatal("old checkout remains")
	}
}

func TestRemoveReportsPartialFailure(t *testing.T) {
	f := setup(t)
	f.create(Options{Branch: "locked-ref"})
	// Worktree removal can succeed while branch deletion fails on a ref lock.
	write(t, filepath.Join(f.r.common, "refs/heads/locked-ref.lock"), "busy", 0644)
	result, err := f.r.Remove(context.Background(), "locked-ref", false)
	if err == nil || result.OK || !result.WorktreeRemoved || result.BranchDeleted {
		t.Fatalf("%+v %v", result, err)
	}
	if !strings.Contains(err.Error(), "worktree removed") || !strings.Contains(err.Error(), "inspect git branch -v") {
		t.Fatal(err)
	}
}
