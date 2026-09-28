package worktree

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Trunk is where branch work lands: the remote default branch when there is
// one. list, merged, and remove all measure integration against it, so a
// "merged" tag, a reap, and a removal cannot disagree about the same branch.
// The creation base (config base, often the caller's HEAD) is a different
// question and plays no part here.
type Trunk struct {
	Name       string `json:"name"`             // origin/develop, or a local main
	Commit     string `json:"commit"`           // what verdicts were measured against
	Remote     string `json:"remote,omitempty"` // refreshed by --fetch; empty for a local trunk
	Stale      bool   `json:"stale"`            // a remote trunk not fetched within fetch.max_age
	Fetched    bool   `json:"fetched,omitempty"`
	FetchError string `json:"fetch_error,omitempty"`
}

// Full ref names, so a local branch called origin/main cannot shadow the remote.
var trunkCandidates = []string{"refs/remotes/origin/HEAD", "refs/remotes/origin/main", "refs/remotes/origin/master", "refs/heads/main", "refs/heads/master"}

func (r *Repo) resolveTrunk(ctx context.Context) (Trunk, error) {
	for _, ref := range trunkCandidates {
		commit, err := git(ctx, r.main, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
		if err != nil || commit == "" {
			continue
		}
		// Follows origin/HEAD to the branch it names.
		full, err := git(ctx, r.main, "rev-parse", "--symbolic-full-name", ref)
		if err != nil {
			return Trunk{}, err
		}
		t := Trunk{Name: strings.TrimPrefix(strings.TrimPrefix(full, "refs/remotes/"), "refs/heads/"), Commit: commit}
		if rest, ok := strings.CutPrefix(full, "refs/remotes/"); ok {
			t.Remote, _, _ = strings.Cut(rest, "/")
		}
		return t, nil
	}
	// No origin and no main/master: the main checkout's branch is all we have.
	full, err := git(ctx, r.main, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		return Trunk{}, fmt.Errorf("no trunk: none of origin/HEAD, origin/main, origin/master, main, master exists and the main checkout is detached")
	}
	commit, err := git(ctx, r.main, "rev-parse", "--verify", full+"^{commit}")
	if err != nil {
		return Trunk{}, err
	}
	return Trunk{Name: strings.TrimPrefix(full, "refs/heads/"), Commit: commit}, nil
}

// fetchStale reports whether no fetch landed within fetch.max_age. Git writes
// FETCH_HEAD into the fetching worktree's own Git directory, so every gwt fetch
// runs from the main checkout and this reads the shared directory's copy:
// freshness is repository-wide. An empty FETCH_HEAD with a fresh mtime is what
// a killed fetch leaves, so it is stale.
func (r *Repo) fetchStale() bool {
	info, err := os.Stat(filepath.Join(r.common, "FETCH_HEAD"))
	return err != nil || info.Size() == 0 || time.Since(info.ModTime()) >= r.config.Fetch.MaxAge
}

// Trunk resolves the trunk. With refresh, a stale remote trunk is fetched
// first, bounded by fetch.timeout; a failed fetch warns and keeps cached refs,
// and the result says so rather than grading silently against an old trunk.
func (r *Repo) Trunk(ctx context.Context, refresh bool) (Trunk, error) {
	t, err := r.resolveTrunk(ctx)
	if err != nil {
		return t, err
	}
	t.Stale = t.Remote != "" && r.fetchStale()
	if !refresh || !t.Stale {
		return t, nil
	}
	fetchCtx, cancel := context.WithTimeout(ctx, r.config.Fetch.Timeout)
	// From the main checkout: FETCH_HEAD is per-worktree, and fetchStale reads
	// the main checkout's (the shared Git directory's) copy.
	_, err = git(fetchCtx, r.main, "fetch", "--quiet", "--", t.Remote)
	cancel()
	if err != nil {
		t.FetchError = err.Error()
		fmt.Fprintf(r.stderr, "gwt: could not refresh %s; merge verdicts use cached refs: %v\n", t.Remote, err)
		return t, nil
	}
	fresh, err := r.resolveTrunk(ctx)
	if err != nil {
		return t, err
	}
	fresh.Fetched = true
	return fresh, nil
}

// verdicts memoizes mergedInto, a pure function of two commits. Without it a
// listing pays a patch-id scan per unmerged branch on every call; with it only
// the first call after a branch or the trunk moves does. The file lives in the
// shared Git directory, one "<tip> <trunk> <0|1>" line per verdict; appends
// stay far below PIPE_BUF, so concurrent gwt processes cannot interleave lines.
// The file name versions the verdict policy.
type verdicts struct {
	path  string
	mu    sync.Mutex
	known map[string]bool
	added []string
}

const verdictMemoLimit = 2000

func (r *Repo) loadVerdicts() *verdicts {
	v := &verdicts{path: filepath.Join(r.common, "gwt-merged-v1"), known: map[string]bool{}}
	f, err := os.Open(v.path)
	if err != nil {
		return v // absent or unreadable: compute everything
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) == 3 && (fields[2] == "0" || fields[2] == "1") {
			v.known[fields[0]+" "+fields[1]] = fields[2] == "1"
		}
	}
	return v
}

