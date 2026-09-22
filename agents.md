# Working on gwt

Personal CLI. Keep changes proportional to demonstrated failures or requested features.

- Contract changes cross repos: check `~/dev/brief`, `~/dotfiles/zsh`,
  `~/dotfiles/tmux`, and `~/dotfiles/claude/.claude/skills/enter-worktree`.
  Callers share gwt's configuration; tmux owns its separate interactive cleanup.
- Integration tests need temporary homes, config, and repositories. Live defaults
  reach real worktrees; isolate tmux sockets and clipboard commands too.
- Consumers run `~/.local/bin/gwt`. Run `make check`, then `make install` when
  shipping executable changes; source edits alone leave callers on the old binary.
- Model-facing help, results, errors, or skills: follow
  `~/dotfiles/claude/.claude/skills/prompt-engineering/SKILL.md`.
- Library/CLI documentation questions: follow `~/.agents/skills/find-docs/SKILL.md`.
