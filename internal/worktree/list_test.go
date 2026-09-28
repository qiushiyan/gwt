package worktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func byBranch(t *testing.T, l Listing) map[string]Worktree {
	t.Helper()
	m := map[string]Worktree{}
	for _, w := range l.Worktrees {
		key := w.Branch
		if key == "" {
			key = "(detached) " + filepath.Base(w.Path)
		}
		m[key] = w
	}
	return m
}

func isTrue(b *bool) bool { return b != nil && *b }

func TestListReportsStateAndVerdicts(t *testing.T) {
	f := setup(t)
	landed := f.create(Options{Branch: "landed"})
	write(t, filepath.Join(landed.Path, "landed"), "x", 0644)
	f.mustGit(landed.Path, "add", "landed")
	f.mustGit(landed.Path, "commit", "-qm", "landed work")
	f.mustGit(f.dir, "merge", "--squash", "landed")
	f.mustGit(f.dir, "commit", "-qm", "squash landed")
	pending := f.create(Options{Branch: "pending"})
	f.mustGit(pending.Path, "commit", "--allow-empty", "-qm", "pending work")
	dirty := f.create(Options{Branch: "dirty"})
	write(t, filepath.Join(dirty.Path, "untracked"), "x", 0644)
	gone := f.create(Options{Branch: "gone"})
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
	if !w["gone"].Prunable || w["gone"].Merged != nil {
		t.Fatalf("prunable: %+v", w["gone"])
	}
	if d := w["(detached) detached"]; d.Path == "" || d.Merged != nil || d.Head == "" {
		t.Fatalf("detached: %+v", d)
	}
	memo, err := os.ReadFile(filepath.Join(f.r.common, "gwt-merged-v1"))
	if err != nil || !strings.Contains(string(memo), w["landed"].Head+" "+l.Trunk.Commit+" 1\n") {
		t.Fatalf("memo %q %v", memo, err)
	}
}