func (v *verdicts) get(tip, trunk string) (merged, ok bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	merged, ok = v.known[tip+" "+trunk]
	return
}

func (v *verdicts) put(tip, trunk string, merged bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	bit := "0"
	if merged {
		bit = "1"
	}
	v.known[tip+" "+trunk] = merged
	v.added = append(v.added, tip+" "+trunk+" "+bit+"\n")
}

// save is best-effort: a lost memo line costs one recomputation, never a
// wrong verdict. Past the limit the file restarts with this call's verdicts.
func (v *verdicts) save() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.added) == 0 {
		return
	}
	lines := strings.Join(v.added, "")
	if len(v.known) > verdictMemoLimit {
		tmp := v.path + ".tmp"
		if os.WriteFile(tmp, []byte(lines), 0644) == nil && os.Rename(tmp, v.path) == nil {
			return
		}
		os.Remove(tmp)
		return
	}
	f, err := os.OpenFile(v.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
	if err != nil {
		return
	}
	f.WriteString(lines)
	f.Close()
}

func (r *Repo) merged(ctx context.Context, memo *verdicts, tip, trunk string) (bool, error) {
	if v, ok := memo.get(tip, trunk); ok {
		return v, nil
	}
	v, err := r.mergedInto(ctx, tip, trunk)
	if err != nil {
		return false, err
	}
	memo.put(tip, trunk, v)
	return v, nil
}

// parallel runs fn(0..n-1) on at most one worker per CPU. Every probe is a Git
// process; launching all of them at once made 25 status scans contend for
// the same disk and index reads.
func parallel(n int, fn func(i int)) {
	workers := min(n, runtime.NumCPU())
	next := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				fn(i)
			}
		}()
	}
	for i := range n {
		next <- i
	}
	close(next)
	wg.Wait()
}

type Worktree struct {
	Path     string `json:"path"`
	Branch   string `json:"branch,omitempty"` // empty when detached
	Head     string `json:"head,omitempty"`
	Main     bool   `json:"main"`
	Current  bool   `json:"current"` // contains the caller's directory
	Locked   bool   `json:"locked"`
	Prunable bool   `json:"prunable"` // registered, but the directory is gone
	Dirty    bool   `json:"dirty"`
	// Whether the branch's work is already in the trunk (ancestry, squash, or
	// rebase). null for a detached checkout or when the check failed.
	Merged *bool  `json:"merged"`
	Error  string `json:"error,omitempty"`
}

type Listing struct {
	Trunk     Trunk      `json:"trunk"`
	Worktrees []Worktree `json:"worktrees"`
}

