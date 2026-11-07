# gwt

A personal worktree CLI. `gwt` resolves a branch, places it under
`~/dev/.worktrees/<main-checkout>/<branch>` by default, seeds ignored prerequisites from
the main checkout, and prints its absolute path. Git owns repository state;
the CLI owns the placement policy.

## Install and use

Requires Go 1.27+, Git 2.38+, and `cp` on macOS or Linux.

```sh
make check
make install                    # installs to ~/.local/bin; keep that directory on PATH
gwt fix/login main              # explicit base, no confirmation
gwt feat/search                 # confirm forking from current HEAD
gwt --cd feat/search            # same, then cd into it (zsh function, default off)
gwt feat/search --no-clipboard  # leave the clipboard alone
gwt create -n feat/agent-work    # unattended; stdout is only the path
gwt create -n feat/agent-json --json
gwt --help                      # the commands; gwt <command> --help for one
```

The binary does not change the caller's directory: no child process can. The
dotfiles zsh `gwt` function wraps the binary and, only with `--cd`, captures the
printed path and changes the parent shell's directory; every other invocation is
forwarded untouched. `gwtcd` is an alias for `gwt --cd`. Without the function,
the binary refuses `--cd` before creating anything and says so. Agents use the
returned path as their working directory. Run `zshreload` once so an existing
shell picks up the function.

When stderr is a terminal, creation also copies the path to the clipboard
through the dotfiles `toclip`, which reaches the laptop's clipboard from an SSH
session, or else `pbcopy`. A caller that captures stderr, as agents do, leaves
the clipboard alone; `--no-clipboard` skips the copy at a terminal. A failed
copy only warns: the worktree and its printed path stand.

Command names are reserved in the first position. To create a branch named
`remove`, for example, use `gwt create remove`. Each command's `--help` is its
reference: what it does, its flags, its text output, and every key of its JSON.

## Configuration

Persistent preferences live in TOML. Global defaults come from
`$XDG_CONFIG_HOME/gwt/config.toml` (normally `~/.config/gwt/config.toml`).
Optional `gwt.toml` in the shared Git directory, normally `.git/gwt.toml`,
overrides them for one repository and all its worktrees. This keeps local
preferences outside checked-out branches. Explicit CLI arguments win.

```toml
base = "HEAD"
worktree_root = "~/dev/.worktrees"
copy_globs = [".env*", ".npmrc", "scripts.local", ".duet", "docs.local"]

[fetch]
max_age = "5m"
timeout = "8s"

[recovery]
keep = "30d"                    # removal's recovery refs; "0" keeps them
```

These are also the built-in defaults when no file exists. During creation,
`base` affects only new branches; `HEAD` means the caller's current commit. A concrete ref such as
`origin/main` avoids interactive confirmation. Shell, tmux, and agents use the
same defaults. A brief's explicit base still overrides them.

Arrays replace inherited arrays; `copy_globs = []` disables seeding. Patterns
match basenames at any depth, including ignored directories. Paths must be
absolute or start with `~/`; shell expressions and environment variables are
not expanded. Invalid settings and unknown keys fail before creation.

```sh
gwt config show                 # effective values and the file owning each
gwt config show --json          # durations are numeric seconds
gwt path feat/search            # intended path, without fetching or creating
```

Environment variables select the configuration location: `GWT_CONFIG` replaces
the global file and must name an existing file; `XDG_CONFIG_HOME` sets the usual
config directory. One-off behavior belongs in flags such as `--no-copy` and
`--no-fetch`. `WORKTREE_COPY_GLOBS` and `WT_*` do not configure the binary.

## Placement rules

Local branches win over remote names. A unique remote branch becomes a local
tracking branch; multiple matching remotes fail with a disambiguation command.
Only an absent name forks from the base. `--new` ignores remote matches but
never resets an existing local branch. New branches do not inherit tracking.

