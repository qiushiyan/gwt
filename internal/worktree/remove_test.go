package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func remove1(r *Repo, target string, o RemoveOptions) (Removal, error) {
	x := r.Remove(context.Background(), []string{target}, o)[0]
	if !x.OK {
		return x, errors.New(x.Error)
	}
	return x, nil
}

func (f *fixture) exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func (f *fixture) hasRef(ref string) bool {
	_, err := git(context.Background(), f.dir, "show-ref", "--verify", "--quiet", ref)
	return err == nil
}

func TestRemoveCheckoutAndBranch(t *testing.T) {
	f := setup(t)
	write(t, filepath.Join(f.dir, ".gitignore"), ".env\n", 0644)
	f.mustGit(f.dir, "add", ".gitignore")
	f.mustGit(f.dir, "commit", "-qm", "ignore env")
	write(t, filepath.Join(f.dir, ".env"), "prerequisite", 0600)
	x := f.create(Options{Branch: "done/nested"})
	l, err := f.r.List(context.Background(), false)
	if err != nil || !byBranch(t, l)["done/nested"].Removable || byBranch(t, l)["main"].Removable {
		t.Fatalf("removable: %+v %v", l.Worktrees, err)
	}
	result, err := remove1(f.r, "done/nested", RemoveOptions{})
	if err != nil || !result.WorktreeRemoved || !result.BranchDeleted || result.RecoveryRef != "" {
		t.Fatalf("%+v %v", result, err)
	}
	if f.exists(x.Path) || f.hasRef("refs/heads/done/nested") || strings.Contains(f.mustGit(f.dir, "worktree", "list"), x.Path) {
		t.Fatal("checkout, branch or registration remains")
	}
	if f.exists(filepath.Dir(x.Path)) {
		t.Fatal("empty parent remains")
	}
	if _, err := remove1(f.r, "done/nested", RemoveOptions{}); err == nil {
		t.Fatal("missing tree reported success")
	}
}

// --force deletes unmerged work only with its tip kept as a recovery ref.
func TestRemoveUnmergedRequiresForce(t *testing.T) {
	f := setup(t)
	x := f.create(Options{Branch: "unmerged"})
	f.mustGit(x.Path, "commit", "--allow-empty", "-qm", "unmerged work")
	tip := f.mustGit(x.Path, "rev-parse", "HEAD")
	result, err := remove1(f.r, "unmerged", RemoveOptions{})
	if err == nil || result.WorktreeRemoved || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("%+v %v", result, err)
	}
	if !f.exists(x.Path) {
		t.Fatal("preflight removed checkout")
	}
	result, err = remove1(f.r, "unmerged", RemoveOptions{Force: true})
	if err != nil || !result.BranchDeleted || result.RecoveryRef == "" {
		t.Fatalf("%+v %v", result, err)
	}
	if got := f.mustGit(f.dir, "rev-parse", result.RecoveryRef); got != tip {
		t.Fatalf("recovery ref %s is %s, want the tip %s", result.RecoveryRef, got, tip)
	}
}

func TestRemoveBranchWithoutCheckout(t *testing.T) {
	f := setup(t)
	f.mustGit(f.dir, "branch", "landed")
	f.mustGit(f.dir, "branch", "ahead")
	f.mustGit(f.dir, "update-ref", "refs/heads/ahead", f.mustGit(f.dir, "commit-tree", "-p", "HEAD", "-m", "ahead", f.mustGit(f.dir, "rev-parse", "HEAD^{tree}")))
	tip := f.mustGit(f.dir, "rev-parse", "ahead")
	if _, err := remove1(f.r, "landed", RemoveOptions{KeepBranch: true}); err == nil || !strings.Contains(err.Error(), "nothing to remove") {
		t.Fatal(err)
	}
	result, err := remove1(f.r, "landed", RemoveOptions{})
	if err != nil || result.WorktreeRemoved || !result.BranchDeleted || result.RecoveryRef != "" || f.hasRef("refs/heads/landed") {
		t.Fatalf("merged branch: %+v %v", result, err)
	}
	if _, err := remove1(f.r, "ahead", RemoveOptions{}); err == nil || !f.hasRef("refs/heads/ahead") {
		t.Fatalf("unmerged branch deleted: %v", err)
	}
	result, err = remove1(f.r, "ahead", RemoveOptions{Force: true})
	if err != nil || f.hasRef("refs/heads/ahead") || f.mustGit(f.dir, "rev-parse", result.RecoveryRef) != tip {
		t.Fatalf("forced branch: %+v %v", result, err)
	}
}

