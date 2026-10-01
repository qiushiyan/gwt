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
gwt --help
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
`remove`, for example, use `gwt create remove`.

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
HEAD. One verdict serves the tmux popup's tags and reap, removal, `brief`'s
closeout, and the clean-worktrees skill's audit, so they cannot disagree about
a branch. The audit verifies its own base against the remote's advertised
heads and passes it as `--into`; everything else measures against the trunk.

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
remote trunk that no fetch has touched within `fetch.max_age`, bounded by
`fetch.timeout`; a failed fetch warns, grades against cached refs, and sets
`trunk.fetch_error`. JSON shapes:

```json
{"trunk":{"name":"origin/develop","commit":"…","remote":"origin","stale":false},
 "worktrees":[{"path":"…","branch":"feat/x","head":"…","main":false,"current":false,
   "locked":false,"prunable":false,"dirty":true,"merged":false}]}
{"trunk":{…},"branches":[{"branch":"feat/x","commit":"…","merged":true}]}
```

`merged` is `null` for a detached checkout, a missing branch, or a failed check
(then `error` says why). `merged` exits 1 if any named branch could not be judged.
Its arguments are local branches first; an argument that names none but is a
full commit id is judged as that commit. `--into <rev>` measures against that
revision instead of the trunk, reported in the `trunk` field, and never
fetches: the caller owns its freshness.

## Removal

Run from another checkout of the same repository:

```sh
gwt remove feat/search --json
gwt remove feat/abandoned --force --json
```

Removal is non-interactive and finds the registered worktree by its local
branch, even after the configured root changes. It deletes the checkout and
then the branch. Main, current, locked, dirty, and untracked worktrees are
protected. Ignored files, including seeded prerequisites, go with the checkout.

Without `--force`, the branch must be merged into the trunk, by the verdict
above. Removal refreshes a stale remote trunk first, so a PR squash-merged on
GitHub minutes ago counts without a manual fetch; if the fetch fails it warns
and judges against cached refs. The verdict does not depend on which checkout
calls removal.
Unconfirmed work stays in place; `--force` permits discarding it while retaining
checkout protections. A registered worktree whose directory is already missing
can still be removed along with its branch.

Success exits 0; operational failure exits 1. JSON reports both steps so an
agent can distinguish refusal from partial completion:

```json
{"ok":true,"path":"/absolute/checkout","branch":"feat/search","worktree_removed":true,"branch_deleted":true}
```

Failures include `error`. If branch deletion fails after checkout removal,
`worktree_removed` is true and `branch_deleted` is false; the branch remains
available for manual cleanup. The command does not manage tmux windows or
create recovery snapshots. The tmux popup owns its richer interactive cleanup.

## Integration boundaries

- The dotfiles zsh `gwt` function and its completion wrap the binary; the
  function adds only the parent-shell `cd` for `--cd`. No creation logic lives
  in the shell.
- The tmux popup calls `gwt create -n` using the shared configuration, at
  its terminal, so the new path lands on the clipboard. It then opens the
  window and delivers dependency installation and the agent command. Its rows
  and reap come from `gwt list --json`, its branch cleanup from `gwt merged`,
  and its background refresh from `gwt trunk --fetch`.
  Trash-and-sweep removal, snapshots, and recovery stay in dotfiles.
- `brief start` calls `gwt path`, `gwt resolve`, and `gwt create -n --json`,
  retaining its own slot diagnosis and resume behavior. Its clipboard pointer,
  copied after creation, replaces any path gwt copied.
- The clean-worktrees skill's audit (`~/.agents/skills/clean-worktrees`) asks
  `gwt merged --json --into <verified base> <HEAD>` per checkout; its
  freshness, activity, and process checks stay its own.
- The `enter-worktree` skill calls the installed binary and enters its returned
  path. The old `worktree-core.sh` CLI is a forwarding shim for running shells.

Creation stdout is one path or one JSON object; resolution prints one verdict
or JSON object. Diagnostics and prompts use stderr. Exit codes are 0 for success, 1 for operational failure or declined
confirmation, and 2 for command-line usage errors. `resolve` may refresh refs;
pass `--no-fetch` for a read-only probe.

## Development

Go fits the existing personal CLI toolchain and provides the subprocess, timeout,
filesystem, and testing support this tool needs. Rust's ownership model would
add little to a short-lived CLI whose work is mostly performed by Git. The code
uses a TOML decoder and x/term's terminal check alongside the standard
library and invokes Git directly, avoiding a second Git implementation or a CLI
framework.

`cmd/gwt` owns arguments, confirmation, output, and the clipboard copy.
`internal/worktree` owns resolution, creation, seeding, listing, merge verdicts,
and removal; `internal/config` owns layered configuration and validation.
`make check` runs race-enabled tests, vet, and formatting checks. Tests use
temporary homes and real repositories/local remotes; a hanging remote helper
exercises the fetch deadline. Installation builds beside the destination and
renames it into place so concurrent callers see a complete executable.
