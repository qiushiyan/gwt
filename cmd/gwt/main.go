package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/qiushiyan/gwt/internal/config"
	"github.com/qiushiyan/gwt/internal/worktree"
)

const help = `Usage: gwt [create] <branch> [base] [options]
       gwt resolve <branch> [--no-fetch] [--json]
       gwt path [branch] [--json]
       gwt remove <branch|path>... [--force] [--discard-dirty] [--keep-branch]
                                   [--expect-head <commit>] [--json]
       gwt list [--fetch] [--json]
       gwt merged <branch|commit>... [--fetch | --into <rev>] [--json]
       gwt trunk [--fetch] [--json]
       gwt config show [--json]

Create and remove Git worktrees; inspect branch resolution and configuration.
Creation places branches in <worktree_root>/<main-checkout>/<branch> and prints
the absolute path, or one JSON object with --json. Diagnostics go to stderr.
When stderr is a terminal, creation also copies the path to the clipboard
(toclip, else pbcopy); callers that capture stderr leave the clipboard alone.
The binary never changes your shell's directory or installs dependencies; the
zsh gwt function from dotfiles wraps it and performs the cd for --cd.

An existing local branch is checked out as-is; a unique remote branch becomes
a tracking branch. Otherwise create a branch from base (config default: HEAD).
Only an implicit HEAD base asks for confirmation; concrete configured refs do not.

Options (create unless marked otherwise; before or after arguments):
  -n, --non-interactive  Use the configured base without asking
  -y, --yes              Alias for --non-interactive
  --force               remove: delete an unmerged branch, keeping its tip as a recovery ref
  --discard-dirty       remove: snapshot uncommitted work to a recovery ref, then remove
  --keep-branch         remove: remove the checkout only
  --expect-head SHA     remove: refuse unless the one target is still at SHA
  --new                 Create even if a remote branch has the same name
  --no-copy             Skip copying ignored prerequisites from the main checkout
  --no-clipboard        Skip copying the new path to the clipboard
  --no-fetch            create/resolve: use locally cached refs
  --fetch               list/merged/trunk: refresh a stale trunk first
  --into REV            merged: judge against REV instead of the trunk (never fetches)
  --cd                  Enter the new worktree in the calling shell (zsh function only)
  --json                All commands: print one JSON object
  -h, --help            All commands: show help

By default, an absent branch name triggers a bounded refresh of stale remote
refs. Fetch failure warns and uses cached refs. Git authentication is unattended.
resolve prints: local | remote <ref> | ambiguous <refs...> | absent.

Examples:
  gwt fix/login main
  gwt create --non-interactive feat/search
  gwt create feat/search origin/main --json --non-interactive

Configuration (CLI arguments override repository config, then global defaults):
  Global      $XDG_CONFIG_HOME/gwt/config.toml or ~/.config/gwt/config.toml
  Repository  gwt.toml inside the shared Git directory (usually .git/gwt.toml)
  GWT_CONFIG  Use this absolute path instead of the global config file

Keys: base, worktree_root, copy_globs, fetch.max_age, fetch.timeout, recovery.keep.
Arrays replace inherited arrays; copy_globs = [] disables copying.
Duration values use units, e.g. "5m", "8s", "30d". Paths accept ~/ but no shell expansion.
config show reports effective values and sources; JSON durations are seconds.
path prints the intended path without fetching or writing; omit branch for its root.
The trunk is where work lands: the first of origin/HEAD, origin/main,
origin/master, main, master (else the main checkout's branch). It is not the
creation base. Work is "merged" into it by ancestry or by matching squash/rebase
patches; patch matches also require merging to leave the trunk's exact contents
unchanged (Git 2.38+). Verdicts are memoized per branch and trunk commit.
list prints every worktree with dirt and verdict (JSON: trunk, worktrees[] with
path, branch, head, main, current, locked, prunable, dirty, merged, removable,
error).
merged judges named local branches, with or without a checkout, or full commit
ids (a detached checkout's HEAD); --into measures against another revision,
e.g. a base the caller has verified itself. trunk prints
the trunk. They never fetch unless --fetch finds the trunk older than
fetch.max_age; a failed fetch warns and grades against cached refs.
remove deletes checkouts and their local branches, without prompting. A target
is a branch (its registered checkout, or the branch alone when it has none) or
a checkout path, absolute or starting with ./ (detached checkouts too). It
refuses main, current, locked and nesting worktrees, and dirt: uncommitted,
untracked, index-hidden, or an unreadable status. Without --force, a branch it
deletes must be merged into the trunk, which removal refreshes first when stale.
list's removable applies the same rule. Ignored files go with the checkout,
which moves to <worktree_root>/.trash and is deleted in the background.
Whatever removal makes unreachable is kept under refs/wt-trash/<batch>/: the
snapshot --discard-dirty takes (its second parent, <ref>^2, holds the index),
a --force-deleted tip, a detached HEAD. Restore with git branch <name> <ref>.
Refs expire after recovery.keep (0 keeps them). A target whose commit or dirt
changed while removal checked it is refused; stop processes working in a
checkout before removing it. remove --json prints one object per target: ok,
target, path, branch, worktree_removed (true once the checkout is gone, even
on failure), branch_deleted, recovery_ref, and error on failure. After partial
removal, inspect the remaining branch before deleting it directly.
Exit codes: 0 success, 1 operational failure/declined creation, 2 invalid arguments.
Invalid arguments print diagnostics on stderr, including with --json.
`

