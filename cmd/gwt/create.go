package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/qiushiyan/clikit"
	"github.com/qiushiyan/gwt/internal/worktree"
)

type CreateCmd struct {
	Branch string `arg:"" help:"The branch to check out; created when no local or remote branch has the name."`
	Base   string `arg:"" optional:"" help:"Where a new branch forks from; unused when a local branch or one remote branch has the name. Default: the configured base."`

	NonInteractive bool `short:"n" help:"Fork a new branch from the configured base without asking, even when that base is HEAD. Agents and scripts pass this."`
	New            bool `help:"Create a new branch even when a remote branch has the name; an existing local branch is still checked out as it is."`
	NoCopy         bool `help:"Copy no ignored files from the main checkout into the new one."`
	NoClipboard    bool `help:"Leave the clipboard alone at a terminal."`
	NoFetch        bool `help:"Decide from cached remote refs, without fetching."`
	CD             bool `name:"cd" help:"Enter the new checkout in the calling shell. Only the zsh gwt function from dotfiles can, so the binary refuses this flag."`
	JSON           bool `help:"Print one JSON object instead of the path."`
}

func (CreateCmd) Help() string {
	return `Creates a checkout of <branch> and prints its absolute path. The branch is:
  - an existing local branch, checked out as it is, with <base> unused;
  - else a new local branch tracking the one remote branch of that name; when
    several remotes have it, gwt refuses and prints the command that picks one;
  - else a new branch, without upstream, forked from <base>. Without <base> it
    forks from the configured base, and when that base is HEAD it asks on stderr
    before forking from the caller's current commit, unless --non-interactive.

Before treating a name as absent, gwt fetches every remote when no fetch has
reached the repository within fetch.max_age, bounded by fetch.timeout. A failed
fetch warns and uses the cached refs.

The slot must be absent or an empty directory; anything else, a checkout an
earlier create made included, is refused, and nothing is deleted. Ignored files in the main checkout whose names match
copy_globs (gwt config show lists them) are copied in. A failed copy is only a
warning: the checkout and its path stand, so do not retry the creation.

When stderr is a terminal the path also goes to the clipboard (toclip, else
pbcopy). A caller that captures stderr, as agents do, leaves the clipboard alone.`
}

func (CreateCmd) HelpSections() []clikit.Section {
	return []clikit.Section{
		{Title: "JSON", Body: `One object; on failure stdout is empty and stderr says why:
  path      the checkout's absolute path
  branch    the branch checked out
  action    created (a new branch), local (an existing local branch), or remote
            (a new branch tracking the remote one)
  base      what a created branch forked from, as named (HEAD reads as its
            branch, such as main); absent otherwise
  upstream  the remote branch tracked, such as origin/x; absent unless remote
  copied    how many ignored files were copied in
  warnings  one string per file that could not be copied; absent when none failed`},
		{Title: "Examples", Body: `gwt fix/login main               # fork from main, no question asked
gwt feat/search                  # fork from HEAD after confirming at a terminal
gwt create -n feat/agent-work    # no question asked: for scripts and agents
gwt create -n feat/x main --json`},
	}
}

func (c *CreateCmd) Run(k *kong.Context, ctx context.Context, in io.Reader, watched attended) error {
	// Only the parent shell can change its own directory. The zsh gwt function
	// strips --cd before calling the binary, so seeing it here means no wrapper.
	if c.CD {
		return clikit.Usage(errors.New(`--cd needs the zsh gwt function from dotfiles; the binary cannot change your shell's directory. Run zshreload, or use: cd "$(gwt create <branch>)"`))
	}
	r, err := openRepo(ctx, k)
	if err != nil {
		return err
	}
	confirm := func(branch, base string) bool {
		fmt.Fprintf(k.Stderr, "gwt: fork %q from current HEAD (%s)? [y/N] ", branch, base)
		answer := make(chan bool, 1)
		go func() {
			line, _ := bufio.NewReader(in).ReadString('\n')
			answer <- strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y")
		}()
		select {
		case yes := <-answer:
			return yes
		case <-ctx.Done():
			return false
		}
	}
	result, err := r.Create(ctx, worktree.CreateOptions{
		Branch: c.Branch, Base: c.Base, ForceNew: c.New, NoCopy: c.NoCopy,
		NoFetch: c.NoFetch, NonInteractive: c.NonInteractive, Confirm: confirm,
	})
	if err != nil {
		return err
	}
	if c.JSON {
		err = json.NewEncoder(k.Stdout).Encode(result)
	} else {
		_, err = fmt.Fprintln(k.Stdout, result.Path)
	}
	if err != nil {
		return err
	}
	// The worktree exists and its path is printed, so a failed copy only warns.
	if !c.NoClipboard && bool(watched) {
		if err := copyPath(ctx, result.Path); err != nil {
			fmt.Fprintln(k.Stderr, "gwt: path not copied to the clipboard:", err)
		}
	}
	return nil
}

type ResolveCmd struct {
	Branch  string `arg:"" help:"The branch name to look up."`
	NoFetch bool   `help:"Decide from cached remote refs, without fetching: a read-only probe."`
	JSON    bool   `help:"Print one JSON object instead of the line."`
}

func (ResolveCmd) Help() string {
	return `Prints one line:
  local                      a local branch has the name
  remote <ref>               one remote branch has it, such as refs/remotes/origin/x
  ambiguous <ref> <ref>...   several remotes have it
  absent                     none has it; create would fork a new branch
An absent name triggers the fetch create would make, so without --no-fetch this
may contact remotes.`
}

func (ResolveCmd) HelpSections() []clikit.Section {
	return []clikit.Section{{Title: "JSON", Body: `One object: kind, one of local, remote, ambiguous or absent; refs, the remote
refs that have the name, absent for local and absent.`}}
}

func (c *ResolveCmd) Run(k *kong.Context, ctx context.Context, r *worktree.Repo) error {
	v, err := r.Resolve(ctx, c.Branch, !c.NoFetch)
	if err != nil {
		return err
	}
	if c.JSON {
		return json.NewEncoder(k.Stdout).Encode(v)
	}
	_, err = fmt.Fprintln(k.Stdout, v.String())
	return err
}

type PathCmd struct {
	Branch string `arg:"" optional:"" help:"The branch whose path to print; without it, <worktree_root>/<name of the main checkout>, which this repository's worktrees go under."`
	JSON   bool   `help:"Print one JSON object instead of the path."`
}

func (PathCmd) Help() string {
	return `Prints the absolute path create would place the branch at, without fetching,
creating anything, or checking that the slot is free. The path follows the
configuration, so it can differ from where an existing checkout of the branch
lives; gwt list shows those.`
}

func (PathCmd) HelpSections() []clikit.Section {
	return []clikit.Section{{Title: "JSON", Body: `One object: path, the absolute path.`}}
}

// pathJSON is what path --json prints.
type pathJSON struct {
	Path string `json:"path"`
}

func (c *PathCmd) Run(k *kong.Context, ctx context.Context, r *worktree.Repo) error {
	dest, err := r.Path(ctx, c.Branch)
	if err != nil {
		return err
	}
	if c.JSON {
		return json.NewEncoder(k.Stdout).Encode(pathJSON{dest})
	}
	_, err = fmt.Fprintln(k.Stdout, dest)
	return err
}
