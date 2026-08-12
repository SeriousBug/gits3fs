package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/SeriousBug/gits3fs/internal/attrs"
)

const usageTrack = `
git s3fs track [options] [<pattern>...]

Route file patterns through git-s3fs by adding them to .gitattributes. With no
patterns, list what is currently tracked.

Patterns are git attribute patterns, the same syntax as .gitignore. Quote them
so your shell does not expand them first.

Options:
  --renormalize   Re-stage files that already match, converting them to pointers

Examples:
  git s3fs track '*.psd'
  git s3fs track 'assets/**' 'models/*.safetensors'
  git s3fs track --renormalize '*.mp4'
`

func cmdTrack(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("track", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	renormalize := fs.Bool("renormalize", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := newEnv()
	if err != nil {
		return err
	}
	if err := e.repo.RequireWorkTree(); err != nil {
		return err
	}

	patterns := fs.Args()
	if len(patterns) == 0 {
		return listTracked(e)
	}

	path := e.repo.Path(".gitattributes")
	added, err := attrs.Track(path, patterns)
	if err != nil {
		return err
	}
	for _, p := range patterns {
		if containsString(added, p) {
			fmt.Printf("Tracking %q\n", p)
		} else {
			fmt.Printf("%q is already tracked\n", p)
		}
	}
	if len(added) == 0 {
		return nil
	}
	if *renormalize {
		if _, err := e.repo.Run("add", "--renormalize", "."); err != nil {
			return fmt.Errorf("re-staging matching files: %w", err)
		}
		fmt.Println("Re-staged matching files as pointers.")
	} else {
		fmt.Println("\nFiles already committed keep their old contents. To convert them, run:")
		fmt.Println("  git s3fs migrate import")
	}
	fmt.Println("Remember to commit .gitattributes.")
	return nil
}

const usageUntrack = `
git s3fs untrack <pattern>...

Stop routing file patterns through git-s3fs by removing them from
.gitattributes. Files already committed as pointers stay pointers; run
` + "`git s3fs migrate export`" + ` to put their contents back into git.
`

func cmdUntrack(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("untrack needs at least one pattern")
	}
	e, err := newEnv()
	if err != nil {
		return err
	}
	if err := e.repo.RequireWorkTree(); err != nil {
		return err
	}
	removed, err := attrs.Untrack(e.repo.Path(".gitattributes"), args)
	if err != nil {
		return err
	}
	for _, p := range args {
		if containsString(removed, p) {
			fmt.Printf("Untracking %q\n", p)
		} else {
			fmt.Printf("%q was not tracked\n", p)
		}
	}
	return nil
}

// listTracked prints every tracked pattern across the repository.
func listTracked(e *env) error {
	entries, err := trackedPatterns(e)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Println("No patterns are tracked. Try: git s3fs track '*.psd'")
		return nil
	}
	fmt.Println("Tracked patterns:")
	for _, ent := range entries {
		fmt.Printf("  %-40s (%s:%d)\n", ent.Pattern, ent.File, ent.Line)
	}
	return nil
}

func trackedPatterns(e *env) ([]attrs.Entry, error) {
	files := map[string]bool{".gitattributes": true}
	if raw, err := e.repo.RunRaw("ls-files", "-z", "--", "*.gitattributes", ".gitattributes"); err == nil {
		for _, f := range splitNul(raw) {
			files[f] = true
		}
	}
	var names []string
	for f := range files {
		names = append(names, f)
	}
	sort.Strings(names)

	var out []attrs.Entry
	for _, name := range names {
		entries, err := attrs.ListFile(filepath.Join(e.repo.Root, filepath.FromSlash(name)), name)
		if err != nil {
			return nil, err
		}
		out = append(out, entries...)
	}
	return out, nil
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func splitNul(b []byte) []string {
	var out []string
	start := 0
	for i, c := range b {
		if c == 0 {
			if i > start {
				out = append(out, string(b[start:i]))
			}
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, string(b[start:]))
	}
	return out
}
