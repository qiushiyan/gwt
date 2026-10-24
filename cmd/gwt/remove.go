package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/alecthomas/kong"
	"github.com/qiushiyan/clikit"
	"github.com/qiushiyan/gwt/internal/worktree"
)

type RemoveCmd struct {
	Targets []string `arg:"" name:"target" help:"A branch (its registered checkout, or the branch alone when it has none) or a checkout path, absolute or starting with ./, which reaches detached checkouts."`

	Force        bool `help:"Delete a branch that is not merged into the trunk; its tip is kept as a recovery ref."`
	DiscardDirty bool `help:"Remove a checkout that holds changes, after snapshotting them to a recovery ref."`
	KeepBranch   bool `help:"Remove only the checkout and keep its branch, so no merge verdict is needed."`
	ExpectHead   rev  `placeholder:"COMMIT" help:"Refuse unless the one target is still at COMMIT. Pass the commit you judged, so work committed since cannot be lost."`
	JSON         bool `help:"Print one JSON object per target, one per line, instead of text."`
}

func (RemoveCmd) Help() string {
	return `Runs without prompting. Several targets form one batch, and a failed target
does not stop the others.

Always refused: the main checkout, the one you are in, a locked one, and one that
holds another worktree. Refused unless --discard-dirty: uncommitted, untracked or
index-hidden changes, or a status that cannot be read. Refused unless --force or
--keep-branch: a branch not merged into the trunk, judged after fetching a remote
trunk that no fetch has reached within fetch.max_age, so a pull request merged
minutes ago counts. gwt list's removable applies the same rules. A target whose
commit or changes moved while gwt checked it is refused; stop any process still
working in a checkout before removing it.

Nothing is discarded without a recovery ref under refs/wt-trash/<batch>/: the
snapshot --discard-dirty takes (shaped like a stash: first parent HEAD, second a
commit of the index, ignored files left out), the tip --force deletes, or a
removed detached checkout's HEAD. Restore one with git branch <name> <ref>. Refs
older than recovery.keep expire on each removal. Ignored files go with the
checkout, which moves to <worktree_root>/.trash and is deleted in the background.

Text: what was removed and any ref kept, on stdout; each failed target on stderr
as "gwt: error: <target>: <reason>". The command exits 1 when any target failed.`
}

func (RemoveCmd) HelpSections() []clikit.Section {
	return []clikit.Section{{Title: "JSON", Body: `One object per line, one per target, in order, success or not; a failure is
also reported on stderr:
  ok                true when everything asked for this target was done
  target            the target as given
  path              the checkout; absent for a branch without one
  branch            the branch; "" for a detached checkout
  worktree_removed  true once the checkout is gone, even when a later step failed
  branch_deleted    true when the branch was deleted
  recovery_ref      the ref keeping what was discarded; absent when nothing was
  error             why the target failed; absent on success
With worktree_removed true, branch_deleted false and an error, the checkout is
gone and its branch remains: inspect the branch before deleting it by hand.`}, trunkSection}
}

func (c *RemoveCmd) Run(k *kong.Context, ctx context.Context) error {
	if c.ExpectHead != "" && len(c.Targets) != 1 {
		return clikit.Usage(errors.New("--expect-head takes exactly one target"))
	}
	// A repository that cannot be opened fails every target in the output
	// callers read, rather than the command alone.
	var results []worktree.Removal
	if r, err := openRepo(ctx, k); err == nil {
		results = r.Remove(ctx, c.Targets, worktree.RemoveOptions{
			Force: c.Force, DiscardDirty: c.DiscardDirty, KeepBranch: c.KeepBranch, ExpectHead: string(c.ExpectHead),
		})
	} else {
		for _, target := range c.Targets {
			results = append(results, worktree.Removal{Target: target, Error: err.Error()})
		}
	}
	failed := false
	for _, x := range results {
		if !x.OK {
			failed = true
			fmt.Fprintf(k.Stderr, "gwt: error: %s: %s\n", x.Target, x.Error)
		}
		if c.JSON {
			if err := json.NewEncoder(k.Stdout).Encode(x); err != nil {
				return err
			}
			continue
		}
		switch {
		case x.WorktreeRemoved && x.BranchDeleted:
			fmt.Fprintf(k.Stdout, "Removed worktree %s and branch %s\n", x.Path, x.Branch)
		case x.WorktreeRemoved:
			fmt.Fprintf(k.Stdout, "Removed worktree %s\n", x.Path)
		case x.BranchDeleted:
			fmt.Fprintf(k.Stdout, "Deleted branch %s\n", x.Branch)
		}
		if x.RecoveryRef != "" {
			fmt.Fprintf(k.Stdout, "Kept %s; restore with: git branch <name> %s\n", x.RecoveryRef, x.RecoveryRef)
		}
	}
	if failed {
		return clikit.Exit(1)
	}
	return nil
}
