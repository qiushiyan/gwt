package worktree

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

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
