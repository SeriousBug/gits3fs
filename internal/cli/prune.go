package cli

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/SeriousBug/gits3fs/internal/config"
	"github.com/SeriousBug/gits3fs/internal/scan"
)

const usagePrune = `
git s3fs prune [options]

Delete objects from the local cache that no commit and no working tree file
refers to any more. Nothing is deleted from the bucket.

By default an object is only removed once it has been confirmed present in the
bucket, so pruning can never lose the only copy of something.

Options:
  --dry-run       Report what would be deleted without deleting
  --force         Delete even if the object is not in the bucket
  --verify        Rehash every cached object and report corruption
`

func cmdPrune(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dryRun := fs.Bool("dry-run", false, "")
	force := fs.Bool("force", false, "")
	verify := fs.Bool("verify", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := newEnv()
	if err != nil {
		return err
	}
	if err := e.cache.CleanTmp(); err != nil {
		return err
	}

	entries, err := e.cache.List()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Println("The local cache is empty.")
		return nil
	}

	if *verify {
		bad := 0
		for _, ent := range entries {
			if err := e.cache.Verify(ent.OID); err != nil {
				bad++
				fmt.Fprintf(os.Stderr, "corrupt: %v\n", err)
			}
		}
		if bad == 0 {
			fmt.Printf("All %d cached object(s) are intact.\n", len(entries))
		} else {
			fmt.Printf("%d of %d cached object(s) are corrupt; delete them and run `git s3fs pull`.\n", bad, len(entries))
		}
	}

	referenced := map[string]bool{}
	found, err := scan.History(e.repo, []string{"--all"})
	if err != nil {
		return err
	}
	for _, f := range found {
		referenced[f.Pointer.OID] = true
	}
	// The index and working tree can hold objects that are not committed yet.
	if e.repo.Root != "" {
		files, err := scan.WorkTree(e.repo, nil)
		if err != nil {
			return err
		}
		for _, f := range files {
			if f.Pointer != nil {
				referenced[f.Pointer.OID] = true
			}
			if p, err := e.stagedPointer(f.Path); err == nil {
				referenced[p.OID] = true
			}
		}
	}

	var mgr interface {
		Has(context.Context, string) (bool, int64, error)
	}
	if !*force && e.cfg.Configured() {
		m, err := e.manager(ctx)
		if err != nil {
			return err
		}
		mgr = m.Primary()
	}

	var deleted, kept int
	var freed int64
	for _, ent := range entries {
		if referenced[ent.OID] {
			kept++
			continue
		}
		if mgr != nil {
			exists, _, err := mgr.Has(ctx, ent.OID)
			if err != nil {
				return err
			}
			if !exists {
				fmt.Printf("keeping %s: it is not in the bucket (use --force to delete anyway)\n", ent.OID[:12])
				kept++
				continue
			}
		} else if !*force {
			fmt.Println("No bucket configured; refusing to delete unreferenced objects without --force.")
			return nil
		}
		if *dryRun {
			fmt.Printf("would delete %s (%s)\n", ent.OID[:12], config.FormatSize(ent.Size))
		} else if err := e.cache.Remove(ent.OID); err != nil {
			return err
		}
		deleted++
		freed += ent.Size
	}

	verb := "Deleted"
	if *dryRun {
		verb = "Would delete"
	}
	fmt.Printf("%s %d object(s), freeing %s. Kept %d.\n", verb, deleted, config.FormatSize(freed), kept)
	return nil
}
