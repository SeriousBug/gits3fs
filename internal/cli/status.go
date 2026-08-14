package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/SeriousBug/gits3fs/internal/config"
	"github.com/SeriousBug/gits3fs/internal/scan"
)

const usageStatus = `
git s3fs status [--no-remote]

Summarise the state of git-s3fs in this repository: which tracked files hold
real content, which are still pointers, and which objects have not been
uploaded yet.

Options:
  --no-remote   Skip the check against the bucket
`

func cmdStatus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	noRemote := fs.Bool("no-remote", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := newEnv()
	if err != nil {
		return err
	}

	if e.cfg.Configured() {
		url, _ := e.cfg.ExampleObjectURL()
		fmt.Printf("Objects go to %s\n", url)
		if !e.cfg.RepoFileExists() {
			fmt.Printf("Warning: no %s is committed, so other clones will not know where objects live.\n", config.FileName)
		}
	} else {
		fmt.Println("No bucket is configured. Run: git s3fs init --bucket <name> --region <region>")
	}

	patterns, err := trackedPatterns(e)
	if err != nil {
		return err
	}
	fmt.Printf("\n%d tracked pattern(s)", len(patterns))
	if len(patterns) > 0 {
		names := make([]string, 0, len(patterns))
		for _, p := range patterns {
			names = append(names, p.Pattern)
		}
		fmt.Printf(": %s", strings.Join(names, ", "))
	}
	fmt.Println()

	if e.repo.Root != "" {
		files, err := scan.WorkTree(e.repo, nil)
		if err != nil {
			return err
		}
		var pointers, missing int
		var bytes int64
		for _, f := range files {
			switch {
			case f.Missing:
				missing++
			case f.Pointer != nil:
				pointers++
				bytes += f.Pointer.Size
			default:
				bytes += f.Size
			}
		}
		fmt.Printf("%d file(s) in the working tree, %s\n", len(files), config.FormatSize(bytes))
		if pointers > 0 {
			fmt.Printf("  %d still hold pointers; run `git s3fs checkout` to download them\n", pointers)
		}
		if missing > 0 {
			fmt.Printf("  %d are tracked but absent from the working tree\n", missing)
		}
	}

	entries, err := e.cache.List()
	if err != nil {
		return err
	}
	var cached int64
	for _, ent := range entries {
		cached += ent.Size
	}
	fmt.Printf("Local cache: %d object(s), %s in %s\n", len(entries), config.FormatSize(cached), e.cache.Root())

	if *noRemote || !e.cfg.Configured() {
		return nil
	}
	return e.reportPending(ctx)
}

// reportPending lists objects on local commits that have not reached the
// bucket yet.
func (e *env) reportPending(ctx context.Context) error {
	found, err := scan.History(e.repo, []string{"--branches", "--tags", "--not", "--remotes"})
	if err != nil {
		return err
	}
	if len(found) == 0 {
		fmt.Println("\nNothing waiting to be uploaded.")
		return nil
	}
	mgr, err := e.manager(ctx)
	if err != nil {
		return err
	}
	primary := mgr.Primary()

	type pending struct {
		path string
		size int64
	}
	var missing []pending
	for _, f := range found {
		exists, _, err := primary.Has(ctx, f.Pointer.OID)
		if err != nil {
			return err
		}
		if !exists {
			missing = append(missing, pending{path: f.Path, size: f.Pointer.Size})
		}
	}
	if len(missing) == 0 {
		fmt.Println("\nEverything on your local commits is already in the bucket.")
		return nil
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].path < missing[j].path })
	var total int64
	for _, m := range missing {
		total += m.size
	}
	fmt.Printf("\n%d object(s) to upload (%s):\n", len(missing), config.FormatSize(total))
	for i, m := range missing {
		if i == 10 {
			fmt.Printf("  ... and %d more\n", len(missing)-10)
			break
		}
		fmt.Printf("  %s (%s)\n", m.path, config.FormatSize(m.size))
	}
	fmt.Println("They will be uploaded on your next `git push`, or run `git s3fs push` now.")
	return nil
}

const usageLsFiles = `
git s3fs ls-files [options] [<pathspec>...]

List the files managed by git-s3fs. Each line shows whether the working tree
holds the real content (*) or still holds a pointer (-).

Options:
  --url    Print the object URL for each file
  --oid    Print the full object id instead of a short one
  --size   Print the object size
`

func cmdLsFiles(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ls-files", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	showURL := fs.Bool("url", false, "")
	fullOID := fs.Bool("oid", false, "")
	showSize := fs.Bool("size", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := newEnv()
	if err != nil {
		return err
	}
	files, err := scan.WorkTree(e.repo, fs.Args())
	if err != nil {
		return err
	}
	for _, f := range files {
		mark, oid, size := "*", "", f.Size
		if f.Pointer != nil {
			mark, oid, size = "-", f.Pointer.OID, f.Pointer.Size
		} else if f.Missing {
			mark = "?"
		}
		if oid == "" {
			// The working tree holds real content, so ask git for the staged
			// pointer to recover the object id.
			if p, err := e.stagedPointer(f.Path); err == nil {
				oid, size = p.OID, p.Size
			}
		}
		short := oid
		if !*fullOID && len(short) > 12 {
			short = short[:12]
		}

		line := fmt.Sprintf("%s %s %s", short, mark, f.Path)
		if *showSize {
			line += fmt.Sprintf(" (%s)", config.FormatSize(size))
		}
		if *showURL && oid != "" {
			if url, err := e.cfg.ObjectURL(oid); err == nil {
				line += " " + url
			}
		}
		fmt.Println(line)
	}
	return nil
}
