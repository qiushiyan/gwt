package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Removal is one target's outcome. RecoveryRef keeps what the removal made
// unreachable: a snapshot of discarded changes (parented on HEAD, so it keeps
// the tip as well, and on a commit of the index), else the tip of a force-deleted branch or of a removed
// detached checkout. `git branch <name> <ref>` restores it.
type Removal struct {
	OK              bool   `json:"ok"`
	Target          string `json:"target"`
	Path            string `json:"path,omitempty"`
	Branch          string `json:"branch"`
	WorktreeRemoved bool   `json:"worktree_removed"`
	BranchDeleted   bool   `json:"branch_deleted"`
	RecoveryRef     string `json:"recovery_ref,omitempty"`
	Error           string `json:"error,omitempty"`
}

type RemoveOptions struct {
	Force        bool   // delete an unmerged branch, keeping its tip as a recovery ref
	DiscardDirty bool   // snapshot uncommitted work to a recovery ref, then remove
	KeepBranch   bool   // remove the checkout only
	ExpectHead   string // refuse unless the target's commit is still this one
}

// Recovery refs live outside refs/heads, so no branch list or completion shows
// them, and keep their objects through gc until recovery.keep expires them.
const recoveryNS = "refs/wt-trash"

// Trash batches younger than this may belong to a removal still sweeping.
const trashGrace = 2 * time.Minute

var errUnmerged = errors.New("unmerged")

// refusal is the one eligibility rule: why remove with o refuses this
// worktree, or nil. list's removable is refusal with no options. Without a
// verdict in Merged a branch counts as unmerged; Remove judges it itself.
func (w *Worktree) refusal(o RemoveOptions) error {
	switch {
	case w.Main:
		return fmt.Errorf("refusing to remove the main worktree: %s", w.Path)
	case w.Current:
		return errors.New("refusing to remove the current worktree; run gwt remove from another checkout")
	case w.Locked:
		return fmt.Errorf("worktree is locked: %s; unlock it explicitly before removal", w.Path)
	case w.nests != "":
		return fmt.Errorf("worktree %s contains another worktree, %s; remove that one first", w.Path, w.nests)
	case w.Dirty && w.Error != "" && !o.DiscardDirty:
		return fmt.Errorf("cannot read the worktree's status, so its changes are unknown: %s", w.Error)
	case w.Dirty && !o.DiscardDirty:
		return fmt.Errorf("worktree has uncommitted, untracked, or index-hidden changes: %s; commit or move them, or pass --discard-dirty to snapshot them to a recovery ref", w.Path)
	case w.Branch != "" && !o.KeepBranch && !o.Force && (w.Merged == nil || !*w.Merged):
		return errUnmerged
	}
	return nil
}

// Remove is deliberately non-interactive. A target is a branch, found through
// Git's registration rather than a path guessed from config (roots change),
// or a checkout path (absolute or ./-relative), which reaches detached
// checkouts. A branch without a checkout is a branch-only target. Targets
// share one batch: one trash directory and one recovery-ref prefix.
//
// A checkout is renamed into <worktree_root>/.trash (instant on one
// filesystem), unregistered, and swept by a detached process, so a caller
// that exits at once, like a closing tmux popup, neither waits for large
// dependency trees nor kills the sweep. The commit and the dirt are read again
// after the verdict's fetch, just before the snapshot and the rename, and a
// branch goes only from the commit that was judged; nothing irreversible
// happens without a recovery ref. A writer still at work in the checkout can
// race that last read: callers stop theirs first. When a removal fails partway,
// worktree_removed says whether the checkout is already gone.
func (r *Repo) Remove(ctx context.Context, targets []string, o RemoveOptions) []Removal {
	out := make([]Removal, len(targets))
	list, err := r.registered(ctx)
	rm := remover{r: r, o: o, list: list, memo: r.loadVerdicts(),
		batch: fmt.Sprintf("%d.%d", time.Now().Unix(), os.Getpid()),
		trash: filepath.Join(r.config.WorktreeRoot, ".trash")}
	for i, target := range targets {
		out[i].Target = target
		if err == nil {
			err := rm.remove(ctx, &out[i], i+1)
			if err != nil {
				out[i].Error = err.Error()
			}
			out[i].OK = err == nil
		} else {
			out[i].Error = err.Error()
		}
	}
	rm.memo.save()
	sweep(rm.trash, rm.batch)
	r.expireRecovery(ctx, time.Now())
	return out
}

type remover struct {
	r            *Repo
	o            RemoveOptions
	list         []Worktree
	memo         *verdicts
	trunk        *Trunk
	batch, trash string
}

