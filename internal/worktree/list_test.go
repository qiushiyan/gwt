package worktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListReportsStateAndVerdicts(t *testing.T) {
	f := setup(t)
	landed := f.create(CreateOptions{Branch: "landed"})
	write(t, filepath.Join(landed.Path, "landed"), "x", 0644)
	f.mustGit(landed.Path, "add", "landed")
	f.mustGit(landed.Path, "commit", "-qm", "landed work")
	f.mustGit(f.dir, "merge", "--squash", "landed")
	f.mustGit(f.dir, "commit", "-qm", "squash landed")
	pending := f.create(CreateOptions{Branch: "pending"})
	f.mustGit(pending.Path, "commit", "--allow-empty", "-qm", "pending work")
	dirty := f.create(CreateOptions{Branch: "dirty"})
	write(t, filepath.Join(dirty.Path, "untracked"), "x", 0644)
	gone := f.create(CreateOptions{Branch: "gone"})
	if err := os.RemoveAll(gone.Path); err != nil {
		t.Fatal(err)
	}
	f.mustGit(f.dir, "worktree", "add", "--detach", "-q", filepath.Join(f.home, "detached"))
	// Opened from a subdirectory of a linked worktree, which is "current".
	sub := filepath.Join(pending.Path, "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	r, err := Open(context.Background(), sub, f.home, &f.log)
	if err != nil {
		t.Fatal(err)
	}
	l, err := r.List(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if l.Trunk.Name != "main" || l.Trunk.Remote != "" || l.Trunk.Stale {
		t.Fatalf("trunk %+v", l.Trunk)
	}
	w := byBranch(t, l)
	if len(w) != 6 || !w["main"].Main || w["pending"].Main || l.Worktrees[0].Branch != "main" {
		t.Fatalf("%+v", l.Worktrees)
	}
	if !isTrue(w["landed"].Merged) || w["landed"].Dirty {
		t.Fatalf("squash-merged: %+v", w["landed"])
	}
	if w["pending"].Merged == nil || *w["pending"].Merged || !w["pending"].Current || w["landed"].Current {
		t.Fatalf("pending/current: %+v", w["pending"])
	}
	if !w["dirty"].Dirty || w["main"].Dirty {
		t.Fatalf("dirt: %+v %+v", w["dirty"], w["main"])
	}
	if !w["gone"].Prunable || !isTrue(w["gone"].Merged) || !w["gone"].Removable {
		t.Fatalf("prunable: %+v", w["gone"])
	}
	if !w["landed"].Removable || w["pending"].Removable || w["dirty"].Removable || w["main"].Removable {
		t.Fatalf("removable: %+v", l.Worktrees)
	}
	if d := w["(detached) detached"]; d.Path == "" || d.Merged != nil || d.Head == "" {
		t.Fatalf("detached: %+v", d)
	}
	memo, err := os.ReadFile(filepath.Join(f.r.common, "gwt-merged-v1"))
	if err != nil || !strings.Contains(string(memo), w["landed"].Head+" "+l.Trunk.Commit+" 1\n") {
		t.Fatalf("memo %q %v", memo, err)
	}
}