type options struct {
	command, branch, base                      string
	yes, forceNew, noCopy, noFetch, json, help bool
	force, cd, noClipboard                     bool
	fetch, discardDirty, keepBranch            bool
	into, expectHead                           string   // merged, remove
	branches                                   []string // merged, remove
}

// A small parser keeps the old interspersed flag syntax without a CLI framework.
func parse(args []string) (options, error) {
	o := options{command: "create"}
	if len(args) > 0 {
		switch args[0] {
		case "create", "resolve", "path", "config", "remove", "list", "merged", "trunk":
			o.command, args = args[0], args[1:]
		}
	}
	var positional []string
	flags := true
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if flags {
			// Valued options: `--into REV` or `--into=REV`.
			if name, dst := valued(arg, &o); dst != nil {
				if value, ok := strings.CutPrefix(arg, name+"="); ok {
					*dst = value
				} else if i+1 < len(args) {
					i++
					*dst = args[i]
				}
				if *dst == "" {
					return o, fmt.Errorf("%s needs a revision", name)
				}
				continue
			}
			switch arg {
			case "--":
				flags = false
				continue
			case "-h", "--help":
				o.help = true
				continue
			case "-n", "--non-interactive", "-y", "--yes":
				o.yes = true
				continue
			case "--force":
				o.force = true
				continue
			case "--discard-dirty":
				o.discardDirty = true
				continue
			case "--keep-branch":
				o.keepBranch = true
				continue
			case "--new":
				o.forceNew = true
				continue
			case "--no-copy":
				o.noCopy = true
				continue
			case "--no-clipboard":
				o.noClipboard = true
				continue
			case "--no-fetch":
				o.noFetch = true
				continue
			case "--json":
				o.json = true
				continue
			case "--fetch":
				o.fetch = true
				continue
			case "--cd":
				o.cd = true
				continue
			}
			if strings.HasPrefix(arg, "-") {
				return o, fmt.Errorf("unknown option: %s", arg)
			}
		}
		positional = append(positional, arg)
	}
	if o.help {
		return o, nil
	}
	if (o.force || o.discardDirty || o.keepBranch || o.expectHead != "") && o.command != "remove" {
		return o, fmt.Errorf("--force, --discard-dirty, --keep-branch and --expect-head are only for remove")
	}
	if o.fetch && o.command != "list" && o.command != "merged" && o.command != "trunk" {
		return o, fmt.Errorf("--fetch is only for list, merged, and trunk")
	}
	if o.into != "" && (o.command != "merged" || o.fetch) {
		return o, fmt.Errorf("--into is only for merged, without --fetch: the caller owns that revision's freshness")
	}
	if o.command != "create" && (o.forceNew || o.noCopy || o.noClipboard || o.yes || o.cd || (o.noFetch && o.command != "resolve")) {
		return o, fmt.Errorf("unsupported option for %s", o.command)
	}
	// Only the parent shell can change its own directory. The zsh gwt function
	// strips --cd before calling the binary, so seeing it here means no wrapper.
	if o.cd {
		return o, errors.New(`--cd needs the zsh gwt function from dotfiles; the binary cannot change your shell's directory. Run zshreload, or use: cd "$(gwt create <branch>)"`)
	}
	switch o.command {
	case "config":
		if len(positional) != 1 || positional[0] != "show" {
			return o, fmt.Errorf("use gwt config show [--json]")
		}
	case "path":
		if len(positional) > 1 {
			return o, fmt.Errorf("path accepts at most one branch")
		}
		if len(positional) == 1 {
			o.branch = positional[0]
		}
	case "list", "trunk":
		if len(positional) != 0 {
			return o, fmt.Errorf("%s takes no arguments", o.command)
		}
	case "merged", "remove":
		if len(positional) == 0 {
			return o, fmt.Errorf("%s needs at least one target", o.command)
		}
		if o.expectHead != "" && len(positional) != 1 {
			return o, fmt.Errorf("--expect-head takes exactly one target")
		}
		o.branches = positional
	default:
		if len(positional) == 0 {
			return o, fmt.Errorf("branch name required (see gwt --help)")
		}
		if len(positional) > 2 || (o.command == "resolve" && len(positional) != 1) {
			return o, fmt.Errorf("too many arguments for %s", o.command)
		}
		o.branch = positional[0]
		if len(positional) == 2 {
			o.base = positional[1]
		}
	}
	return o, nil
}