func TestRemoveKeepBranch(t *testing.T) {
	f := setup(t)
	x := f.create(Options{Branch: "kept"})
	f.mustGit(x.Path, "commit", "--allow-empty", "-qm", "unmerged work")
	result, err := remove1(f.r, x.Path, RemoveOptions{KeepBranch: true})
	if err != nil || !result.WorktreeRemoved || result.BranchDeleted || result.Branch != "kept" || result.RecoveryRef != "" {
		t.Fatalf("%+v %v", result, err)
	}
	if f.exists(x.Path) || !f.hasRef("refs/heads/kept") {
		t.Fatal("checkout remains or branch gone")
	}
}

// A detached checkout's commits may be referenced nowhere else.
func TestRemoveDetachedCheckoutKeepsHead(t *testing.T) {
	f := setup(t)
	path := filepath.Join(f.home, "detached")
	f.mustGit(f.dir, "worktree", "add", "-q", "--detach", path)
	f.mustGit(path, "commit", "--allow-empty", "-qm", "detached work")
	head := f.mustGit(path, "rev-parse", "HEAD")
	result, err := remove1(f.r, path, RemoveOptions{})
	if err != nil || !result.WorktreeRemoved || f.exists(path) || f.mustGit(f.dir, "rev-parse", result.RecoveryRef) != head {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err := remove1(f.r, filepath.Join(f.home, "never-registered"), RemoveOptions{}); err == nil {
		t.Fatal("unregistered path removed")
	}
}

// brief offers --force when the local tip equals a merged PR's head; the tip
// can move before an agent runs the command.
func TestRemoveExpectHead(t *testing.T) {
	f := setup(t)
	x := f.create(Options{Branch: "pr"})
	f.mustGit(x.Path, "commit", "--allow-empty", "-qm", "reviewed")
	reviewed := f.mustGit(x.Path, "rev-parse", "HEAD")
	f.mustGit(x.Path, "commit", "--allow-empty", "-qm", "after review")
	if _, err := remove1(f.r, "pr", RemoveOptions{Force: true, ExpectHead: reviewed}); err == nil || !f.exists(x.Path) || !f.hasRef("refs/heads/pr") {
		t.Fatalf("moved tip removed: %v", err)
	}
	if _, err := remove1(f.r, "pr", RemoveOptions{Force: true, ExpectHead: f.mustGit(x.Path, "rev-parse", "HEAD")}); err != nil {
		t.Fatal(err)
	}
}

// Every refusal here is also list's: removable false.
func TestRemoveProtectsWorktrees(t *testing.T) {
	for kind, reason := range map[string]string{
		"main": "main worktree", "current": "current worktree", "locked": "locked",
		"dirty": "changes", "untracked": "changes", "assume-unchanged": "changes",
		"skip-worktree": "changes", "nesting": "contains another worktree",
	} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			x := f.create(Options{Branch: "protected"})
			branch := "protected"
			write(t, filepath.Join(x.Path, "tracked"), "before", 0644)
			f.mustGit(x.Path, "add", "tracked")
			f.mustGit(x.Path, "commit", "-qm", "track file")
			f.mustGit(f.dir, "merge", "-q", "--ff-only", "protected")
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
				write(t, filepath.Join(x.Path, "tracked"), "after", 0644)
			case "untracked":
				write(t, filepath.Join(x.Path, "precious"), "keep", 0644)
			case "assume-unchanged", "skip-worktree":
				f.mustGit(x.Path, "update-index", "--"+kind, "tracked")
				write(t, filepath.Join(x.Path, "tracked"), "hidden edit", 0644)
				if f.mustGit(x.Path, "status", "--porcelain") != "" {
					t.Fatal("status sees the edit; the case tests nothing")
				}
			case "locked":
				f.mustGit(f.dir, "worktree", "lock", x.Path)
			case "nesting":
				// Ignored, so only the nesting can refuse it.
				write(t, filepath.Join(f.r.common, "info", "exclude"), "inner/\n", 0644)
				f.mustGit(f.dir, "worktree", "add", "-q", "-b", "inner", filepath.Join(x.Path, "inner"))
			}
			l, err := f.r.List(context.Background(), false)
			if err != nil || byBranch(t, l)[branch].Removable {
				t.Fatalf("list calls it removable: %+v %v", byBranch(t, l)[branch], err)
			}
			result, err := remove1(f.r, branch, RemoveOptions{Force: true})
			if err == nil || !strings.Contains(err.Error(), reason) || result.WorktreeRemoved || result.BranchDeleted {
				t.Fatalf("%+v %v", result, err)
			}
			if !f.exists(filepath.Join(x.Path, "tracked")) {
				t.Fatal("checkout touched")
			}
		})
	}
}

