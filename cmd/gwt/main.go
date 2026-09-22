package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/qiushiyan/gwt/internal/worktree"
)

const help = `Usage: gwt [create] <branch> [base] [options]
       gwt resolve <branch> [--no-fetch] [--json]

Place a branch in ~/dev/.worktrees/<main-checkout>/<branch>.
Print only the absolute path on stdout; diagnostics go to stderr.
The binary never changes your shell's directory or installs dependencies.

An existing local branch is checked out as-is; a unique remote branch becomes
a tracking branch. Otherwise create a branch from base (default: current HEAD).
Without an explicit base, creating a branch asks for confirmation.

Options (before or after arguments):
  -n, --non-interactive  Use current HEAD without asking when base is omitted
  -y, --yes              Alias for --non-interactive
  --new                 Create even if a remote branch has the same name
  --no-copy             Skip copying ignored prerequisites from the main checkout
  --no-fetch            Resolve only against locally cached refs
  --json                Print one JSON object instead of the path/verdict
  -h, --help            Show help

By default, an absent branch name triggers a bounded refresh of stale remote
refs. Fetch failure warns and uses cached refs. Git authentication is unattended.
resolve prints: local | remote <ref> | ambiguous <refs...> | absent.

Examples:
  gwt fix/login main
  gwt create --non-interactive feat/search
  gwt create feat/search origin/main --json --non-interactive
  gwtcd fix/login main                 # optional zsh helper: create and cd

Environment:
  WORKTREE_COPY_GLOBS  Basename globs separated by whitespace; "off" disables
                      Default: .env* .npmrc scripts.local .duet docs.local
  WT_BASE_MAX_AGE_MIN  Fetch freshness in minutes (default 5)
  WT_FETCH_TIMEOUT     Fetch deadline in seconds (default 8)
`

type options struct {
	command, branch, base                      string
	yes, forceNew, noCopy, noFetch, json, help bool
}

// A small parser keeps the old interspersed flag syntax without a CLI framework.
func parse(args []string) (options, error) {
	o := options{command: "create"}
	if len(args) > 0 && (args[0] == "create" || args[0] == "resolve") {
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
	if len(positional) == 0 {
		return o, fmt.Errorf("branch name required (see gwt --help)")
	}
	if len(positional) > 2 || (o.command == "resolve" && len(positional) != 1) {
		return o, fmt.Errorf("too many arguments for %s", o.command)
	}
	if o.command == "resolve" && (o.forceNew || o.noCopy || o.yes) {
		return o, fmt.Errorf("resolve accepts only --no-fetch and --json")
	}
	o.branch = positional[0]
	if len(positional) == 2 {
		o.base = positional[1]
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
	if err != nil {
		fmt.Fprintln(stderr, "gwt:", err)
		return 1
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, "gwt:", err)
		return 1
	}
	r, err := worktree.Open(ctx, cwd, home, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "gwt:", err)
		return 1
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
