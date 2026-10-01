package worktree

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
		wg.Go(func() {
			for i := range next {
				fn(i)
			}
		})
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
	// Uncommitted, untracked, or hidden by an assume-unchanged/skip-worktree
	// index flag. A status that cannot be read counts as dirty (error says why):
	// unknown state is never treated as clean.
	Dirty bool `json:"dirty"`
	// Whether the branch's work is already in the trunk (ancestry, squash, or
	// rebase). null for a detached checkout, an unreadable status, or when the
	// check failed.
	Merged *bool `json:"merged"`
	// Whether `gwt remove <path>` without flags would remove it. It applies
	// remove's own rule (refusal), so a caller's tag and the removal agree.
	Removable bool   `json:"removable"`
	Error     string `json:"error,omitempty"`
	nests     string // another registered worktree inside this one
}

type Listing struct {
	Trunk     Trunk      `json:"trunk"`
	Worktrees []Worktree `json:"worktrees"`
}

// List reports every registered worktree in Git's order with its dirt, its
// merge verdict against the trunk, and whether remove would take it. It is
// read-only apart from the verdict memo and the dangling objects a squash
// check writes, and never waits on the network unless refresh asks for a
// stale trunk to be fetched first.
func (r *Repo) List(ctx context.Context, refresh bool) (Listing, error) {
	trunk, err := r.Trunk(ctx, refresh)
	if err != nil {
		return Listing{}, err
	}
	list, err := r.registered(ctx)
	if err != nil {
		return Listing{}, err
	}
	memo := r.loadVerdicts()
	parallel(len(list), func(i int) {
		w := &list[i]
		// An unreadable status leaves the verdict unknown too.
		if !w.Prunable && !probe(ctx, w) {
			return
		}
		if w.Branch != "" && w.Head != "" {
			v, err := r.merged(ctx, memo, w.Head, trunk.Commit)
			if err != nil {
				w.Error = err.Error()
				return
			}
			w.Merged = &v
		}
		w.Removable = w.refusal(RemoveOptions{}) == nil
	})
	memo.save()
	if list == nil {
		list = []Worktree{}
	}
	return Listing{Trunk: trunk, Worktrees: list}, nil
}

// registered parses Git's registrations in Git's order, skipping a bare main
// repository, and marks main, current and nesting. It probes nothing.
func (r *Repo) registered(ctx context.Context) ([]Worktree, error) {
	out, err := git(ctx, r.dir, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	current, _ := git(ctx, r.dir, "rev-parse", "--show-toplevel")
	current = canonical(current)
	var list []Worktree
	var real []string
	for i, record := range strings.Split(strings.TrimRight(out, "\x00"), "\x00\x00") {
		var w Worktree
		for field := range strings.SplitSeq(record, "\x00") {
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
		real = append(real, canonical(w.Path))
		w.Current = current != "" && real[len(real)-1] == current
		list = append(list, w)
	}
	// Removing a checkout takes everything beneath it, another checkout included.
	for i := range list {
		for j, other := range real {
			if j != i && strings.HasPrefix(other, real[i]+string(filepath.Separator)) {
				list[i].nests = list[j].Path
				break
			}
		}
	}
	return list, nil
}

// probe reads a checkout's dirt. Edits hidden by an assume-unchanged or
// skip-worktree flag count, since status cannot see them and removal would
// discard them. A failed probe marks the worktree dirty, records the error,
// and returns false.
func probe(ctx context.Context, w *Worktree) bool {
	status, err := git(ctx, w.Path, "--no-optional-locks", "status", "--porcelain")
	var flags string
	if err == nil {
		flags, err = git(ctx, w.Path, "ls-files", "-v", "-z")
	}
	if err != nil {
		w.Dirty, w.Error = true, err.Error()
		return false
	}
	w.Dirty = status != ""
	for entry := range strings.SplitSeq(flags, "\x00") {
		if entry != "" && (entry[0] == 'S' || ('a' <= entry[0] && entry[0] <= 'z')) {
			w.Dirty = true
			break
		}
	}
	return true
}

type BranchVerdict struct {
	Branch string `json:"branch"`
	Commit string `json:"commit,omitempty"`
	Merged *bool  `json:"merged"` // null when the branch does not exist or the check failed
	Error  string `json:"error,omitempty"`
}

type Verdicts struct {
	Trunk    Trunk           `json:"trunk"` // or the explicit ref Merged was given
	Branches []BranchVerdict `json:"branches"`
}

// A full commit id (SHA-1 or SHA-256), accepted where a branch is not found.
var commitID = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// Merged judges local branches with or without a checkout, e.g. after the
// popup has already moved a worktree away and must decide the branch's fate.
// An argument that names no local branch but is a full commit id is judged as
// that commit, which is how a caller holding a detached checkout's HEAD asks.
// With into, the verdict is measured against that revision instead of the
// trunk and nothing is fetched: the caller owns its freshness (the
// clean-worktrees audit verifies its base against advertised remote heads).
func (r *Repo) Merged(ctx context.Context, args []string, refresh bool, into string) (Verdicts, error) {
	var trunk Trunk
	var err error
	if into != "" {
		commit, rerr := git(ctx, r.dir, "rev-parse", "--verify", "--end-of-options", into+"^{commit}")
		if rerr != nil {
			return Verdicts{}, fmt.Errorf("--into %s: %w", into, rerr)
		}
		trunk = Trunk{Name: into, Commit: commit}
	} else if trunk, err = r.Trunk(ctx, refresh); err != nil {
		return Verdicts{}, err
	}
	out := make([]BranchVerdict, len(args))
	memo := r.loadVerdicts()
	parallel(len(args), func(i int) {
		b := &out[i]
		b.Branch = args[i]
		tip, err := r.branchOrCommit(ctx, b.Branch)
		if err != nil {
			b.Error = err.Error()
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

// branchOrCommit resolves a local branch first, so a branch whose name happens
// to be hex still means the branch; only a full commit id falls through.
func (r *Repo) branchOrCommit(ctx context.Context, arg string) (string, error) {
	if !commitID.MatchString(arg) {
		if err := r.validate(ctx, arg); err != nil {
			return "", err
		}
	}
	if tip, err := git(ctx, r.dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+arg); err == nil && tip != "" {
		return tip, nil
	}
	if commitID.MatchString(arg) {
		if tip, err := git(ctx, r.dir, "rev-parse", "--verify", "--quiet", arg+"^{commit}"); err == nil && tip != "" {
			return tip, nil
		}
		return "", fmt.Errorf("no local branch or commit %s", arg)
	}
	return "", fmt.Errorf("no local branch %s", arg)
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
