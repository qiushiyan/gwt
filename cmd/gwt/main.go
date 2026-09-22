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
       gwt remove <branch> [--force] [--json]
       gwt config show [--json]

Create and remove Git worktrees; inspect branch resolution and configuration.
Creation places branches in <worktree_root>/<main-checkout>/<branch> and prints
the absolute path, or one JSON object with --json. Diagnostics go to stderr.
The binary never changes your shell's directory or installs dependencies.

An existing local branch is checked out as-is; a unique remote branch becomes
a tracking branch. Otherwise create a branch from base (config default: HEAD).
Only an implicit HEAD base asks for confirmation; concrete configured refs do not.

Options (create unless marked otherwise; before or after arguments):
  -n, --non-interactive  Use the configured base without asking
  -y, --yes              Alias for --non-interactive
  --force               remove: allow an unmerged branch; dirty/locked trees still fail
  --new                 Create even if a remote branch has the same name
  --no-copy             Skip copying ignored prerequisites from the main checkout
  --no-fetch            create/resolve: use locally cached refs
  --json                All commands: print one JSON object
  -h, --help            All commands: show help

By default, an absent branch name triggers a bounded refresh of stale remote
refs. Fetch failure warns and uses cached refs. Git authentication is unattended.
resolve prints: local | remote <ref> | ambiguous <refs...> | absent.

Examples:
  gwt fix/login main
  gwt create --non-interactive feat/search
  gwt create feat/search origin/main --json --non-interactive
  gwtcd fix/login main                 # optional zsh helper: create and cd

Configuration (CLI arguments override repository config, then global defaults):
  Global      $XDG_CONFIG_HOME/gwt/config.toml or ~/.config/gwt/config.toml
  Repository  gwt.toml inside the shared Git directory (usually .git/gwt.toml)
  GWT_CONFIG  Use this absolute path instead of the global config file

Keys: base, worktree_root, copy_globs, fetch.max_age, fetch.timeout.
Arrays replace inherited arrays; copy_globs = [] disables copying.
Duration values use units, e.g. "5m" and "8s". Paths accept ~/ but no shell expansion.
config show reports effective values and sources; JSON durations are seconds.
path prints the intended path without fetching or writing; omit branch for its root.
remove deletes the registered checkout and its local branch, without prompting.
It protects main/current worktrees and refuses dirt. Without --force, the branch
must be an ancestor of the configured base and pass git branch -d. It does not
fetch or recognize squash merges. Ignored files are removed with the checkout.
remove --json reports ok, worktree_removed, branch_deleted, and error on operational
failure. After partial removal, inspect the remaining branch before deleting it
directly; retrying remove cannot find a checkout that was already removed.
Exit codes: 0 success, 1 operational failure/declined creation, 2 invalid arguments.
Invalid arguments print diagnostics on stderr, including with --json.
WORKTREE_COPY_GLOBS and WT_* environment settings are no longer used by gwt.
`

type options struct {
	command, branch, base                      string
	yes, forceNew, noCopy, noFetch, json, help bool
	force                                      bool
}

// A small parser keeps the old interspersed flag syntax without a CLI framework.
func parse(args []string) (options, error) {
	o := options{command: "create"}
	if len(args) > 0 && (args[0] == "create" || args[0] == "resolve" || args[0] == "path" || args[0] == "config" || args[0] == "remove") {
		o.command, args = args[0], args[1:]
	}
	var positional []string
	flags := true
	for _, arg := range args {
		if flags {
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
			case "--new":
				o.forceNew = true
				continue
			case "--no-copy":
				o.noCopy = true
				continue
			case "--no-fetch":
				o.noFetch = true
				continue
			case "--json":
				o.json = true
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
	if o.force && o.command != "remove" {
		return o, fmt.Errorf("--force is only for remove")
	}
	if o.command != "create" && (o.forceNew || o.noCopy || o.yes || (o.noFetch && o.command != "resolve")) {
		return o, fmt.Errorf("unsupported option for %s", o.command)
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
	default:
		if len(positional) == 0 {
			return o, fmt.Errorf("branch name required (see gwt --help)")
		}
		if len(positional) > 2 || ((o.command == "resolve" || o.command == "remove") && len(positional) != 1) {
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
		result := worktree.Removal{Branch: o.branch}
		if err == nil {
			result, err = r.Remove(ctx, o.branch, o.force)
		}
		if err != nil {
			result.Error = err.Error()
		}
		if o.json {
			if outErr := json.NewEncoder(out).Encode(result); outErr != nil {
				fmt.Fprintln(stderr, "gwt:", outErr)
				return 1
			}
		} else if err == nil {
			fmt.Fprintf(out, "Removed worktree %s and branch %s\n", result.Path, result.Branch)
		}
		if err != nil {
			fmt.Fprintln(stderr, "gwt:", err)
			return 1
		}
		return 0
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
	return 0
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
