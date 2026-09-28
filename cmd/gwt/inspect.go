package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/qiushiyan/gwt/internal/worktree"
)

// inspect runs the read-only commands that judge worktrees and branches
// against the trunk: list, merged, and trunk.
func inspect(ctx context.Context, r *worktree.Repo, o options, out, stderr io.Writer) int {
	var value any
	var err error
	failed := false
	switch o.command {
	case "list":
		value, err = r.List(ctx, o.fetch)
	case "merged":
		var v worktree.Verdicts
		v, err = r.Merged(ctx, o.branches, o.fetch)
		for _, b := range v.Branches {
			failed = failed || b.Error != ""
		}
		value = v
	case "trunk":
		value, err = r.Trunk(ctx, o.fetch)
	}
	if err != nil {
		fmt.Fprintln(stderr, "gwt:", err)
		return 1
	}
	if o.json {
		err = json.NewEncoder(out).Encode(value)
	} else {
		err = show(value, out, stderr)
	}
	if err != nil {
		fmt.Fprintln(stderr, "gwt:", err)
		return 1
	}
	if failed {
		return 1
	}
	return 0
}

func show(value any, out, stderr io.Writer) error {
	switch v := value.(type) {
	case worktree.Trunk:
		_, err := fmt.Fprintln(out, trunkLine(v))
		return err
	case worktree.Listing:
		fmt.Fprintln(out, "trunk", trunkLine(v.Trunk))
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, t := range v.Worktrees {
			mark, dirty := " ", " "
			if t.Current {
				mark = "»"
			}
			if t.Dirty {
				dirty = "*"
			}
			branch := t.Branch
			if branch == "" {
				branch = "(detached)"
			}
			var tags []string
			for _, tag := range []struct {
				on   bool
				name string
			}{{t.Main, "main"}, {t.Merged != nil && *t.Merged, "merged"}, {t.Locked, "locked"}, {t.Prunable, "prunable"}} {
				if tag.on {
					tags = append(tags, tag.name)
				}
			}
			fmt.Fprintf(w, "%s%s %s\t%s\t%s\n", mark, dirty, branch, t.Path, strings.Join(tags, " "))
			if t.Error != "" {
				fmt.Fprintf(stderr, "gwt: %s: %s\n", t.Path, t.Error)
			}
		}
		return w.Flush()
	case worktree.Verdicts:
		for _, b := range v.Branches {
			switch {
			case b.Error != "":
				fmt.Fprintf(stderr, "gwt: %s: %s\n", b.Branch, b.Error)
			case *b.Merged:
				fmt.Fprintf(out, "merged\t%s\n", b.Branch)
			default:
				fmt.Fprintf(out, "unmerged\t%s\n", b.Branch)
			}
		}
		return nil
	}
	return fmt.Errorf("unexpected value %T", value)
}

func trunkLine(t worktree.Trunk) string {
	line := t.Name + " " + t.Commit[:min(12, len(t.Commit))]
	switch {
	case t.FetchError != "":
		line += " (fetch failed; cached)"
	case t.Fetched:
		line += " (fetched)"
	case t.Stale:
		line += " (stale; --fetch refreshes)"
	}
	return line
}
