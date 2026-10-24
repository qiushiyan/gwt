package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/alecthomas/kong"
	"github.com/qiushiyan/clikit"
	"github.com/qiushiyan/gwt/internal/worktree"
)

// trunkSection is the one account of the trunk and of "merged", on every page
// whose command judges against it.
var trunkSection = clikit.Section{Title: "The trunk and merge verdicts", Body: `The trunk is where work lands: the first of origin/HEAD, origin/main,
origin/master, main and master that exists, else the main checkout's branch. It
is not create's base, which only says where a new branch forks from.

A branch is merged into the trunk when the trunk contains its commits, when one
trunk commit carries its combined change (a squash merge), or when trunk commits
carry each of its commits' changes (a rebase). A change match also needs a clean
merge that leaves the trunk's contents exactly as they are. A branch with merge
commits of its own, or whose lines the trunk edited again later, may stay
unconfirmed. A branch with no commits of its own counts as merged: it holds
nothing to lose.`}

type ListCmd struct {
	Fetch bool `help:"First fetch a remote trunk that no fetch has reached within fetch.max_age, so verdicts see merges made on the remote since."`
	JSON  bool `help:"Print one JSON object instead of the table."`
}

func (ListCmd) Help() string {
	return `Prints every worktree Git has registered, the main checkout first: whether it
holds changes, and whether its branch is merged into the trunk. Text: the trunk,
then one row per worktree: » marks the one you are in and * one with changes,
then its branch, path, and tags (main, merged, locked, prunable). A worktree
whose status cannot be read counts as having changes, and why is reported on
stderr as "gwt: warning: <path>: <reason>"; the listing still exits 0.

Without --fetch it reads cached remote refs and contacts no remote. A failed
fetch warns on stderr and the verdicts use the cached refs.`
}

func (ListCmd) HelpSections() []clikit.Section {
	return []clikit.Section{{Title: "JSON", Body: `One object: trunk, as gwt trunk --json prints it, and worktrees, one object each:
  path       the checkout's absolute path
  branch     its branch; absent when detached
  head       its commit; absent when unknown
  main       true for the main checkout
  current    true for the checkout the caller is in
  locked     locked with git worktree lock
  prunable   registered, but its directory is gone
  dirty      uncommitted, untracked, or index-hidden changes, or a status that
             could not be read
  merged     whether its branch is merged into the trunk; null when detached,
             when its status could not be read, or when the check failed
  removable  whether gwt remove <path> without flags would remove it
  error      why its status or verdict could not be read; absent otherwise`}, trunkSection}
}

func (c *ListCmd) Run(k *kong.Context, ctx context.Context, r *worktree.Repo) error {
	l, err := r.List(ctx, c.Fetch)
	if err != nil {
		return err
	}
	if c.JSON {
		return json.NewEncoder(k.Stdout).Encode(l)
	}
	fmt.Fprintln(k.Stdout, "trunk", trunkLine(l.Trunk))
	w := tabwriter.NewWriter(k.Stdout, 0, 0, 2, ' ', 0)
	for _, t := range l.Worktrees {
		mark, dirty := " ", " "
		if t.Current {
			mark = "»"
		}
		if t.Dirty {
			dirty = "*"
		}
		branch := t.Branch
		if branch == "" {
			branch = "(detached)"
		}
		var tags []string
		for _, tag := range []struct {
			on   bool
			name string
		}{{t.Main, "main"}, {t.Merged != nil && *t.Merged, "merged"}, {t.Locked, "locked"}, {t.Prunable, "prunable"}} {
			if tag.on {
				tags = append(tags, tag.name)
			}
		}
		fmt.Fprintf(w, "%s%s %s\t%s\t%s\n", mark, dirty, branch, t.Path, strings.Join(tags, " "))
		if t.Error != "" {
			fmt.Fprintf(k.Stderr, "gwt: warning: %s: %s\n", t.Path, t.Error)
		}
	}
	return w.Flush()
}