func run(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) int {
	o, err := parse(args)
	if err != nil {
		fmt.Fprintln(stderr, "gwt:", err)
		return 2
	}
	if o.help {
		fmt.Fprint(out, help)
		return 0
	}
	cwd, err := os.Getwd()
	var home string
	var r *worktree.Repo
	if err == nil {
		home, err = os.UserHomeDir()
	}
	if err == nil {
		r, err = worktree.Open(ctx, cwd, home, stderr)
	}
	if o.command == "remove" {
		return remove(ctx, r, err, o, out, stderr)
	}
	if o.command == "config" {
		var cfg config.Config
		if errors.Is(err, worktree.ErrNotRepository) {
			cfg, err = config.Load(home, "")
		} else if err == nil {
			cfg = r.Configuration()
		}
		if err == nil {
			if o.json {
				err = json.NewEncoder(out).Encode(cfg)
			} else {
				err = cfg.Show(out)
			}
		}
		if err != nil {
			fmt.Fprintln(stderr, "gwt:", err)
			return 1
		}
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "gwt:", err)
		return 1
	}
	if o.command == "list" || o.command == "merged" || o.command == "trunk" {
		return inspect(ctx, r, o, out, stderr)
	}
	if o.command == "path" {
		dest, err := r.Path(ctx, o.branch)
		if err == nil {
			if o.json {
				err = json.NewEncoder(out).Encode(struct {
					Path string `json:"path"`
				}{dest})
			} else {
				_, err = fmt.Fprintln(out, dest)
			}
		}
		if err != nil {
			fmt.Fprintln(stderr, "gwt:", err)
			return 1
		}
		return 0
	}
	if o.command == "resolve" {
		v, err := r.Resolve(ctx, o.branch, !o.noFetch)
		if err != nil {
			fmt.Fprintln(stderr, "gwt:", err)
			return 1
		}
		if o.json {
			err = json.NewEncoder(out).Encode(v)
		} else {
			_, err = fmt.Fprintln(out, v.String())
		}
		if err != nil {
			fmt.Fprintln(stderr, "gwt:", err)
			return 1
		}
		return 0
	}
	confirm := func(branch, base string) bool {
		fmt.Fprintf(stderr, "gwt: fork %q from current HEAD (%s)? [y/N] ", branch, base)
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
	result, err := r.Create(ctx, worktree.Options{
		Branch: o.branch, Base: o.base, ForceNew: o.forceNew, NoCopy: o.noCopy,
		NoFetch: o.noFetch, NonInteractive: o.yes, Confirm: confirm,
	})
	if err != nil {
		fmt.Fprintln(stderr, "gwt:", err)
		return 1
	}
	if o.json {
		err = json.NewEncoder(out).Encode(result)
	} else {
		_, err = fmt.Fprintln(out, result.Path)
	}
	if err != nil {
		fmt.Fprintln(stderr, "gwt:", err)
		return 1
	}
	// The worktree exists and its path is printed, so a failed copy only warns.
	if !o.noClipboard && attended(stderr) {
		if err := copyPath(ctx, result.Path); err != nil {
			fmt.Fprintln(stderr, "gwt: path not copied to the clipboard:", err)
		}
	}
	return 0
}

// valued names the options that take a value and where it goes.
func valued(arg string, o *options) (string, *string) {
	for _, v := range []struct {
		name string
		dst  *string
	}{{"--into", &o.into}, {"--expect-head", &o.expectHead}} {
		if arg == v.name || strings.HasPrefix(arg, v.name+"=") {
			return v.name, v.dst
		}
	}
	return "", nil
}

// remove prints one result per target, as JSON lines or prose, and fails if
// any target did.
func remove(ctx context.Context, r *worktree.Repo, openErr error, o options, out, stderr io.Writer) int {
	var results []worktree.Removal
	if openErr == nil {
		results = r.Remove(ctx, o.branches, worktree.RemoveOptions{
			Force: o.force, DiscardDirty: o.discardDirty, KeepBranch: o.keepBranch, ExpectHead: o.expectHead,
		})
	} else {
		for _, target := range o.branches {
			results = append(results, worktree.Removal{Target: target, Error: openErr.Error()})
		}
	}
	code := 0
	for _, x := range results {
		if !x.OK {
			code = 1
			fmt.Fprintf(stderr, "gwt: %s: %s\n", x.Target, x.Error)
		}
		if o.json {
			if err := json.NewEncoder(out).Encode(x); err != nil {
				fmt.Fprintln(stderr, "gwt:", err)
				return 1
			}
			continue
		}
		switch {
		case x.WorktreeRemoved && x.BranchDeleted:
			fmt.Fprintf(out, "Removed worktree %s and branch %s\n", x.Path, x.Branch)
		case x.WorktreeRemoved:
			fmt.Fprintf(out, "Removed worktree %s\n", x.Path)
		case x.BranchDeleted:
			fmt.Fprintf(out, "Deleted branch %s\n", x.Branch)
		}
		if x.RecoveryRef != "" {
			fmt.Fprintf(out, "Kept %s; restore with: git branch <name> %s\n", x.RecoveryRef, x.RecoveryRef)
		}
	}
	return code
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