// List reports every registered worktree in Git's order with its dirt and its
// merge verdict against the trunk. It is read-only apart from the verdict memo
// and the dangling objects a squash check writes, and never waits on the
// network unless refresh asks for a stale trunk to be fetched first.
func (r *Repo) List(ctx context.Context, refresh bool) (Listing, error) {
	trunk, err := r.Trunk(ctx, refresh)
	if err != nil {
		return Listing{}, err
	}
	out, err := git(ctx, r.dir, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return Listing{}, err
	}
	current, _ := git(ctx, r.dir, "rev-parse", "--show-toplevel")
	current = canonical(current)
	var list []Worktree
	for i, record := range strings.Split(strings.TrimRight(out, "\x00"), "\x00\x00") {
		var w Worktree
		for _, field := range strings.Split(record, "\x00") {
			switch {
			case strings.HasPrefix(field, "worktree "):
				w.Path = strings.TrimPrefix(field, "worktree ")
			case strings.HasPrefix(field, "HEAD "):
				w.Head = strings.TrimPrefix(field, "HEAD ")
			case strings.HasPrefix(field, "branch "):
				w.Branch = strings.TrimPrefix(field, "branch refs/heads/")
			case field == "locked" || strings.HasPrefix(field, "locked "):
				w.Locked = true
			case field == "prunable" || strings.HasPrefix(field, "prunable "):
				w.Prunable = true
			case field == "bare":
				w.Path = "" // a bare main repository has no checkout to list
			}
		}
		if w.Path == "" {
			continue
		}
		w.Main = i == 0
		w.Current = current != "" && canonical(w.Path) == current
		list = append(list, w)
	}
	memo := r.loadVerdicts()
	parallel(len(list), func(i int) {
		w := &list[i]
		if w.Prunable {
			return
		}
		status, err := git(ctx, w.Path, "--no-optional-locks", "status", "--porcelain")
		if err != nil {
			w.Error = err.Error()
		}
		w.Dirty = status != ""
		if w.Branch == "" || w.Head == "" {
			return
		}
		v, err := r.merged(ctx, memo, w.Head, trunk.Commit)
		if err != nil {
			w.Error = err.Error()
			return
		}
		w.Merged = &v
	})
	memo.save()
	if list == nil {
		list = []Worktree{}
	}
	return Listing{Trunk: trunk, Worktrees: list}, nil
}

type BranchVerdict struct {
	Branch string `json:"branch"`
	Commit string `json:"commit,omitempty"`
	Merged *bool  `json:"merged"` // null when the branch does not exist or the check failed
	Error  string `json:"error,omitempty"`
}

type Verdicts struct {
	Trunk    Trunk           `json:"trunk"`
	Branches []BranchVerdict `json:"branches"`
}

// Merged judges local branches with or without a checkout, e.g. after the
// popup has already moved a worktree away and must decide the branch's fate.
func (r *Repo) Merged(ctx context.Context, branches []string, refresh bool) (Verdicts, error) {
	trunk, err := r.Trunk(ctx, refresh)
	if err != nil {
		return Verdicts{}, err
	}
	out := make([]BranchVerdict, len(branches))
	memo := r.loadVerdicts()
	parallel(len(branches), func(i int) {
		b := &out[i]
		b.Branch = branches[i]
		if err := r.validate(ctx, b.Branch); err != nil {
			b.Error = err.Error()
			return
		}
		tip, err := git(ctx, r.dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+b.Branch)
		if err != nil || tip == "" {
			b.Error = "no local branch " + b.Branch
			return
		}
		b.Commit = tip
		v, err := r.merged(ctx, memo, tip, trunk.Commit)
		if err != nil {
			b.Error = err.Error()
			return
		}
		b.Merged = &v
	})
	memo.save()
	return Verdicts{Trunk: trunk, Branches: out}, nil
}

// canonical compares paths through symlinks (macOS /tmp is /private/tmp).
func canonical(p string) string {
	if p == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return filepath.Clean(p)
}