type MergedCmd struct {
	Targets []string `arg:"" name:"target" help:"Local branches, with or without a checkout, or full commit ids where no branch has the name."`
	Fetch   bool     `xor:"freshness" help:"First fetch a remote trunk that no fetch has reached within fetch.max_age, so a merge made on the remote since counts."`
	Into    rev      `xor:"freshness" placeholder:"REV" help:"Judge against REV instead of the trunk, such as a base you verified against the remote yourself; nothing is fetched."`
	JSON    bool     `help:"Print one JSON object instead of lines."`
}

func (MergedCmd) Help() string {
	return `Prints whether each target's work is in the trunk: one line per target,
"merged<TAB><target>" or "unmerged<TAB><target>", in order. A full commit id,
such as a detached checkout's HEAD, is judged as that commit. A target that
cannot be judged is reported on stderr as "gwt: error: <target>: <reason>" (in
JSON, by its error field), the rest are still judged, and the command exits 1.

Without --fetch it reads cached remote refs and contacts no remote.`
}

func (MergedCmd) HelpSections() []clikit.Section {
	return []clikit.Section{{Title: "JSON", Body: `One object, printed even when a target fails:
  trunk     what the verdicts were measured against, as gwt trunk --json prints
            it; with --into, its name and commit are that revision
  branches  one object per target, in order:
    branch  the target as given
    commit  the commit judged; absent when the target was not found
    merged  whether it is merged; null when not found or when the check failed
    error   why it could not be judged; absent otherwise`}, trunkSection}
}

func (c *MergedCmd) Run(k *kong.Context, ctx context.Context, r *worktree.Repo) error {
	v, err := r.Merged(ctx, c.Targets, c.Fetch, string(c.Into))
	if err != nil {
		return err
	}
	failed := false
	for _, b := range v.Branches {
		failed = failed || b.Error != ""
	}
	if c.JSON {
		err = json.NewEncoder(k.Stdout).Encode(v)
	} else {
		for _, b := range v.Branches {
			switch {
			case b.Error != "":
				fmt.Fprintf(k.Stderr, "gwt: error: %s: %s\n", b.Branch, b.Error)
			case *b.Merged:
				fmt.Fprintf(k.Stdout, "merged\t%s\n", b.Branch)
			default:
				fmt.Fprintf(k.Stdout, "unmerged\t%s\n", b.Branch)
			}
		}
	}
	if err != nil {
		return err
	}
	if failed {
		return clikit.Exit(1)
	}
	return nil
}

type TrunkCmd struct {
	Fetch bool `help:"First fetch the trunk if it is a remote branch that no fetch has reached within fetch.max_age."`
	JSON  bool `help:"Print one JSON object instead of the line."`
}

func (TrunkCmd) Help() string {
	return `Prints the trunk's name and commit, marked "(stale; --fetch refreshes)" when it
is a remote branch no fetch has reached within fetch.max_age, "(fetched)" after
--fetch refreshed it, or "(fetch failed; cached)" when that fetch failed, which
also warns on stderr.`
}

func (TrunkCmd) HelpSections() []clikit.Section {
	return []clikit.Section{{Title: "JSON", Body: `One object:
  name         origin/<branch> for a remote trunk, else a local branch
  commit       the commit verdicts are measured against
  remote       the remote --fetch refreshes; absent for a local trunk
  stale        true when no fetch has reached a remote trunk within fetch.max_age
  fetched      true when --fetch fetched it; absent otherwise
  fetch_error  why --fetch failed, leaving the cached trunk; absent otherwise`}, trunkSection}
}

func (c *TrunkCmd) Run(k *kong.Context, ctx context.Context, r *worktree.Repo) error {
	t, err := r.Trunk(ctx, c.Fetch)
	if err != nil {
		return err
	}
	if c.JSON {
		return json.NewEncoder(k.Stdout).Encode(t)
	}
	_, err = fmt.Fprintln(k.Stdout, trunkLine(t))
	return err
}

func trunkLine(t worktree.Trunk) string {
	line := t.Name + " " + t.Commit[:min(12, len(t.Commit))]
	switch {
	case t.FetchError != "":
		line += " (fetch failed; cached)"
	case t.Fetched:
		line += " (fetched)"
	case t.Stale:
		line += " (stale; --fetch refreshes)"
	}
	return line
}
