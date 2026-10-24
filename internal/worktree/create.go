package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Resolution is how creation would obtain a branch: a local branch, the one
// remote branch of that name, several (ambiguous), or none (absent). It is
// not a merge verdict, which verdict.go owns.
type Resolution struct {
	Kind string   `json:"kind"`
	Refs []string `json:"refs,omitempty"`
}

func (v Resolution) String() string {
	return strings.Join(append([]string{v.Kind}, v.Refs...), " ")
}

func (r *Repo) resolve(ctx context.Context, branch string) (Resolution, error) {
	// Query failures must not masquerade as an absent branch.
	refs, err := git(ctx, r.dir, "for-each-ref", "--format=%(refname)", "refs/heads", "refs/remotes")
	if err != nil {
		return Resolution{}, err
	}
	v := Resolution{Kind: "absent"}
	for ref := range strings.SplitSeq(refs, "\n") {
		if ref == "refs/heads/"+branch {
			return Resolution{Kind: "local"}, nil
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

// Path computes placement without fetching, creating directories, or requiring
// a free slot. Callers use it to diagnose existing worktrees before creation.
func (r *Repo) Path(ctx context.Context, branch string) (string, error) {
	if branch == "" {
		return r.root, nil
	}
	if err := r.validate(ctx, branch); err != nil {
		return "", err
	}
	return filepath.Join(r.root, branch), nil
}

func (r *Repo) Resolve(ctx context.Context, branch string, fresh bool) (Resolution, error) {
	if err := r.validate(ctx, branch); err != nil {
		return Resolution{}, err
	}
	v, err := r.resolve(ctx, branch)
	if err != nil || v.Kind != "absent" || !fresh {
		return v, err
	}
	if !r.fetchStale() {
		return v, nil
	}
	remotes, err := git(ctx, r.dir, "remote")
	if err != nil {
		return Resolution{}, err
	}
	if remotes == "" {
		return v, nil
	}
	fmt.Fprintf(r.stderr, "gwt: no branch %q found; refreshing remote refs…\n", branch)
	fetchCtx, cancel := context.WithTimeout(ctx, r.config.Fetch.Timeout)
	defer cancel()
	// Refresh all configured remotes, including a newly added remote without
	// tracking refs yet, so ambiguous names cannot silently become new branches.
	// From the main checkout, so FETCH_HEAD lands where fetchStale reads it.
	if _, err := git(fetchCtx, r.main, "fetch", "--all", "--quiet"); err != nil {
		fmt.Fprintf(r.stderr, "gwt: fetch failed or timed out; using cached refs: %v\n", err)
	}
	return r.resolve(ctx, branch)
}

type CreateOptions struct {
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

func (r *Repo) Create(ctx context.Context, o CreateOptions) (Result, error) {
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
	v := Resolution{Kind: "absent"}
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
			base = r.config.Base
		}
		if base == "HEAD" && o.Base == "" {
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
