# Working on gwt

gwt places Git worktrees and decides when one may be removed. A person calls it
through the zsh `gwt` function; the tmux popup, brief, drive, rex-worktree, the
`/wt` mod and the clean-worktrees and enter-worktree skills run the binary and
read its output, so it is the one placement policy and the one removal engine
they share. `README.md` owns the design; read it before changing behaviour.

- One maintainer's tool: commit on `main`, push at milestones, and keep
  changes proportional to demonstrated failures or requested features.
  Dependencies: the standard library, clikit and Kong, plus BurntSushi/toml
  for the config files and x/term for the terminal check.
- **Contracts other repositories depend on:** `create`'s path line or JSON
  (popup, rex-worktree, `/wt`, enter-worktree, brief); `resolve`'s line and
  `path` (brief); `list --json` and `trunk --fetch --json` (popup,
  rex-worktree); `merged --json`, read even on exit 1 (drive, the
  clean-worktrees audit, popup); `remove --json`, one object per target, read
  even on exit 1, its `error` text matched by tests (popup, the clean-worktrees
  runner, the commands brief closeout emits); `config show --json` (clean-worktrees);
  the root help's `Commands:` block (the zsh completion test); exit codes, which
  the zsh wrapper passes through. Pinned byte for byte by `TestContracts`
  (`cmd/gwt/testdata/contract`, rewritten with `GWT_CONTRACT_UPDATE=1`;
  `GWT_CONTRACT_BIN=<build>` replays it against a binary). A change to one
  updates its consumers in the same pass; find them with:

  ```sh
  rg -l --hidden -g '!.git' -e "\bgwt['\"]?,? +['\"]?(create|resolve|path|remove|list|merged|trunk|config|-n|--)" \
    -e "[\"']gwt[\"']" ~/dev ~/dotfiles | grep -v -e /dev/gwt/ -e /.worktrees/
  ```
- **The first word picks the command.** `dispatch` (`cmd/gwt/main.go`) sends
  anything else to create, as the parser always did; Kong alone would carry a
  create flag on to a later command name and turn `gwt -n remove x` into a
  removal. Held by `create-command-name-later` in `TestContracts`.
- **One removal rule and one merge verdict.** `refusal`
  (`internal/worktree/remove.go`) decides both `remove` and `list`'s
  `removable`; `internal/worktree/verdict.go` alone decides "merged" for list,
  merged and remove. Held by `TestRemoveProtectsWorktrees`,
  `TestRemoveRecognizesMergeStyles` and `TestRemoveVerdictIgnoresCaller`.
- **Nothing irreversible without a recovery ref.** A removal keeps a ref under
  `refs/wt-trash/` before it discards work, rereads the commit and the dirt just
  before acting, and deletes a branch only from the commit it judged. Held by
  `TestRemoveDiscardDirtySnapshots`, `TestRemoveUnmergedRequiresForce` and
  `TestRemoveKeepsATipThatMovedAfterTheRecheck`.
- Tests never touch live state: real worktrees under `~/dev`,
  `~/.config/gwt/config.toml`, global Git config, the clipboard and real
  remotes. `newWorld` (`cmd/gwt/world_test.go`) and `setup`
  (`internal/worktree/fixture_test.go`) give each test a temporary `HOME` and
  config, `GIT_CONFIG_GLOBAL=/dev/null` and local bare remotes; `newWorld` also
  puts a `toclip` stub first on `PATH`.
- Ship: `make check`, then `make install`. Callers run `~/.local/bin/gwt` and
  keep the old build until then; the mini keeps its own until `twin sync gwt`.
- The command line runs on clikit: how help renders and how argv errors read
  are clikit's (`~/dev/clikit`). Help, results and errors an agent reads
  follow `~/dotfiles/claude/.claude/skills/prompt-engineering/SKILL.md`;
  library questions go to `~/.agents/skills/find-docs/SKILL.md`.

Docs follow `~/dotfiles/docs/documentation-standards.md`, paths written from
the repository root.
