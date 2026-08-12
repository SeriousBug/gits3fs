package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/SeriousBug/gits3fs/internal/attrs"
	"github.com/SeriousBug/gits3fs/internal/config"
	"github.com/SeriousBug/gits3fs/internal/scan"
)

const usageMigrate = `
git s3fs migrate <import|export|info> [options]

Convert files between real content and git-s3fs pointers.

  import   Track patterns and re-stage matching files as pointers
  export   Stop tracking patterns and put real content back into git
  info     Report the largest files in the working tree, to help pick patterns

Options:
  --include <pattern>   Pattern to convert; repeatable. Required for import
                        and export unless patterns are already tracked
  --above <size>        For info: only report files at least this big
                        (default 1mb)
  --top <n>             For info: how many files to list (default 20)

These commands rewrite the index and working tree, not history. Existing
commits keep whatever they already contain, so a fresh clone of an old commit
still gets the old blobs. To rewrite history, use git-filter-repo and run
` + "`git s3fs migrate import`" + ` as its conversion step.

Examples:
  git s3fs migrate info --above 5mb
  git s3fs migrate import --include '*.psd' --include '*.mp4'
  git s3fs migrate export --include '*.psd'
`

func cmdMigrate(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("migrate needs a subcommand: import, export or info")
	}
	sub, rest := args[0], args[1:]

	fs := flag.NewFlagSet("migrate "+sub, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var include multiFlag
	fs.Var(&include, "include", "")
	above := fs.String("above", "1mb", "")
	top := fs.Int("top", 20, "")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	include = append(include, fs.Args()...)

	e, err := newEnv()
	if err != nil {
		return err
	}
	if err := e.repo.RequireWorkTree(); err != nil {
		return err
	}

	switch sub {
	case "import":
		return e.migrateImport(ctx, include)
	case "export":
		return e.migrateExport(ctx, include)
	case "info":
		size, err := config.ParseSize(*above)
		if err != nil {
			return err
		}
		return e.migrateInfo(size, *top)
	default:
		return fmt.Errorf("unknown migrate subcommand %q", sub)
	}
}

func (e *env) migrateImport(ctx context.Context, include []string) error {
	if len(include) > 0 {
		added, err := attrs.Track(e.repo.Path(".gitattributes"), include)
		if err != nil {
			return err
		}
		for _, p := range added {
			fmt.Printf("Tracking %q\n", p)
		}
	}
	patterns, err := trackedPatterns(e)
	if err != nil {
		return err
	}
	if len(patterns) == 0 {
		return fmt.Errorf("nothing is tracked yet; pass --include '<pattern>' or run `git s3fs track` first")
	}

	// --renormalize re-runs the clean filter over everything in the index,
	// which is exactly the conversion we want: matching files become
	// pointers and their contents land in the local cache.
	if _, err := e.repo.Run("add", "--renormalize", "."); err != nil {
		return fmt.Errorf("re-staging files: %w", err)
	}

	files, err := scan.WorkTree(e.repo, nil)
	if err != nil {
		return err
	}
	var total int64
	for _, f := range files {
		if p, err := e.stagedPointer(f.Path); err == nil {
			total += p.Size
		}
	}
	fmt.Printf("Converted %d file(s), %s, to pointers.\n", len(files), config.FormatSize(total))
	fmt.Println("Review with `git status`, then commit. Objects upload on your next push.")
	return nil
}

func (e *env) migrateExport(ctx context.Context, include []string) error {
	if len(include) == 0 {
		return fmt.Errorf("export needs at least one --include pattern")
	}
	// Real content has to be present before the patterns stop being tracked,
	// otherwise the pointers would be committed as ordinary files.
	if _, err := e.checkout(ctx, nil); err != nil {
		return fmt.Errorf("downloading content before export: %w", err)
	}
	removed, err := attrs.Untrack(e.repo.Path(".gitattributes"), include)
	if err != nil {
		return err
	}
	for _, p := range removed {
		fmt.Printf("Untracking %q\n", p)
	}
	if _, err := e.repo.Run("add", "--renormalize", "."); err != nil {
		return fmt.Errorf("re-staging files: %w", err)
	}
	fmt.Println("File contents are back in the index. Review with `git status`, then commit.")
	return nil
}

func (e *env) migrateInfo(above int64, top int) error {
	raw, err := e.repo.RunRaw("ls-files", "-z", "--cached")
	if err != nil {
		return err
	}
	type entry struct {
		path string
		size int64
	}
	var big []entry
	for _, path := range splitNul(raw) {
		st, err := os.Stat(e.repo.Path(path))
		if err != nil || !st.Mode().IsRegular() || st.Size() < above {
			continue
		}
		big = append(big, entry{path, st.Size()})
	}
	if len(big) == 0 {
		fmt.Printf("No tracked files are %s or larger.\n", config.FormatSize(above))
		return nil
	}
	for i := 1; i < len(big); i++ {
		for j := i; j > 0 && big[j].size > big[j-1].size; j-- {
			big[j], big[j-1] = big[j-1], big[j]
		}
	}

	byExt := map[string]int64{}
	countExt := map[string]int{}
	for _, b := range big {
		ext := "(no extension)"
		if i := strings.LastIndex(b.path, "."); i >= 0 && i < len(b.path)-1 {
			ext = "*" + b.path[i:]
		}
		byExt[ext] += b.size
		countExt[ext]++
	}

	fmt.Printf("Largest tracked files at least %s:\n", config.FormatSize(above))
	for i, b := range big {
		if i >= top {
			fmt.Printf("  ... and %d more\n", len(big)-top)
			break
		}
		fmt.Printf("  %10s  %s\n", config.FormatSize(b.size), b.path)
	}
	fmt.Println("\nBy extension:")
	for ext, size := range byExt {
		fmt.Printf("  %-16s %4d file(s)  %s\n", ext, countExt[ext], config.FormatSize(size))
	}
	fmt.Println("\nTo convert, for example:")
	for ext := range byExt {
		if strings.HasPrefix(ext, "*") {
			fmt.Printf("  git s3fs migrate import --include '%s'\n", ext)
		}
	}
	return nil
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }

func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}
