package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"
)

// attended reports whether a person is watching: stderr, where prompts go, is
// a terminal. Agents and scripts capture it, so they keep their clipboard.
// /dev/null is a character device too, hence a real terminal check.
var attended = func(stderr io.Writer) bool {
	f, ok := stderr.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// copyPath puts the path on the clipboard of the machine the user sits at.
// toclip (dotfiles) reaches the laptop over SSH; pbcopy is the local fallback.
func copyPath(ctx context.Context, path string) error {
	const timeout = 3 * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var cmd *exec.Cmd
	if bin, err := exec.LookPath("toclip"); err == nil {
		cmd = exec.CommandContext(ctx, bin, "-q")
	} else if bin, err := exec.LookPath("pbcopy"); err == nil {
		cmd = exec.CommandContext(ctx, bin)
	} else {
		return errors.New("neither toclip nor pbcopy is on PATH")
	}
	cmd.Stdin = strings.NewReader(path)
	// pbcopy can mangle non-ASCII text under LC_ALL=C.
	cmd.Env = append(os.Environ(), "LC_ALL=en_US.UTF-8")
	// A helper's child that inherits the pipe must not hold gwt open.
	cmd.WaitDelay = 100 * time.Millisecond
	if _, err := cmd.Output(); err != nil {
		name := filepath.Base(cmd.Path)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%s timed out after %s", name, timeout)
		}
		if exit, ok := errors.AsType[*exec.ExitError](err); ok && len(exit.Stderr) > 0 {
			return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(exit.Stderr)))
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
