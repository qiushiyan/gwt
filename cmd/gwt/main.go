// gwt places Git worktrees and decides when one may go. This package is the
// command line only: each command's flags become one call into
// internal/worktree, and its result is rendered here. clikit
// (~/dev/clikit) owns how help renders and how an argv error reads.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/qiushiyan/clikit"
	"github.com/qiushiyan/gwt/internal/config"
	"github.com/qiushiyan/gwt/internal/worktree"
)

var app = clikit.App{
	Name:        "gwt",
	Description: "gwt creates and removes Git worktrees under one placement policy, and judges whether branches are merged into the trunk.",
	New:         func() any { return &CLI{} },
	Options: []kong.Option{
		// main replaces this with one that ends on an interrupt.
		kong.BindTo(context.Background(), (*context.Context)(nil)),
		kong.BindToProvider(openRepo),
		kong.BindToProvider(watching),
	},
	ExitCodes: map[int]string{
		1: "failed, or a creation was declined; remove and merged also exit 1 when any target failed, after printing every target's result",
	},
}

func main() {
	// Git runs in its own process group, so an interrupt reaches it only
	// through this context.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	a := app
	a.Options = append(slices.Clone(app.Options), kong.BindTo(ctx, (*context.Context)(nil)))
	code := a.Run(dispatch(os.Args[1:]))
	stop()
	os.Exit(code)
}

type CLI struct {
	Create  CreateCmd  `cmd:"" default:"withargs" help:"Check out a branch in its own worktree, creating the branch if needed, and print the path."`
	Resolve ResolveCmd `cmd:"" help:"Say how create would obtain a branch: local, remote, ambiguous or absent."`
	Path    PathCmd    `cmd:"" help:"Print where create would place a branch, without creating anything."`
	Remove  RemoveCmd  `cmd:"" help:"Remove checkouts and their branches, keeping a recovery ref for anything discarded."`
	List    ListCmd    `cmd:"" help:"List every worktree with its changes and whether its branch is merged into the trunk."`
	Merged  MergedCmd  `cmd:"" help:"Judge whether branches or commits are merged into the trunk."`
	Trunk   TrunkCmd   `cmd:"" help:"Print the trunk: the branch work lands on, which merge verdicts measure against."`
	Config  ConfigCmd  `cmd:"" help:"Inspect gwt's configuration."`
}

func (CLI) Help() string {
	return `A branch's worktree lives at <worktree_root>/<name of the main checkout>/<branch>.
Git owns repository state; gwt owns where checkouts go, what is copied into them,
and when one may be removed. Create and remove worktrees through gwt rather than
git worktree, so every checkout follows the same placement and removal rules.

The first word is the command; anything else there is a branch for create, so
"gwt feat/x main" creates feat/x from main. To create a branch named like a
command, write "gwt create list".

stdout carries only the result: a path, one line per item, or with --json one JSON
document (remove prints one JSON object per line, one per target). Progress,
warnings and the confirmation prompt go to stderr. gwt never changes the caller's
directory; agents use the printed path as their working directory.`
}

// dispatch keeps the first word deciding the command, as it always has: a
// first word that names no command is a branch for create. Kong alone would
// let a flag of create's carry the parse on to a later command name, so
// "gwt -n remove x" would remove x instead of creating the branch remove.
func dispatch(args []string) []string {
	if len(args) == 0 || slices.Contains([]string{"-h", "--help", "help"}, args[0]) {
		return args
	}
	if k, err := kong.New(&CLI{}); err == nil {
		for _, c := range k.Model.Children {
			if c.Name == args[0] || slices.Contains(c.Aliases, args[0]) {
				return args
			}
		}
	}
	return append([]string{"create"}, args...)
}

// openRepo opens the repository the caller stands in, with its configuration.
// Commands that must refuse their arguments first, or report a failed open in
// their own output, call it in Run instead of asking Kong for a *Repo.
func openRepo(ctx context.Context, k *kong.Context) (*worktree.Repo, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return worktree.Open(ctx, cwd, home, k.Stderr)
}

// rev is a revision a flag names. An empty one names nothing, so it is refused
// with the flag instead of reading as the flag's absence.
type rev string

func (r *rev) Decode(ctx *kong.DecodeContext) error {
	t, err := ctx.Scan.PopValue("revision")
	if err != nil {
		return err
	}
	if t.String() == "" {
		return errors.New("an empty revision names nothing")
	}
	*r = rev(t.String())
	return nil
}

type ConfigCmd struct {
	JSON bool `help:"Print one JSON object instead of text."`

	Show ConfigShowCmd `cmd:"" help:"Print the effective configuration and the file each value came from."`
}

type ConfigShowCmd struct{}

func (ConfigShowCmd) Help() string {
	return `Works outside a repository too, where it shows the global layer only.`
}

func (ConfigShowCmd) HelpSections() []clikit.Section {
	return []clikit.Section{{Title: "Configuration", Body: `Global defaults live in $XDG_CONFIG_HOME/gwt/config.toml (normally
~/.config/gwt/config.toml); GWT_CONFIG names another file instead, which must
exist. gwt.toml in the shared Git directory (usually .git/gwt.toml) overrides them
for one repository and all its worktrees. Command-line arguments win over both.

  base           where create forks a new branch: HEAD (the caller's current
                 commit, which create asks about first unless --non-interactive)
                 or a ref such as origin/main
  worktree_root  the directory checkouts go under: absolute, or ~/...
  copy_globs     basename patterns of ignored files create copies from the main
                 checkout; an array replaces the inherited one, and [] copies nothing
  fetch.max_age  how old the last fetch may be before create, or --fetch, fetches
                 again, e.g. "5m"
  fetch.timeout  the deadline for that fetch, e.g. "8s"
  recovery.keep  how long remove's recovery refs last, e.g. "30d"; "0" keeps them

An invalid value or an unknown key fails every command that reads the file.`},
		{Title: "JSON", Body: `One object: base, worktree_root, copy_globs, fetch (max_age, timeout) and
recovery (keep) as above, durations in seconds; sources, the file each key came
from or "builtin"; files, the global path and, inside a repository, the
repository path.`}}
}

func (ConfigShowCmd) Run(k *kong.Context, ctx context.Context, cli *CLI) error {
	var cfg config.Config
	r, err := openRepo(ctx, k)
	switch {
	case errors.Is(err, worktree.ErrNotRepository):
		var home string
		if home, err = os.UserHomeDir(); err == nil {
			cfg, err = config.Load(home, "")
		}
	case err == nil:
		cfg = r.Configuration()
	}
	if err != nil {
		return err
	}
	if cli.Config.JSON {
		return json.NewEncoder(k.Stdout).Encode(cfg)
	}
	return cfg.Show(k.Stdout)
}