// A status that cannot be read is unknown state, never clean: list does not
// call it merged or removable, and removal keeps it even with --discard-dirty
// when it cannot be snapshotted.
func TestUnreadableStatusIsNeverClean(t *testing.T) {
	f := setup(t)
	x := f.create(Options{Branch: "broken"})
	write(t, filepath.Join(x.Path, "precious"), "only copy", 0644)
	gitfile := filepath.Join(x.Path, ".git")
	if err := os.Chmod(gitfile, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(gitfile, 0644) })
	l, err := f.r.List(context.Background(), false)
	if w := byBranch(t, l)["broken"]; err != nil || !w.Dirty || w.Merged != nil || w.Removable || w.Error == "" {
		t.Fatalf("%+v %v", w, err)
	}
	for _, o := range []RemoveOptions{{}, {DiscardDirty: true}} {
		if _, err := remove1(f.r, "broken", o); err == nil || !f.exists(filepath.Join(x.Path, "precious")) {
			t.Fatalf("%+v: removed unknown state: %v", o, err)
		}
	}
}

// --discard-dirty: the snapshot holds tracked edits and untracked files but
// not ignored ones, and is parented on HEAD, which it therefore keeps too.
func TestRemoveDiscardDirtySnapshots(t *testing.T) {
	f := setup(t)
	x := f.create(Options{Branch: "snapme"})
	write(t, filepath.Join(x.Path, ".gitignore"), "ignore-me\n", 0644)
	write(t, filepath.Join(x.Path, "tracked.txt"), "base\n", 0644)
	f.mustGit(x.Path, "add", ".")
	f.mustGit(x.Path, "commit", "-qm", "base")
	tip := f.mustGit(x.Path, "rev-parse", "HEAD")
	write(t, filepath.Join(x.Path, "tracked.txt"), "base\ntracked-edit\n", 0644)
	write(t, filepath.Join(x.Path, "untracked.txt"), "brand new\n", 0644)
	write(t, filepath.Join(x.Path, "ignore-me"), "ignored\n", 0644)
	result, err := remove1(f.r, "snapme", RemoveOptions{DiscardDirty: true, KeepBranch: true})
	if err != nil || !result.WorktreeRemoved || result.RecoveryRef == "" || f.exists(x.Path) {
		t.Fatalf("%+v %v", result, err)
	}
	ref := result.RecoveryRef
	if got := f.mustGit(f.dir, "show", ref+":untracked.txt"); got != "brand new" {
		t.Fatalf("untracked file: %q", got)
	}
	if got := f.mustGit(f.dir, "show", ref+":tracked.txt"); !strings.Contains(got, "tracked-edit") {
		t.Fatalf("tracked edit: %q", got)
	}
	if _, err := git(context.Background(), f.dir, "cat-file", "-e", ref+":ignore-me"); err == nil {
		t.Fatal("ignored file in snapshot")
	}
	if got := f.mustGit(f.dir, "rev-parse", ref+"^"); got != tip {
		t.Fatalf("snapshot parent %s, want HEAD %s", got, tip)
	}
}