func TestMergedJudgesBranchesWithoutCheckout(t *testing.T) {
	f := setup(t)
	f.mustGit(f.dir, "branch", "at-trunk")
	head := f.mustGit(f.dir, "rev-parse", "HEAD")
	write(t, filepath.Join(f.dir, "ahead"), "x", 0644)
	f.mustGit(f.dir, "add", "ahead")
	ahead := f.mustGit(f.dir, "write-tree")
	f.mustGit(f.dir, "reset", "-q")
	os.Remove(filepath.Join(f.dir, "ahead"))
	commit := f.mustGit(f.dir, "commit-tree", ahead, "-p", head, "-m", "ahead")
	f.mustGit(f.dir, "branch", "ahead", commit)
	v, err := f.r.Merged(context.Background(), []string{"at-trunk", "ahead", "missing"}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	b := v.Branches
	if len(b) != 3 || !isTrue(b[0].Merged) || b[1].Merged == nil || *b[1].Merged || b[2].Merged != nil || b[2].Error == "" {
		t.Fatalf("%+v", b)
	}
}

// remoteFixture gives the repository an origin whose HEAD is develop, and a
// second clone standing in for GitHub, where merges land without our fetch.
func remoteFixture(t *testing.T, f *fixture) (origin, upstream string) {
	t.Helper()
	origin = filepath.Join(f.home, "origin.git")
	f.mustGit(f.home, "init", "-q", "--bare", "-b", "main", origin)
	f.mustGit(f.dir, "remote", "add", "origin", origin)
	f.mustGit(f.dir, "branch", "develop")
	f.mustGit(f.dir, "push", "-q", "origin", "main", "develop")
	f.mustGit(origin, "symbolic-ref", "HEAD", "refs/heads/develop")
	f.mustGit(f.dir, "remote", "set-head", "origin", "develop")
	upstream = filepath.Join(f.home, "github")
	f.mustGit(f.home, "clone", "-q", "-b", "develop", origin, upstream)
	f.mustGit(upstream, "config", "user.name", "GitHub")
	f.mustGit(upstream, "config", "user.email", "github@example.invalid")
	return origin, upstream
}

func TestTrunkIsRemoteDefaultBranch(t *testing.T) {
	f := setup(t)
	remoteFixture(t, f)
	// A local branch named like the remote ref must not shadow it.
	f.mustGit(f.dir, "branch", "origin/develop")
	tr, err := f.r.Trunk(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	want := f.mustGit(f.dir, "rev-parse", "refs/remotes/origin/develop")
	if tr.Name != "origin/develop" || tr.Remote != "origin" || tr.Commit != want || !tr.Stale {
		t.Fatalf("%+v", tr)
	}
}

// A PR squash-merged on the remote after our last fetch: list without --fetch
// grades against the cached trunk; refresh and remove fetch it first.
func TestStaleTrunkIsRefreshed(t *testing.T) {
	f := setup(t)
	origin, upstream := remoteFixture(t, f)
	x := f.create(Options{Branch: "feature", Base: "origin/develop", NoFetch: true})
	write(t, filepath.Join(x.Path, "feature"), "shipped\n", 0644)
	f.mustGit(x.Path, "add", "feature")
	f.mustGit(x.Path, "commit", "-qm", "feature")
	write(t, filepath.Join(upstream, "feature"), "shipped\n", 0644)
	f.mustGit(upstream, "add", "feature")
	f.mustGit(upstream, "commit", "-qm", "Feature (#1)")
	f.mustGit(upstream, "push", "-q", "origin", "develop")

	l, err := f.r.List(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if w := byBranch(t, l)["feature"]; w.Merged == nil || *w.Merged || !l.Trunk.Stale {
		t.Fatalf("cached trunk: %+v %+v", l.Trunk, w)
	}

	// An unreachable remote warns, keeps cached refs, and removal refuses.
	f.mustGit(f.dir, "remote", "set-url", "origin", filepath.Join(f.home, "nowhere.git"))
	result, err := f.r.Remove(context.Background(), "feature", false)
	if err == nil || result.WorktreeRemoved || !strings.Contains(err.Error(), "origin/develop") {
		t.Fatalf("%+v %v", result, err)
	}
	if !strings.Contains(f.log.String(), "could not refresh origin") {
		t.Fatalf("no fetch warning: %s", f.log.String())
	}

	f.mustGit(f.dir, "remote", "set-url", "origin", origin)
	l, err = f.r.List(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if w := byBranch(t, l)["feature"]; !isTrue(w.Merged) || !l.Trunk.Fetched || l.Trunk.Stale {
		t.Fatalf("refreshed trunk: %+v %+v", l.Trunk, w)
	}
	result, err = f.r.Remove(context.Background(), "feature", false)
	if err != nil || !result.OK {
		t.Fatalf("%+v %v", result, err)
	}
}

// A branch whose commits cancel out has no patch to match; an empty net diff
// must not read as "already applied".
func TestNoNetChangeIsNotMerged(t *testing.T) {
	f := setup(t)
	x := f.create(Options{Branch: "noop"})
	write(t, filepath.Join(x.Path, "x"), "x", 0644)
	f.mustGit(x.Path, "add", "x")
	f.mustGit(x.Path, "commit", "-qm", "add")
	f.mustGit(x.Path, "rm", "-q", "x")
	f.mustGit(x.Path, "commit", "-qm", "remove")
	v, err := f.r.Merged(context.Background(), []string{"noop"}, false, "")
	if err != nil || v.Branches[0].Merged == nil || *v.Branches[0].Merged {
		t.Fatalf("%+v %v", v, err)
	}
}

// The memo is really read (a poisoned entry is served), and its key includes
// the trunk commit, so a moved trunk retires the entry instead of reusing it.
func TestVerdictMemoKeysOnBothCommits(t *testing.T) {
	f := setup(t)
	x := f.create(Options{Branch: "open"})
	f.mustGit(x.Path, "commit", "--allow-empty", "-qm", "open work")
	tip := f.mustGit(x.Path, "rev-parse", "HEAD")
	trunk := f.mustGit(f.dir, "rev-parse", "HEAD")
	write(t, filepath.Join(f.r.common, "gwt-merged-v1"), tip+" "+trunk+" 1\n", 0644)
	v, err := f.r.Merged(context.Background(), []string{"open"}, false, "")
	if err != nil || !isTrue(v.Branches[0].Merged) {
		t.Fatalf("memo not read: %+v %v", v, err)
	}
	f.mustGit(f.dir, "commit", "--allow-empty", "-qm", "trunk moves on")
	v, err = f.r.Merged(context.Background(), []string{"open"}, false, "")
	if err != nil || v.Branches[0].Merged == nil || *v.Branches[0].Merged {
		t.Fatalf("stale memo reused: %+v %v", v, err)
	}
}

// A fetch killed mid-flight truncates FETCH_HEAD with a fresh mtime; that is
// the least trustworthy state and must read as stale.
func TestFetchFreshness(t *testing.T) {
	f := setup(t)
	remoteFixture(t, f)
	head := filepath.Join(f.r.common, "FETCH_HEAD")
	for _, c := range []struct {
		content string
		age     time.Duration
		stale   bool
	}{{"", 0, true}, {"fetched\n", 0, false}, {"fetched\n", 2 * f.r.config.Fetch.MaxAge, true}} {
		write(t, head, c.content, 0644)
		when := time.Now().Add(-c.age)
		if err := os.Chtimes(head, when, when); err != nil {
			t.Fatal(err)
		}
		tr, err := f.r.Trunk(context.Background(), false)
		if err != nil || tr.Stale != c.stale {
			t.Fatalf("%q aged %v: %+v %v", c.content, c.age, tr, err)
		}
	}
}

// Freshness is repository-wide: a refresh from a linked checkout must count
// for the next call, from that checkout or any other, instead of fetching again.
func TestRefreshFromLinkedCheckoutCounts(t *testing.T) {
	f := setup(t)
	remoteFixture(t, f)
	x := f.create(Options{Branch: "linked", Base: "origin/develop", NoFetch: true})
	r, err := Open(context.Background(), x.Path, f.home, &f.log)
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.Trunk(context.Background(), true)
	if err != nil || !first.Fetched {
		t.Fatalf("first refresh: %+v %v", first, err)
	}
	for _, from := range []*Repo{r, f.r} {
		again, err := from.Trunk(context.Background(), true)
		if err != nil || again.Fetched || again.Stale {
			t.Fatalf("refetched from %s: %+v %v", from.dir, again, err)
		}
	}
}

// A detached checkout's HEAD is judged by commit id, and --into measures it
// against the caller's own revision instead of the trunk.
func TestMergedCommitIntoRevision(t *testing.T) {
	f := setup(t)
	detached := filepath.Join(f.home, "detached")
	f.mustGit(f.dir, "worktree", "add", "--detach", "-q", detached)
	write(t, filepath.Join(detached, "work"), "shipped\n", 0644)
	f.mustGit(detached, "add", "work")
	f.mustGit(detached, "commit", "-qm", "detached work")
	head := f.mustGit(detached, "rev-parse", "HEAD")
	// "release" squash-lands the same change; the trunk (main) never gets it.
	f.mustGit(f.dir, "switch", "-qc", "release")
	write(t, filepath.Join(f.dir, "work"), "shipped\n", 0644)
	f.mustGit(f.dir, "add", "work")
	f.mustGit(f.dir, "commit", "-qm", "Squash detached work")
	f.mustGit(f.dir, "switch", "-q", "main")
	missing := strings.Repeat("0", 40)
	into, err := f.r.Merged(context.Background(), []string{head, missing}, false, "release")
	if err != nil || into.Trunk.Name != "release" || !isTrue(into.Branches[0].Merged) || into.Branches[1].Merged != nil ||
		!strings.Contains(into.Branches[1].Error, "no local branch or commit") {
		t.Fatalf("--into release: %+v %v", into, err)
	}
	trunk, err := f.r.Merged(context.Background(), []string{head}, false, "")
	if err != nil || trunk.Trunk.Name != "main" || trunk.Branches[0].Merged == nil || *trunk.Branches[0].Merged {
		t.Fatalf("against the trunk: %+v %v", trunk, err)
	}
	if _, err := f.r.Merged(context.Background(), []string{head}, false, "no-such-ref"); err == nil {
		t.Fatal("an unresolvable --into revision passed")
	}
}