func (m *remover) remove(ctx context.Context, res *Removal, slot int) error {
	r, o := m.r, m.o
	w, err := m.find(ctx, res.Target)
	if err != nil {
		return err
	}
	var tip string
	if w != nil {
		res.Path, res.Branch, tip = w.Path, w.Branch, w.Head
		if !w.Prunable {
			probe(ctx, w)
		}
	} else {
		res.Branch = res.Target
		if tip, err = git(ctx, r.dir, "rev-parse", "--verify", "refs/heads/"+res.Branch); err != nil {
			return fmt.Errorf("no registered worktree or local branch %q; inspect git worktree list and git branch --list before choosing a cleanup action", res.Branch)
		}
	}
	if o.ExpectHead != "" {
		want, err := git(ctx, r.dir, "rev-parse", "--verify", "--end-of-options", o.ExpectHead+"^{commit}")
		if err != nil || want != tip {
			return fmt.Errorf("%s is at %s, not the expected %s; inspect git log %s before removing it", res.Target, short(tip), o.ExpectHead, res.Target)
		}
	}
	deleting := res.Branch != "" && !o.KeepBranch
	if w == nil && !deleting {
		return fmt.Errorf("nothing to remove: branch %q has no checkout and --keep-branch keeps it", res.Branch)
	}
	// The verdict, which may fetch, comes after the cheap refusals.
	if w != nil {
		if err := w.refusal(o); err != nil && !errors.Is(err, errUnmerged) {
			return err
		}
	}
	if deleting && !o.Force {
		merged, err := m.judge(ctx, tip)
		if err != nil {
			return err
		}
		if !merged {
			return m.unmerged(res.Branch, tip)
		}
	}
	// Everything above took time, and a writer may still be at work: read the
	// commit and the dirt again, so what is kept is what is there now.
	if err := m.recheck(ctx, w, res.Branch, tip); err != nil {
		return err
	}
	// Keep what this removal makes unreachable.
	keep, name := "", res.Branch
	if name == "" {
		name = "detached"
	}
	switch {
	case w != nil && w.Dirty:
		if keep, err = snapshot(ctx, w.Path, "gwt remove: uncommitted work in "+name); err != nil {
			return fmt.Errorf("could not snapshot the uncommitted work, so the checkout stays: %w", err)
		}
	case deleting && o.Force, w != nil && w.Branch == "":
		keep = tip
	}
	if keep != "" {
		// The slot keeps feat/x and feat-x apart: they flatten alike.
		ref := fmt.Sprintf("%s/%s/%03d-%s", recoveryNS, m.batch, slot, strings.ReplaceAll(name, "/", "-"))
		if _, err := git(ctx, r.dir, "update-ref", ref, keep, ""); err != nil {
			return fmt.Errorf("could not keep a recovery ref, so nothing was removed: %w", err)
		}
		res.RecoveryRef = ref
	}
	if w != nil {
		if !w.Prunable {
			dest := filepath.Join(m.trash, m.batch)
			if err := os.MkdirAll(dest, 0755); err != nil {
				return fmt.Errorf("could not create the trash: %w", err)
			}
			if err := os.Rename(w.Path, filepath.Join(dest, strconv.Itoa(slot))); err != nil {
				return fmt.Errorf("could not move the checkout into %s, so it stays: %w", dest, err)
			}
		}
		// The checkout is gone from here on: the sweep deletes the trash.
		res.WorktreeRemoved = true
		r.removeEmptyParents(w.Path)
		// The directory is gone, so this drops only the registration.
		if _, err := git(ctx, r.dir, "worktree", "remove", "--", w.Path); err != nil {
			return fmt.Errorf("checkout moved to the trash, but its registration remains: %w; run git worktree prune", err)
		}
	}
	if deleting {
		// Only the tip that was judged and kept: a commit made since stays. No
		// branch -d: squash and rebase merges fail its ancestry-only test, and
		// the verdict or --force (with its recovery ref) already decided.
		if _, err := git(ctx, r.dir, "update-ref", "-d", "refs/heads/"+res.Branch, tip); err != nil {
			return fmt.Errorf("branch %q remains: %w; inspect git branch -v and resolve the deletion error before deleting the branch directly", res.Branch, err)
		}
		res.BranchDeleted = true
		// What branch -D also drops; a branch without settings has no section.
		git(ctx, r.dir, "config", "--remove-section", "branch."+res.Branch)
	}
	return nil
}

// recheck refuses when the target's commit moved off tip, or its checkout
// turned dirty without --discard-dirty, since the evidence was first read.
func (m *remover) recheck(ctx context.Context, w *Worktree, branch, tip string) error {
	var now string
	var err error
	switch {
	case w != nil && !w.Prunable:
		now, err = git(ctx, w.Path, "rev-parse", "--verify", "HEAD")
	case branch != "":
		now, err = git(ctx, m.r.dir, "rev-parse", "--verify", "refs/heads/"+branch)
	default:
		return nil
	}
	name := branch
	if name == "" {
		name = w.Path
	}
	if err != nil {
		return fmt.Errorf("could not read the commit of %s again, so it stays: %w", name, err)
	}
	if now != tip {
		return fmt.Errorf("%s moved from %s while gwt was checking it; inspect it and run gwt remove again", name, short(tip))
	}
	if w == nil || w.Prunable {
		return nil
	}
	probe(ctx, w)
	if err := w.refusal(m.o); err != nil && !errors.Is(err, errUnmerged) {
		return err
	}
	return nil
}