An absent name triggers a fetch of all remotes if `FETCH_HEAD` is missing,
empty, or older than the configured freshness window (five minutes by default).
The fetch deadline defaults to eight seconds and includes child processes. Failure warns and uses cached refs, matching the
existing workflow. `--no-fetch` selects offline operation. Git authentication
is unattended; custom SSH commands and Git hooks remain user configuration.
The freshness window is repository-wide, so it does not prove every remote
was fetched recently.

Paths derive from the main checkout even when invoked in a linked worktree.
An absent path or empty directory is usable. Files, symlinks, nonempty slots,
and symlink parents inside the repository's worktree root are refused; nothing
is deleted or force-repaired. Repeating a successful create fails on the occupied
slot. `brief` owns its separate resume behavior.

Seeding copies ignored basename matches from the main checkout, preserving
permissions and symlinks. Entire ignored directories are considered as single
entries, so the default patterns do not scan dependency trees. Tracked target
files and other existing destinations are preserved. A copy failure is a
warning after successful creation: the path is still returned, and JSON includes
`warnings`, so callers can use the checkout without retrying creation. Use
`--no-copy` to disable seeding for one invocation. Keep configured globs
specific because a matching directory is copied whole.

## Listing and merge verdicts

```sh
gwt list --json                 # every worktree: dirt and verdict against the trunk
gwt merged feat/a feat/b        # branches with or without a checkout
gwt merged --into <rev> <sha>   # a commit (a detached HEAD) against a chosen revision
gwt trunk --fetch               # the trunk, refreshed when older than fetch.max_age
```

"Merged" always means merged into the **trunk**, where work lands: the first of
`origin/HEAD`, `origin/main`, `origin/master`, `main`, `master` that exists,
else the main checkout's branch. Full ref names are tried, so a local branch
called `origin/main` cannot shadow the remote. The trunk is not the creation
`base`, which answers where a new branch forks from and is often the caller's
HEAD. One verdict, `internal/worktree/verdict.go`, serves the tmux popup's tags
and reap, removal, `brief`'s closeout, drive's landing check, and the
clean-worktrees skill's audit, so they cannot disagree about a branch. The
audit verifies its own base against the remote's advertised heads and passes it
as `--into`; everything else measures against the trunk.

A branch is merged by ancestry, by a squash commit matching its combined patch,
or by rebased commits matching each of its patches. Patch matches also require
a clean merge that leaves the trunk's exact contents unchanged (Git 2.38+),
because patch IDs ignore whitespace that can change code. Merge commits on the
branch defeat the per-commit match: `git cherry` cannot see edits made inside a
merge. Later edits to the same lines on the trunk may leave integration
unconfirmed. A branch with no commits of its own counts as merged; it holds
nothing to lose.

A verdict is a pure function of the branch and trunk commits, so it is memoized
in `gwt-merged-v1` in the shared Git directory. Only the first call after either
moves pays the patch-ID scans. Status and verdicts run concurrently, one Git
process per CPU at most. Status probes pass `--no-optional-locks` so they never
contend with an agent working in the same checkout.

`list`, `merged`, and `trunk` read cached refs. `--fetch` first refreshes a
remote trunk whose last fetch, the main checkout's `FETCH_HEAD`, is older than
`fetch.max_age`, bounded by `fetch.timeout`; a failed fetch warns, grades against
cached refs, and sets `trunk.fetch_error`.

An unreadable status reads as dirty and leaves the verdict unknown, so unknown
state is never clean. `list`'s `removable` applies removal's rule (§ Removal) to
the cached verdict. `merged` takes local branches first; an argument that names
none but is a full commit id is judged as that commit, which is how a caller
holding a detached HEAD asks. An unmerged verdict is a result: `merged` exits 1
only when a target could not be judged, and still prints every verdict.
`--into <rev>` measures against that revision instead of the trunk and never
fetches: the caller owns its freshness.

## Removal