// The snapshot is built in a scratch index: staging the user's files as a
// side effect would change the state being preserved.
func TestSnapshotLeavesCheckoutIndexAlone(t *testing.T) {
	f := setup(t)
	x := f.create(Options{Branch: "index"})
	write(t, filepath.Join(x.Path, "untracked.txt"), "new", 0644)
	if _, err := snapshot(context.Background(), x.Path, "test"); err != nil {
		t.Fatal(err)
	}
	if got := f.mustGit(x.Path, "status", "--porcelain"); got != "?? untracked.txt" {
		t.Fatalf("status after snapshot: %q", got)
	}
	if f.hasRef("refs/stash") {
		t.Fatal("snapshot used the stash")
	}
}

// One batch, distinct refs: feat and feat/x flatten alike, and refs cannot
// hold both .../feat and .../feat/x.
func TestRemoveBatchKeepsRefsApart(t *testing.T) {
	f := setup(t)
	var paths []string
	for _, b := range []string{"feat", "feat-x"} {
		x := f.create(Options{Branch: b})
		write(t, filepath.Join(x.Path, "wip"), b, 0644)
		paths = append(paths, x.Path)
	}
	results := f.r.Remove(context.Background(), paths, RemoveOptions{DiscardDirty: true, KeepBranch: true})
	a, b := results[0].RecoveryRef, results[1].RecoveryRef
	if !results[0].OK || !results[1].OK || a == b || filepath.Dir(a) != filepath.Dir(b) {
		t.Fatalf("%+v", results)
	}
	if f.mustGit(f.dir, "show", a+":wip") != "feat" || f.mustGit(f.dir, "show", b+":wip") != "feat-x" {
		t.Fatal("a snapshot overwrote the other")
	}
}

// Recovery refs pin objects forever unless they expire; 0 keeps them.
func TestRecoveryRefsExpire(t *testing.T) {
	f := setup(t)
	head := f.mustGit(f.dir, "rev-parse", "HEAD")
	now := time.Now()
	old := recoveryNS + "/1700000000.1/001-old"
	recent := fmt.Sprintf("%s/%d.1/001-recent", recoveryNS, now.Unix())
	f.mustGit(f.dir, "update-ref", old, head)
	f.mustGit(f.dir, "update-ref", recent, head)
	f.r.expireRecovery(context.Background(), now)
	if f.hasRef(old) || !f.hasRef(recent) {
		t.Fatal("30-day default: old ref kept or recent ref dropped")
	}
	f.configure(`recovery.keep = "0"`)
	f.mustGit(f.dir, "update-ref", old, head)
	f.r.expireRecovery(context.Background(), now)
	if !f.hasRef(old) {
		t.Fatal("keep = 0 expired a ref")
	}
}