// find resolves a target to its registration, or nil for a branch without a
// checkout.
func (m *remover) find(ctx context.Context, target string) (*Worktree, error) {
	if filepath.IsAbs(target) || strings.HasPrefix(target, ".") {
		path := target
		if !filepath.IsAbs(path) {
			path = filepath.Join(m.r.dir, path)
		}
		for i := range m.list {
			if canonical(m.list[i].Path) == canonical(path) {
				return &m.list[i], nil
			}
		}
		return nil, fmt.Errorf("no registered worktree at %s; inspect git worktree list", path)
	}
	if err := m.r.validate(ctx, target); err != nil {
		return nil, err
	}
	var found *Worktree
	for i := range m.list {
		if m.list[i].Branch == target {
			if found != nil {
				return nil, fmt.Errorf("branch %q is checked out in multiple worktrees; inspect git worktree list before choosing a checkout to remove", target)
			}
			found = &m.list[i]
		}
	}
	return found, nil
}

// judge reports whether tip is merged into the trunk, refreshed when stale, so
// a PR squash-merged on GitHub minutes ago counts without a manual fetch.
func (m *remover) judge(ctx context.Context, tip string) (bool, error) {
	if m.trunk == nil {
		t, err := m.r.Trunk(ctx, true)
		if err != nil {
			return false, err
		}
		m.trunk = &t
	}
	return m.r.merged(ctx, m.memo, tip, m.trunk.Commit)
}

func (m *remover) unmerged(branch, tip string) error {
	t := m.trunk
	return fmt.Errorf("branch %q has work not confirmed in %s (%s); inspect git log --oneline %s..%s and the branch diff before using --force, which keeps its tip as a recovery ref", branch, t.Name, short(t.Commit), short(t.Commit), short(tip))
}

func short(sha string) string { return sha[:min(12, len(sha))] }