gwt is the one removal engine: the tmux popup, `brief`'s closeout and the
clean-worktrees skill all call it. Run from another checkout of the same
repository:

```sh
gwt remove feat/search --json                 # checkout and merged branch
gwt remove feat/abandoned --force --json      # unmerged branch; tip kept
gwt remove /path/to/checkout --keep-branch    # checkout only; detached too
gwt remove feat/a feat/b --discard-dirty      # one batch; dirt snapshotted
gwt remove feat/pr --force --expect-head <sha> --json
```

A target is a branch or a checkout path (absolute or `./`-relative). A branch
finds its registered checkout, even after the configured root changes, or is
deleted alone when it has none. Several targets form one batch.

**Refusals** are one rule, `refusal` in `internal/worktree/remove.go`, which
`list`'s `removable` also applies: main, current, locked, a checkout holding
another worktree, and dirt. Dirt is uncommitted or untracked files, edits
hidden by assume-unchanged or skip-worktree flags, or a status that cannot be
read. Without `--force`, a branch that removal deletes must be merged into the
trunk by the verdict above; removal refreshes a stale remote trunk first, so a
PR squash-merged on GitHub minutes ago counts. `--keep-branch` removes only the
checkout. `--expect-head` refuses unless the single target is still at that
commit, which closes the race between deciding and removing (brief's closeout
and the clean-worktrees runner pass the commit they judged). The verdict can
fetch, so after it the commit and the dirt are read again, just before anything
is kept or moved; a target that changed meanwhile is refused. The branch is deleted only from the commit that
was judged (`git update-ref -d` with the expected value). A writer still
working in the checkout can race that last read, so callers stop their own
first (the popup kills the checkout's windows before calling gwt).

**Recovery.** Nothing irreversible happens without a ref under
`refs/wt-trash/<epoch>.<pid>/<slot>-<name>`, outside `refs/heads` so no branch
list shows it:

- `--discard-dirty` snapshots the checkout's whole working state, untracked
  files included and ignored ones not, as a commit shaped like a stash: first
  parent HEAD, second parent a commit of the index (`<ref>^2`), which holds a
  staged version the working tree has since changed. Scratch index files leave
  the checkout's index and the stash alone. A checkout that cannot be
  snapshotted, such as one with unresolved conflicts, stays.
- `--force` keeps the deleted branch's tip.
- A removed detached checkout keeps its HEAD.

`git branch <name> <ref>` restores any of them. Refs older than
`recovery.keep` (default `30d`, `0` keeps them) expire on each removal.

**Trash.** A checkout is renamed into `<worktree_root>/.trash/<batch>/`, which is
instant on one filesystem however large its dependency trees, then
unregistered. Its now-empty parent directories under the repository's root are
removed without scanning siblings. A process in its own session deletes the
batch, plus any batch older than two minutes a killed run left, so the caller
returns at once and a closing tmux popup's hangup cannot stop the deletion.
Ignored files, including seeded prerequisites, go with the checkout; the
clean-worktrees skill archives them first when they matter.

Every target's result is printed, then the command exits 1 if any failed. With
`--json` that is one object per line per target, so an agent can tell refusal
from partial completion, even when the repository cannot be opened.
`worktree_removed` is true once the checkout is in the trash, even when
unregistering it then fails (`git worktree prune` drops the registration), so a
caller never treats a gone checkout as still there; a branch deletion that fails
after it leaves the branch for inspection. A failed target does not stop the
batch. gwt does not touch tmux windows or processes working in a checkout; its
callers do.

## Integration boundaries

- The dotfiles zsh `gwt` function and its completion wrap the binary; the
  function adds only the parent-shell `cd` for `--cd`. No creation logic lives
  in the shell.
- The tmux popup calls `gwt create -n` using the shared configuration, at
  its terminal, so the new path lands on the clipboard. It then opens the
  window and delivers dependency installation and the agent command. Its rows
  and reap come from `gwt list --json` (`removable`, `merged`), its branch
  prompts from `gwt merged`, its background refresh from `gwt trunk --fetch`,
  and every removal from `gwt remove`. Prompts and tmux windows stay there.
- `brief start` calls `gwt path`, `gwt resolve`, and `gwt create -n --json`,
  retaining its own slot diagnosis and resume behavior. Its clipboard pointer,
  copied after creation, replaces any path gwt copied.
- The clean-worktrees skill's audit (`~/.agents/skills/clean-worktrees`) asks
  `gwt merged --json --into <verified base> <HEAD>` per checkout; its
  freshness, activity, and process checks stay its own. Its runner archives
  ignored files, then calls `gwt remove --keep-branch --expect-head`.
- `brief closeout` emits `gwt remove` commands for a merged branch, with
  `--force --expect-head <PR head>` for a squash merge, and
  `<path> --keep-branch --expect-head <local tip>` for the checkout of a
  branch it holds.
- drive asks `gwt merged <branches…> [<commit>] --json` whether a session's
  work landed, reading the verdicts even when gwt exits 1 for a target it could
  not judge.
- rex-worktree and the `/wt` mod create through `gwt create --non-interactive`
  and take the path from the last line of its output; rex-worktree lists
  through `gwt list --json`.
- The dotfiles worktree mod reads `gwt path` for the root a session follows a
  new worktree under.

Creation prints one path or one JSON object, and `resolve` one line or JSON
object. Diagnostics and prompts use stderr; a failure reads
`gwt: error: <reason>`, and a usage error adds the command's synopsis. Exit codes
are 0 for success, 1 for an operational failure or a declined confirmation, and
2 for invalid arguments, refused before anything runs. `resolve` may refresh
refs; pass `--no-fetch` for a read-only probe. `TestContracts` pins these bytes
for the argv each caller sends (§ Development).

## Development

Go fits the existing personal CLI toolchain and provides the subprocess, timeout,
filesystem, and testing support this tool needs. Rust's ownership model would
add little to a short-lived CLI whose work is mostly performed by Git. gwt
invokes Git directly rather than carrying a second Git implementation; beyond the
standard library it uses clikit and Kong for the command line, a TOML decoder,
and x/term's terminal check.

`cmd/gwt` owns arguments, confirmation, output, and the clipboard copy: each
command is a Kong struct whose `Run` makes one call into `internal/worktree`.
How help renders, how an argv error reads, and which errors are usage errors
belong to clikit (`~/dev/clikit`), shared by the Go CLIs in `~/dev`: a change
there is a clikit release and a `go.mod` bump, reviewed through the golden help
pages. One parsing rule is gwt's own: the first word picks the command
(`dispatch` in `cmd/gwt/main.go`). Kong alone lets a flag of the default command
carry the parse on to a later command name, which would turn `gwt -n remove x`
from creating the branch `remove` into removing `x`.

`internal/worktree` owns resolution, creation, seeding, listing, and removal,
with the merge verdict in `verdict.go` alone; `internal/config` owns layered
configuration and validation.

`make check` runs vet, `go fix -diff` (Go 1.27's modernizers must leave nothing
to rewrite), race-enabled tests, and formatting. What agents and programs read is
pinned by its own suite:

- **Caller output:** `TestContracts` replays each caller's argv over a fixed
  world and compares stdout, stderr, and the exit code byte for byte with
  `cmd/gwt/testdata/contract`. `GWT_CONTRACT_UPDATE=1` rewrites the files;
  `GWT_CONTRACT_BIN=<build>` replays them against a built binary.
- **Help pages:** `TestHelpPages` pins every page; `make golden` rewrites them.
- **JSON documentation:** `TestTheJSONIsDescribed` fails when a key a command's
  `--json` can print is missing from its help page.

Tests use temporary homes and real repositories with local remotes; a hanging
remote helper exercises the fetch deadline. Installation builds beside the
destination and renames it into place so concurrent callers see a complete
executable.
