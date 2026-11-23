package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/SeriousBug/gits3fs/internal/cache"
	"github.com/SeriousBug/gits3fs/internal/config"
	"github.com/SeriousBug/gits3fs/internal/scan"
)

const usagePrune = `
git s3fs prune [options]

Delete objects from the local cache that this repository no longer needs.
Nothing is deleted from the bucket.

An object is kept when any of these refer to it:

  - a file in the working tree or the index
  - the tip of a branch, tag or remote
  - a commit on any ref newer than the retention window
  - a commit that has not been pushed to a remote yet
  - any commit still reachable from the reflog
  - a stash

The rest is dropped, and comes back from the bucket with "git s3fs pull" if an
old commit needs it. By default an object is only removed once it has been
confirmed present in the bucket, so pruning cannot lose the only copy of
anything.

Options:
  --dry-run         Report what would be deleted without deleting
  --age <duration>  How much committed history to keep cached, such as 3d or
                    12h; 0 keeps everything (default s3fs.pruneAge, 7 days)
  --all             Same as --age 0
  --force           Delete even if the object is not in the bucket
  --verify          Rehash every cached object and report corruption
`

func cmdPrune(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dryRun := fs.Bool("dry-run", false, "")
	keepAll := fs.Bool("all", false, "")
	age := fs.String("age", "", "")
	force := fs.Bool("force", false, "")
	verify := fs.Bool("verify", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := newEnv()
	if err != nil {
		return err
	}

	window := e.cfg.PruneAge
	if *age != "" {
		window, err = config.ParseDuration(*age)
		if err != nil {
			return fmt.Errorf("--age: %w", err)
		}
	}
	if *keepAll {
		window = 0
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
		if err := e.verifyCache(entries); err != nil {
			return err
		}
	}

	referenced, err := e.referencedOIDs(window)
	if err != nil {
		return err
	}

	var orphans []cache.Entry
	var total int64
	for _, ent := range entries {
		total += ent.Size
		if !referenced[ent.OID] {
			orphans = append(orphans, ent)
		}
	}
	kept := len(entries) - len(orphans)

	if len(orphans) == 0 {
		fmt.Printf("Nothing to delete: all %d cached object(s), %s, are still needed.\n",
			len(entries), config.FormatSize(total))
		return nil
	}

	present, err := e.objectsInBucket(ctx, orphans, *force)
	if err != nil {
		return err
	}
	if !*force && !e.cfg.Configured() {
		fmt.Println("No bucket configured; refusing to delete unreferenced objects without --force.")
		return nil
	}

	var deleted int
	var freed int64
	for _, ent := range orphans {
		if present != nil && !present[ent.OID] {
			fmt.Printf("keeping %s: it is not in the bucket (use --force to delete anyway)\n", ent.OID[:12])
			kept++
			continue
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
	fmt.Printf("%s %d object(s), freeing %s. Kept %d of %d (%s).\n",
		verb, deleted, config.FormatSize(freed), kept, len(entries), config.FormatSize(total-freed))
	if deleted > 0 && !*dryRun {
		fmt.Println("`git s3fs pull` fetches any of these again if an old commit needs it.")
	}
	return nil
}

// verifyCache rehashes every object and reports the ones that do not match
// their own object id.
func (e *env) verifyCache(entries []cache.Entry) error {
	var bad int
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
	return nil
}

// objectsInBucket reports which candidates the bucket already holds, so that
// deleting a local copy cannot lose the only one. It returns nil when the
// answer must not be consulted, which is what --force asks for.
func (e *env) objectsInBucket(ctx context.Context, candidates []cache.Entry, force bool) (map[string]bool, error) {
	if force || !e.cfg.Configured() {
		return nil, nil
	}
	m, err := e.manager(ctx)
	if err != nil {
		return nil, err
	}
	oids := make([]string, 0, len(candidates))
	for _, c := range candidates {
		oids = append(oids, c.OID)
	}
	return m.Present(ctx, oids)
}

// referencedOIDs collects the objects that must stay cached. The window
// bounds how far back committed history is worth keeping, matching what
// git-lfs does: the working tree, ref tips, unpushed commits and stashes are
// always kept, and older commits only keep their objects for a while.
func (e *env) referencedOIDs(window time.Duration) (map[string]bool, error) {
	scopes := [][]string{
		// Ref tips, so that checking out any branch works without a
		// download. --no-walk keeps this to the tip trees, not history.
		{"--no-walk=unsorted", "--all"},
		// Unpushed commits are the only copy of their objects anywhere.
		{"--all", "--not", "--remotes"},
	}
	if window > 0 {
		cutoff := time.Now().Add(-window).Format(time.RFC3339)
		scopes = append(scopes, []string{"--all", "--since=" + cutoff})
	} else {
		scopes = append(scopes, []string{"--all"})
	}
	if _, err := e.repo.Run("rev-parse", "--verify", "--quiet", "refs/stash"); err == nil {
		scopes = append(scopes, []string{"refs/stash"})
	}
	// Work you can still recover from the reflog has never been pushed under
	// a name, so it is treated like an unpushed commit. --walk-reflogs lists
	// the reflog entries themselves, not their ancestry, so this stays small.
	scopes = append(scopes, []string{"--walk-reflogs", "--all"})

	referenced := map[string]bool{}
	for _, scope := range scopes {
		found, err := scan.History(e.repo, scope)
		if err != nil {
			return nil, err
		}
		for _, f := range found {
			referenced[f.Pointer.OID] = true
		}
	}

	// The index and working tree can hold objects that are not committed yet.
	if e.repo.Root != "" {
		files, err := scan.WorkTree(e.repo, nil)
		if err != nil {
			return nil, err
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
	return referenced, nil
}
