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

func TestRemoveRecognizesMergeStyles(t *testing.T) {
	for _, style := range []string{"merge", "squash", "rebase", "unmerged"} {
		t.Run(style, func(t *testing.T) {
			f := setup(t)
			x := f.create(Options{Branch: "feature"})
			write(t, filepath.Join(x.Path, "feature"), "one\n", 0644)
			f.mustGit(x.Path, "add", "feature")
			f.mustGit(x.Path, "commit", "-qm", "one")
			first := f.mustGit(x.Path, "rev-parse", "HEAD")
			write(t, filepath.Join(x.Path, "feature"), "one\ntwo\n", 0644)
			f.mustGit(x.Path, "commit", "-qam", "two")
			second := f.mustGit(x.Path, "rev-parse", "HEAD")
			write(t, filepath.Join(f.dir, "unrelated"), "main advanced", 0644)
			f.mustGit(f.dir, "add", "unrelated")
			f.mustGit(f.dir, "commit", "-qm", "advance main")
			switch style {
			case "merge":
				f.mustGit(f.dir, "merge", "--no-ff", "-qm", "merge feature", "feature")
			case "squash":
				f.mustGit(f.dir, "merge", "--squash", "feature")
				f.mustGit(f.dir, "commit", "-qm", "squash feature")
			case "rebase":
				f.mustGit(f.dir, "cherry-pick", first, second)
			}
			result, err := f.r.Remove(context.Background(), "feature", false)
			if style == "unmerged" {
				if err == nil || result.WorktreeRemoved || result.BranchDeleted {
					t.Fatalf("unmerged content removed: %+v %v", result, err)
				}
				return
			}
			if err != nil || !result.OK {
				t.Fatalf("%+v %v", result, err)
			}
		})
	}
}

func TestRemoveUsesMainCheckoutBase(t *testing.T) {
	f := setup(t)
	caller := f.create(Options{Branch: "older-topic"})
	target := f.create(Options{Branch: "merged"})
	f.mustGit(target.Path, "commit", "--allow-empty", "-qm", "merged commit")
	f.mustGit(f.dir, "merge", "--ff-only", "merged")
	r, err := Open(context.Background(), caller.Path, f.home, &f.log)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Remove(context.Background(), "merged", false)
	if err != nil || !result.OK {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestRemovePrunableRegistration(t *testing.T) {
	f := setup(t)
	x := f.create(Options{Branch: "missing"})
	if err := os.RemoveAll(x.Path); err != nil {
		t.Fatal(err)
	}
	result, err := f.r.Remove(context.Background(), "missing", false)
	if err != nil || !result.OK || !result.WorktreeRemoved || !result.BranchDeleted {
		t.Fatalf("%+v %v", result, err)
	}
	if strings.Contains(f.mustGit(f.dir, "worktree", "list", "--porcelain"), "refs/heads/missing") {
		t.Fatal("registration remains")
	}
}

func TestRemovePreservesChangesIntroducedByMergeCommit(t *testing.T) {
	f := setup(t)
	feature := f.create(Options{Branch: "feature"})
	side := f.create(Options{Branch: "side"})
	write(t, filepath.Join(feature.Path, "feature"), "feature", 0644)
	f.mustGit(feature.Path, "add", "feature")
	f.mustGit(feature.Path, "commit", "-qm", "feature change")
	featureTip := f.mustGit(feature.Path, "rev-parse", "HEAD")
	write(t, filepath.Join(side.Path, "side"), "side", 0644)
	f.mustGit(side.Path, "add", "side")
	f.mustGit(side.Path, "commit", "-qm", "side change")
	sideTip := f.mustGit(side.Path, "rev-parse", "HEAD")
	f.mustGit(feature.Path, "merge", "--no-ff", "--no-commit", "side")
	write(t, filepath.Join(feature.Path, "merge-only"), "keep this merge edit", 0644)
	f.mustGit(feature.Path, "add", "merge-only")
	f.mustGit(feature.Path, "commit", "-qm", "merge with additional edits")
	// All non-merge commits are replayed; the merge commit's own edit is not.
	f.mustGit(f.dir, "cherry-pick", featureTip, sideTip)
	result, err := f.r.Remove(context.Background(), "feature", false)
	if err == nil || result.WorktreeRemoved || result.BranchDeleted {
		t.Fatalf("merge-only work discarded: %+v %v", result, err)
	}
}