// snapshot commits a checkout's entire working state, tracked edits and
// untracked files alike, and returns the commit. Like a stash, its first
// parent is HEAD and its second a commit of the index, which holds a staged
// version the working tree has since changed. Not `git stash create`: that
// keeps tracked changes only, and an agent's dirt is mostly new files.
// Scratch index files leave the checkout's own index and the shared stash
// list alone, and `add -A` still obeys .gitignore, so dependency trees stay
// out. The working-state index starts without the checkout's
// assume-unchanged/skip-worktree flags, so edits they hid are kept too. An
// index with unresolved conflicts has no tree, so it fails the snapshot.
func snapshot(ctx context.Context, path, message string) (string, error) {
	dir, err := os.MkdirTemp("", "gwt-snapshot-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	var parents []string
	head, err := git(ctx, path, "rev-parse", "--verify", "--quiet", "HEAD")
	if err == nil && head != "" {
		parents = []string{"-p", head}
	}
	// A copy, so writing its tree cannot touch the checkout's index.
	index, err := git(ctx, path, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return "", err
	}
	staged := filepath.Join(dir, "staged")
	if data, err := os.ReadFile(index); err == nil {
		if err := os.WriteFile(staged, data, 0600); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	tree, err := gitEnv(ctx, path, []string{"GIT_INDEX_FILE=" + staged}, "write-tree")
	if err != nil {
		return "", fmt.Errorf("the index has no tree: %w", err)
	}
	indexCommit, err := git(ctx, path, append(append(scratchIdentity(), "commit-tree", "-m", "index: "+message), append(parents, tree)...)...)
	if err != nil {
		return "", err
	}
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(dir, "index")}
	if head != "" {
		if _, err := gitEnv(ctx, path, env, "read-tree", "HEAD"); err != nil {
			return "", err
		}
	}
	if _, err := gitEnv(ctx, path, env, "add", "-A"); err != nil {
		return "", err
	}
	if tree, err = gitEnv(ctx, path, env, "write-tree"); err != nil {
		return "", err
	}
	args := append(append(scratchIdentity(), "commit-tree", "-m", message), parents...)
	return git(ctx, path, append(args, "-p", indexCommit, tree)...)
}

// Objects gwt writes for itself need neither the user's identity nor signing.
func scratchIdentity() []string {
	return []string{"-c", "user.name=gwt", "-c", "user.email=gwt@localhost", "-c", "commit.gpgSign=false"}
}

// removeEmptyParents removes the empty directories a slashed branch leaves,
// only under the current configured root. A checkout under an older root can
// still be removed without sweeping that root. Never a scan: one walk over
// sibling checkouts' dependency trees once turned a removal into a minute.
func (r *Repo) removeEmptyParents(path string) {
	root, err := filepath.EvalSymlinks(r.root)
	if err != nil {
		return
	}
	for parent := filepath.Dir(canonical(path)); strings.HasPrefix(parent, root+string(filepath.Separator)); parent = filepath.Dir(parent) {
		if os.Remove(parent) != nil {
			return
		}
	}
}

// sweep deletes this batch's trash, and any batch a killed run left behind,
// in a new session: it outlives the caller and the hangup a closing tmux popup
// sends its process group. Batches younger than trashGrace may still belong to
// another run, so they are left to it.
func sweep(trash, batch string) {
	entries, _ := os.ReadDir(trash)
	var doomed []string
	for _, e := range entries {
		info, err := e.Info()
		if e.Name() == batch || (err == nil && time.Since(info.ModTime()) > trashGrace) {
			doomed = append(doomed, filepath.Join(trash, e.Name()))
		}
	}
	if len(doomed) == 0 {
		return
	}
	cmd := exec.Command("rm", append([]string{"-rf", "--"}, doomed...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if cmd.Start() == nil {
		cmd.Process.Release()
	}
}

// expireRecovery drops recovery refs older than recovery.keep: refs keep their
// objects forever, so a net nobody prunes is a disk leak. The batch name starts
// with its epoch, so the age is in the ref name.
func (r *Repo) expireRecovery(ctx context.Context, now time.Time) {
	keep := r.config.Recovery.Keep
	if keep <= 0 {
		return
	}
	refs, err := git(ctx, r.dir, "for-each-ref", "--format=%(refname)", recoveryNS)
	if err != nil {
		return
	}
	for ref := range strings.SplitSeq(refs, "\n") {
		batch, _, _ := strings.Cut(strings.TrimPrefix(ref, recoveryNS+"/"), "/")
		epoch, _, _ := strings.Cut(batch, ".")
		if sec, err := strconv.ParseInt(epoch, 10, 64); err == nil && now.Sub(time.Unix(sec, 0)) > keep {
			git(ctx, r.dir, "update-ref", "-d", ref)
		}
	}
}

// mergedInto recognizes ancestry, squash merges, and replayed commits, in that
// order. Empty net changes do not prove integration of unmerged commits.
func (r *Repo) mergedInto(ctx context.Context, branch, base string) (bool, error) {
	if _, err := git(ctx, r.dir, "merge-base", "--is-ancestor", branch, base); err == nil {
		return true, nil
	} else if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
		return false, err
	}
	ancestor, err := git(ctx, r.dir, "merge-base", base, branch)
	if err != nil {
		return false, err
	}
	tree, err := git(ctx, r.dir, "rev-parse", branch+"^{tree}")
	if err != nil {
		return false, err
	}
	ancestorTree, err := git(ctx, r.dir, "rev-parse", ancestor+"^{tree}")
	if err != nil || tree == ancestorTree {
		return false, err
	}
	// This temporary object gives git cherry the branch's combined patch without
	// changing any ref.
	squash, err := git(ctx, r.dir, append(scratchIdentity(), "commit-tree", tree, "-p", ancestor, "-m", "gwt integration check")...)
	if err != nil {
		return false, err
	}
	out, err := git(ctx, r.dir, "cherry", base, squash)
	if err != nil {
		return false, err
	}
	if strings.HasPrefix(out, "- ") {
		return r.mergeLeavesBaseUnchanged(ctx, branch, base)
	}
	// git cherry omits merge commits. Matching their parents' patches cannot
	// prove that edits made in the merge itself reached the base.
	merges, err := git(ctx, r.dir, "rev-list", "--merges", base+".."+branch)
	if err != nil || merges != "" {
		return false, err
	}
	out, err = git(ctx, r.dir, "cherry", base, branch)
	if err != nil || out == "" {
		return false, err
	}
	for line := range strings.SplitSeq(out, "\n") {
		if !strings.HasPrefix(line, "- ") {
			return false, nil
		}
	}
	return r.mergeLeavesBaseUnchanged(ctx, branch, base)
}

// Patch IDs ignore whitespace, which can change code semantics. A patch match
// needs a clean merge that leaves the base's exact contents unchanged too.
func (r *Repo) mergeLeavesBaseUnchanged(ctx context.Context, branch, base string) (bool, error) {
	tree, err := git(ctx, r.dir, "merge-tree", "--write-tree", base, branch)
	if err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 {
			return false, nil // Conflicts leave integration unconfirmed.
		}
		return false, err
	}
	baseTree, err := git(ctx, r.dir, "rev-parse", base+"^{tree}")
	return tree == baseTree, err
}
