package worktree

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/qiushiyan/gwt/internal/config"
)

var ErrNotRepository = errors.New("not inside a Git working tree")

// Repo is one repository as seen from a directory inside it: its main
// checkout, its shared Git directory, its configuration, and the root its
// worktrees are placed under.
type Repo struct {
	dir, main, root, common string
	stderr                  io.Writer
	config                  config.Config
}

func Open(ctx context.Context, dir, home string, stderr io.Writer) (*Repo, error) {
	inside, err := git(ctx, dir, "rev-parse", "--is-inside-work-tree")
	if err != nil || inside != "true" {
		return nil, fmt.Errorf("%w: %s", ErrNotRepository, dir)
	}
	list, err := git(ctx, dir, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	first, _, _ := strings.Cut(list, "\x00")
	if !strings.HasPrefix(first, "worktree ") {
		return nil, fmt.Errorf("cannot determine main worktree")
	}
	main := strings.TrimPrefix(first, "worktree ")
	common, err := git(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(home, common)
	if err != nil {
		return nil, err
	}
	return &Repo{dir: dir, main: main, common: common, root: filepath.Join(cfg.WorktreeRoot, filepath.Base(main)), stderr: stderr, config: cfg}, nil
}

func (r *Repo) validate(ctx context.Context, branch string) error {
	// Full-ref validation rejects path traversal and @{-1} expansion before any
	// fetch or mkdir. --branch adds Git's special HEAD and leading-dash rules.
	if _, err := git(ctx, r.dir, "check-ref-format", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("invalid branch name: %q", branch)
	}
	if _, err := git(ctx, r.dir, "check-ref-format", "--branch", branch); err != nil {
		return fmt.Errorf("invalid branch name: %q", branch)
	}
	return nil
}

func (r *Repo) Configuration() config.Config { return r.config }
