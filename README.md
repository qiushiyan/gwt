# gwt

A personal worktree CLI. `gwt` resolves a branch, places it under
`~/dev/.worktrees/<main-checkout>/<branch>`, seeds ignored prerequisites from
the main checkout, and prints its absolute path. Git owns repository state;
the CLI owns the placement policy.

## Install and use

Requires Go 1.25+, Git, and `cp` on macOS or Linux. There are no Go dependencies.

```sh
make check
make install                    # ~/.local/bin/gwt; put ~/.local/bin on PATH
gwt fix/login main              # explicit base, no confirmation
gwt feat/search                 # confirm forking from current HEAD
gwt create -n feat/agent-work    # unattended; stdout is only the path
gwt create -n feat/agent-json --json
gwt --help
```

The binary does not change the caller's directory. The dotfiles `gwtcd` helper
captures its path and changes the parent shell's directory. Agents use the
returned path as their working directory. An existing shell may still hold the
old `gwt` function; run `zshreload` once or invoke `~/.local/bin/gwt` directly.

`create` and `resolve` are command names; to create a branch literally named
`create` or `resolve`, use `gwt create create` or `gwt create resolve`.

## Placement rules

Local branches win over remote names. A unique remote branch becomes a local
tracking branch; multiple matching remotes fail with a disambiguation command.
Only an absent name forks from the base. `--new` ignores remote matches but
never resets an existing local branch. New branches do not inherit tracking.

An absent name triggers a fetch of all remotes if `FETCH_HEAD` is missing,
empty, or older than five minutes. The fetch has an eight-second deadline,
including child processes. Failure warns and uses cached refs, matching the
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
`--no-copy` or `WORKTREE_COPY_GLOBS=off` to disable seeding; custom globs are
whitespace-separated. Keep them specific because a matching directory is copied
whole.

## Integration boundaries

- Dotfiles zsh completion and `gwtcd` call the binary. No creation logic lives in
  the shell helper.
- The tmux popup calls `gwt create -n` with its default base and copy patterns.
  It then opens the window and delivers dependency installation and the agent
  command. Listing, merge checks, removal, and recovery stay in dotfiles.
- `brief start` calls `gwt resolve` and `gwt create -n`, retaining its own slot
  diagnosis and resume behavior.
- The `enter-worktree` skill calls the installed binary and enters its returned
  path. The old `worktree-core.sh` CLI is a forwarding shim for running shells.

Stdout is one path, one verdict line, or one JSON object. Diagnostics and prompts
use stderr. Exit codes are 0 for success, 1 for operational failure or declined
confirmation, and 2 for command-line usage errors. `resolve` may refresh refs;
pass `--no-fetch` for a read-only probe. A base affects only new branches.

## Development

Go fits the existing personal CLI toolchain and provides the subprocess, timeout,
filesystem, and testing support this tool needs. Rust's ownership model would
add little to a short-lived CLI whose work is mostly performed by Git. The code
uses the standard library and invokes Git directly rather than introducing a
second Git implementation or a CLI framework.

`cmd/gwt` owns arguments, confirmation, and output. `internal/worktree` owns
resolution, creation, and seeding. `make check` runs race-enabled tests, vet,
and formatting checks. Tests use temporary homes and real repositories/local
remotes; a hanging remote helper exercises the fetch deadline. Installation
builds beside the destination and renames it into place so concurrent callers
see a complete executable.