// The checkout leaves at once; a detached process deletes it with any batch a
// killed run left, and spares a batch another run may still be sweeping.
func TestRemoveSweepsTrash(t *testing.T) {
	f := setup(t)
	trash := filepath.Join(f.home, "dev", ".worktrees", ".trash")
	abandoned, live := filepath.Join(trash, "1.1"), filepath.Join(trash, "2.2")
	for _, d := range []string{abandoned, live} {
		write(t, filepath.Join(d, "1", "file"), "x", 0644)
	}
	hour := time.Now().Add(-time.Hour)
	os.Chtimes(abandoned, hour, hour)
	x := f.create(Options{Branch: "swept"})
	if _, err := remove1(f.r, "swept", RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, _ := os.ReadDir(trash)
		if len(entries) == 1 && entries[0].Name() == "2.2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("trash after sweep: %v (checkout %s)", entries, x.Path)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRemoveUsesRegistrationAfterRootChange(t *testing.T) {
	f := setup(t)
	x := f.create(Options{Branch: "old-root"})
	f.configure(`worktree_root = "~/new-root"`)
	result, err := remove1(f.r, "old-root", RemoveOptions{})
	if err != nil || result.Path == filepath.Join(f.r.root, "old-root") {
		t.Fatalf("%+v %v", result, err)
	}
	if f.exists(x.Path) {
		t.Fatal("old checkout remains")
	}
}

func TestRemoveReportsPartialFailure(t *testing.T) {
	f := setup(t)
	f.create(Options{Branch: "locked-ref"})
	// Worktree removal can succeed while branch deletion fails on a ref lock.
	write(t, filepath.Join(f.r.common, "refs/heads/locked-ref.lock"), "busy", 0644)
	result, err := remove1(f.r, "locked-ref", RemoveOptions{})
	if err == nil || result.OK || !result.WorktreeRemoved || result.BranchDeleted {
		t.Fatalf("%+v %v", result, err)
	}
	if !strings.Contains(err.Error(), "inspect git branch -v") {
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
			result, err := remove1(f.r, "feature", RemoveOptions{})
			if style == "unmerged" {
				if err == nil || result.WorktreeRemoved || result.BranchDeleted {
					t.Fatalf("unmerged content removed: %+v %v", result, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%+v %v", result, err)
			}
		})
	}
}

// The verdict is measured against the trunk, whichever checkout calls remove.
func TestRemoveVerdictIgnoresCaller(t *testing.T) {
	f := setup(t)
	caller := f.create(Options{Branch: "older-topic"})
	target := f.create(Options{Branch: "merged"})
	f.mustGit(target.Path, "commit", "--allow-empty", "-qm", "merged commit")
	f.mustGit(f.dir, "merge", "--ff-only", "merged")
	r, err := Open(context.Background(), caller.Path, f.home, &f.log)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := remove1(r, "merged", RemoveOptions{}); err != nil {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestRemovePrunableRegistration(t *testing.T) {
	f := setup(t)
	x := f.create(Options{Branch: "missing"})
	if err := os.RemoveAll(x.Path); err != nil {
		t.Fatal(err)
	}
	result, err := remove1(f.r, "missing", RemoveOptions{})
	if err != nil || !result.WorktreeRemoved || !result.BranchDeleted {
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
	result, err := remove1(f.r, "feature", RemoveOptions{})
	if err == nil || result.WorktreeRemoved || result.BranchDeleted {
		t.Fatalf("merge-only work discarded: %+v %v", result, err)
	}
}

func TestRemovePreservesWhitespaceChangeAfterSquash(t *testing.T) {
	f := setup(t)
	x := f.create(Options{Branch: "feature"})
	write(t, filepath.Join(x.Path, "script.py"), "if True:\n    print('one')\n    print('two')\n", 0644)
	f.mustGit(x.Path, "add", "script.py")
	f.mustGit(x.Path, "commit", "-qm", "feature")
	f.mustGit(f.dir, "merge", "--squash", "feature")
	f.mustGit(f.dir, "commit", "-qm", "squash feature")
	write(t, filepath.Join(x.Path, "script.py"), "if True:\n    print('one')\nprint('two')\n", 0644)
	f.mustGit(x.Path, "commit", "-qam", "change indentation")
	result, err := remove1(f.r, "feature", RemoveOptions{})
	if err == nil || result.WorktreeRemoved || result.BranchDeleted {
		t.Fatalf("unmerged indentation discarded: %+v %v", result, err)
	}
}
