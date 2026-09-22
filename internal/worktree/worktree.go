package worktree

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Repo struct {
	dir, main, root, common string
	stderr                  io.Writer
}

func Open(ctx context.Context, dir, home string, stderr io.Writer) (*Repo, error) {
	inside, err := git(ctx, dir, "rev-parse", "--is-inside-work-tree")
	if err != nil || inside != "true" {
		return nil, fmt.Errorf("not inside a Git working tree: %s", dir)
	}
	list, err := git(ctx, dir, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	first, _, _ := strings.Cut(list, "\x00")
	if !strings.HasPrefix(first, "worktree ") {
		return nil, fmt.Errorf("cannot determine main worktree")
	}
	main := strings.TrimPrefix(first, "worktree ")
	common, err := git(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	return &Repo{dir: dir, main: main, common: common, root: filepath.Join(home, "dev", ".worktrees", filepath.Base(main)), stderr: stderr}, nil
}

type Verdict struct {
	Kind string   `json:"kind"`
	Refs []string `json:"refs,omitempty"`
}

func (v Verdict) String() string {
	return strings.Join(append([]string{v.Kind}, v.Refs...), " ")
}

func (r *Repo) validate(ctx context.Context, branch string) error {
	// Full-ref validation rejects path traversal and @{-1} expansion before any
	// fetch or mkdir. --branch adds Git's special HEAD and leading-dash rules.
	if _, err := git(ctx, r.dir, "check-ref-format", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("invalid branch name: %q", branch)
	}
	if _, err := git(ctx, r.dir, "check-ref-format", "--branch", branch); err != nil {
		return fmt.Errorf("invalid branch name: %q", branch)
	}
	return nil
}

func (r *Repo) resolve(ctx context.Context, branch string) (Verdict, error) {
	// Query failures must not masquerade as an absent branch.
	refs, err := git(ctx, r.dir, "for-each-ref", "--format=%(refname)", "refs/heads", "refs/remotes")
	if err != nil {
		return Verdict{}, err
	}
	v := Verdict{Kind: "absent"}
	for _, ref := range strings.Split(refs, "\n") {
		if ref == "refs/heads/"+branch {
			return Verdict{Kind: "local"}, nil
		}
		if !strings.HasPrefix(ref, "refs/remotes/") {
			continue
		}
		_, name, ok := strings.Cut(strings.TrimPrefix(ref, "refs/remotes/"), "/")
		if ok && name == branch {
			v.Refs = append(v.Refs, ref)
		}
	}
	switch len(v.Refs) {
	case 0:
	case 1:
		v.Kind = "remote"
	default:
		v.Kind = "ambiguous"
	}
	return v, nil
}

func envDuration(name string, fallback float64, unit time.Duration) time.Duration {
	f, err := strconv.ParseFloat(os.Getenv(name), 64)
	if err != nil || f <= 0 || f > 86400 {
		f = fallback
	}
	return time.Duration(f * float64(unit))
}

func (r *Repo) Resolve(ctx context.Context, branch string, fresh bool) (Verdict, error) {
	if err := r.validate(ctx, branch); err != nil {
		return Verdict{}, err
	}
	v, err := r.resolve(ctx, branch)
	if err != nil || v.Kind != "absent" || !fresh {
		return v, err
	}
	info, err := os.Stat(filepath.Join(r.common, "FETCH_HEAD"))
	if err == nil && info.Size() > 0 && time.Since(info.ModTime()) < envDuration("WT_BASE_MAX_AGE_MIN", 5, time.Minute) {
		return v, nil
	}
	remotes, err := git(ctx, r.dir, "remote")
	if err != nil {
		return Verdict{}, err
	}
	if remotes == "" {
		return v, nil
	}
	fmt.Fprintf(r.stderr, "gwt: no branch %q found; refreshing remote refs…\n", branch)
	fetchCtx, cancel := context.WithTimeout(ctx, envDuration("WT_FETCH_TIMEOUT", 8, time.Second))
	defer cancel()
	// Refresh all configured remotes, including a newly added remote without
	// tracking refs yet, so ambiguous names cannot silently become new branches.
	if _, err := git(fetchCtx, r.dir, "fetch", "--all", "--quiet"); err != nil {
		fmt.Fprintf(r.stderr, "gwt: fetch failed or timed out; using cached refs: %v\n", err)
	}
	return r.resolve(ctx, branch)
}

type Options struct {
	Branch, Base                              string
	ForceNew, NoCopy, NoFetch, NonInteractive bool
	Confirm                                   func(branch, base string) bool
}

type Result struct {
	Path     string   `json:"path"`
	Branch   string   `json:"branch"`
	Action   string   `json:"action"` // created | local | remote
	Base     string   `json:"base,omitempty"`
	Upstream string   `json:"upstream,omitempty"`
	Copied   int      `json:"copied"`
	Warnings []string `json:"warnings,omitempty"`
}

func (r *Repo) Create(ctx context.Context, o Options) (Result, error) {
	if err := r.validate(ctx, o.Branch); err != nil {
		return Result{}, err
	}
	dest := filepath.Join(r.root, o.Branch)
	if err := safeParents(r.root, filepath.Dir(dest)); err != nil {
		return Result{}, err
	}
	if err := freeSlot(dest); err != nil {
		return Result{}, err
	}
	v := Verdict{Kind: "absent"}
	var err error
	if !o.ForceNew {
		v, err = r.Resolve(ctx, o.Branch, !o.NoFetch)
	}
	if err != nil {
		return Result{}, err
	}
	if v.Kind == "ambiguous" {
		return Result{}, fmt.Errorf("%q exists on multiple remotes: %s; select one with git branch --track %s <remote>/%s, then retry", o.Branch, strings.Join(v.Refs, ", "), o.Branch, o.Branch)
	}
	result := Result{Path: dest, Branch: o.Branch, Action: v.Kind}
	args := []string{"worktree", "add"}
	switch v.Kind {
	case "local":
		fmt.Fprintf(r.stderr, "gwt: checking out existing local branch %q (base unused)\n", o.Branch)
		// -b would reject an existing branch; use its short name to attach HEAD.
		args = append(args, "--", dest, o.Branch)
	case "remote":
		result.Upstream = strings.TrimPrefix(v.Refs[0], "refs/remotes/")
		fmt.Fprintf(r.stderr, "gwt: checking out %q, tracking %s (base unused)\n", o.Branch, result.Upstream)
		args = append(args, "--track", "-b", o.Branch, "--", dest, v.Refs[0])
	default:
		base := o.Base
		if base == "" {
			base, err = git(ctx, r.dir, "rev-parse", "--abbrev-ref", "HEAD")
			if err != nil {
				return Result{}, err
			}
			if !o.NonInteractive && (o.Confirm == nil || !o.Confirm(o.Branch, base)) {
				return Result{}, fmt.Errorf("aborted; pass a base or --non-interactive to create without confirmation")
			}
		}
		// Verify before creating directories; --end-of-options prevents a base
		// such as --help from being interpreted as a Git option.
		if _, err := git(ctx, r.dir, "rev-parse", "--verify", "--end-of-options", base+"^{commit}"); err != nil {
			return Result{}, err
		}
		result.Action, result.Base = "created", base
		fmt.Fprintf(r.stderr, "gwt: creating branch %q from %q\n", o.Branch, base)
		args = append(args, "--no-track", "-b", o.Branch, "--", dest, base)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return Result{}, err
	}
	if _, err := git(ctx, r.dir, args...); err != nil {
		return Result{}, err
	}
	// Seeding is best-effort after creation. Report the existing worktree even
	// on a copy error, so callers don't retry a successful worktree add.
	if !o.NoCopy {
		result.Copied, result.Warnings = r.seed(ctx, dest)
	}
	for _, warning := range result.Warnings {
		fmt.Fprintln(r.stderr, "gwt: warning:", warning)
	}
	if result.Copied > 0 {
		fmt.Fprintf(r.stderr, "gwt: copied %d item(s) from main\n", result.Copied)
	}
	return result, nil
}

func freeSlot(dest string) error {
	info, err := os.Lstat(dest)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		entries, err := os.ReadDir(dest)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			return nil
		}
	}
	return fmt.Errorf("path already exists: %s (nothing removed)", dest)
}

// A slash in a branch name creates parents. Refuse symlink parents so a path
// inside our root cannot silently place a checkout elsewhere.
func safeParents(root, dir string) error {
	for p := dir; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && !info.IsDir() {
			return fmt.Errorf("parent is not a real directory: %s", p)
		}
		if p == root {
			return nil
		}
		if p == filepath.Dir(p) {
			return fmt.Errorf("path is outside root %s", root)
		}
	}
}
