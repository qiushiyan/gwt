# gwt

A personal worktree CLI. `gwt` resolves a branch, places it under
`~/dev/.worktrees/<main-checkout>/<branch>` by default, seeds ignored prerequisites from
the main checkout, and prints its absolute path. Git owns repository state;
the CLI owns the placement policy.

## Install and use

Requires Go 1.25+, Git, and `cp` on macOS or Linux. TOML decoding uses BurntSushi/toml.

```sh
make check
make install                    # installs to ~/.local/bin; keep that directory on PATH
gwt fix/login main              # explicit base, no confirmation
gwt feat/search                 # confirm forking from current HEAD
gwt create -n feat/agent-work    # unattended; stdout is only the path
gwt create -n feat/agent-json --json
gwt --help
```

The binary does not change the caller's directory. The dotfiles `gwtcd` helper
captures its path and changes the parent shell's directory. Agents use the
returned path as their working directory. An existing shell may still hold the
old `gwt` function; run `zshreload` once to pick up the binary.

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

Without `--force`, branch work must be integrated into the configured base by
ancestry or matching squash/rebase patches. Removal resolves the base in the
**main checkout**, so `HEAD` means its current branch regardless of the caller.
Creation still uses the caller's HEAD. These checks use local refs without fetching.
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

- Dotfiles zsh completion and `gwtcd` call the binary. No creation logic lives in
  the shell helper.
- The tmux popup calls `gwt create -n` using the shared configuration.
  It then opens the window and delivers dependency installation and the agent
  command. Listing, merge checks, removal, and recovery stay in dotfiles.
- `brief start` calls `gwt path`, `gwt resolve`, and `gwt create -n --json`,
  retaining its own slot diagnosis and resume behavior.
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
uses a TOML decoder alongside the standard library and invokes Git directly,
avoiding a second Git implementation or a CLI framework.

`cmd/gwt` owns arguments, confirmation, and output. `internal/worktree` owns
resolution, creation, seeding, and removal; `internal/config` owns layered
configuration and validation. `make check` runs race-enabled tests, vet,
and formatting checks. Tests use temporary homes and real repositories/local
remotes; a hanging remote helper exercises the fetch deadline. Installation
builds beside the destination and renames it into place so concurrent callers
see a complete executable.
