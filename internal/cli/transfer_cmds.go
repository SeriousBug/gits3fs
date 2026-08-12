package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/SeriousBug/gits3fs/internal/config"
	"github.com/SeriousBug/gits3fs/internal/scan"
	"github.com/SeriousBug/gits3fs/internal/transfer"
)

const usagePush = `
git s3fs push [options] [<remote> [<ref>...]]

Upload the objects referenced by the given refs to the bucket. Objects already
in the bucket are skipped, so running it twice is cheap.

The pre-push hook installed by ` + "`git s3fs init`" + ` runs this automatically,
so you rarely need it by hand.

Options:
  --all       Consider every object reachable from any ref
  --dry-run   Report what would be uploaded without uploading

Examples:
  git s3fs push
  git s3fs push origin main
  git s3fs push --dry-run
`

func cmdPush(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	all := fs.Bool("all", false, "")
	dryRun := fs.Bool("dry-run", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var revs []string
	rest := fs.Args()
	switch {
	case *all || len(rest) == 0:
		revs = []string{"--all"}
	case len(rest) == 1:
		revs = []string{"--branches", "--tags"}
	default:
		revs = rest[1:]
	}

	e, err := newEnv()
	if err != nil {
		return err
	}
	return e.push(ctx, revs, *dryRun)
}

func (e *env) push(ctx context.Context, revs []string, dryRun bool) error {
	items, err := e.itemsFromRevs(revs)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Println("Nothing to upload.")
		return nil
	}
	mgr, err := e.manager(ctx)
	if err != nil {
		return err
	}
	verb := "Uploading"
	if dryRun {
		verb = "Would upload"
	}
	fmt.Fprintf(os.Stderr, "%s to %s\n", verb, mgr.Primary().Describe())

	result, err := mgr.Push(ctx, items, dryRun, reporter(verb))
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s %d object(s), %s; %d already present.\n",
		strings.TrimSuffix(verb, "ing")+"ed", result.Transferred, config.FormatSize(result.Bytes), result.Skipped)
	return result.Err()
}

const usageFetch = `
git s3fs fetch [options] [<ref>...]

Download the objects referenced by the given refs into the local cache,
without touching the working tree. Defaults to HEAD.

Options:
  --all   Fetch every object reachable from any ref
`

func cmdFetch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	all := fs.Bool("all", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := newEnv()
	if err != nil {
		return err
	}
	revs := revsFor(*all, fs.Args())
	_, err = e.fetch(ctx, revs)
	return err
}

func (e *env) fetch(ctx context.Context, revs []string) (transfer.Result, error) {
	items, err := e.itemsFromRevs(revs)
	if err != nil {
		return transfer.Result{}, err
	}
	if len(items) == 0 {
		return transfer.Result{}, nil
	}
	mgr, err := e.manager(ctx)
	if err != nil {
		return transfer.Result{}, err
	}
	result, err := mgr.Fetch(ctx, items, reporter("Downloading"))
	if err != nil {
		return result, err
	}
	if result.Transferred > 0 {
		fmt.Fprintf(os.Stderr, "Downloaded %d object(s), %s.\n", result.Transferred, config.FormatSize(result.Bytes))
	}
	return result, result.Err()
}

const usageCheckout = `
git s3fs checkout [<pathspec>...]

Replace pointer files in the working tree with their real contents, fetching
anything that is not cached. Use it after a clone or checkout that ran without
the filters installed, or after ` + "`git s3fs fetch`" + `.
`

func cmdCheckout(ctx context.Context, args []string) error {
	e, err := newEnv()
	if err != nil {
		return err
	}
	n, err := e.checkout(ctx, args)
	if err != nil {
		return err
	}
	if n == 0 {
		fmt.Println("Everything is already checked out.")
	} else {
		fmt.Printf("Checked out %d file(s).\n", n)
	}
	return nil
}

func (e *env) checkout(ctx context.Context, pathspecs []string) (int, error) {
	files, err := scan.WorkTree(e.repo, pathspecs)
	if err != nil {
		return 0, err
	}
	var pending []scan.TrackedFile
	for _, f := range files {
		if f.Pointer != nil {
			pending = append(pending, f)
		}
	}
	if len(pending) == 0 {
		return 0, nil
	}

	mgr, err := e.manager(ctx)
	if err != nil {
		return 0, err
	}
	items := make([]transfer.Item, 0, len(pending))
	for _, f := range pending {
		items = append(items, transfer.Item{Pointer: f.Pointer, Path: f.Path})
	}
	if _, err := mgr.Fetch(ctx, items, reporter("Downloading")); err != nil {
		return 0, err
	}

	written := 0
	var failures []string
	for _, f := range pending {
		if !e.cache.Has(f.Pointer.OID) {
			failures = append(failures, f.Path)
			continue
		}
		if err := e.materialise(f.Path, f.Pointer.OID); err != nil {
			return written, err
		}
		written++
	}
	if len(failures) > 0 {
		return written, fmt.Errorf("could not fetch %d file(s), including %s", len(failures), failures[0])
	}
	return written, nil
}

// materialise replaces a working tree pointer with the cached content,
// preserving the file mode git recorded.
func (e *env) materialise(path, oid string) error {
	full := e.repo.Path(filepath.FromSlash(path))
	mode := os.FileMode(0o644)
	if st, err := os.Stat(full); err == nil {
		mode = st.Mode().Perm()
	}
	src, err := e.cache.Open(oid)
	if err != nil {
		return err
	}
	defer src.Close()

	tmp, err := os.CreateTemp(filepath.Dir(full), ".s3fs-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), full)
}

const usagePull = `
git s3fs pull [<ref>...]

Fetch the objects for the given refs and check them out. Equivalent to
` + "`git s3fs fetch`" + ` followed by ` + "`git s3fs checkout`" + `.

Options:
  --all   Fetch every object reachable from any ref
`

func cmdPull(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	all := fs.Bool("all", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := newEnv()
	if err != nil {
		return err
	}
	if _, err := e.fetch(ctx, revsFor(*all, fs.Args())); err != nil {
		return err
	}
	n, err := e.checkout(ctx, nil)
	if err != nil {
		return err
	}
	fmt.Printf("Checked out %d file(s).\n", n)
	return nil
}

const usagePrePush = `
git s3fs pre-push <remote> <url>

The pre-push hook. Reads the refs being pushed on stdin and uploads the
objects they reference before git contacts the remote.
`

func cmdPrePush(ctx context.Context, args []string) error {
	e, err := newEnv()
	if err != nil {
		return err
	}
	if !e.cfg.Configured() {
		// Nothing to do, and nothing to complain about: the repository may
		// simply not use git-s3fs.
		return nil
	}
	remote := ""
	if len(args) > 0 {
		remote = args[0]
	}

	var revs []string
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		localSHA, remoteSHA := fields[1], fields[3]
		if isZeroOID(localSHA) {
			continue // branch deletion
		}
		revs = append(revs, localSHA)
		if !isZeroOID(remoteSHA) {
			revs = append(revs, "--not", remoteSHA)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if len(revs) == 0 {
		return nil
	}
	// Exclude whatever the remote already has, so a first push of a branch
	// does not re-examine the entire history.
	if remote != "" {
		revs = append(revs, "--not", "--remotes="+remote)
	}
	return e.push(ctx, revs, false)
}

const usageHook = `
Installed hook entry point. git runs this for you.
`

func cmdPostCheckout(ctx context.Context, args []string) error { return hookCheckout(ctx) }
func cmdPostMerge(ctx context.Context, args []string) error    { return hookCheckout(ctx) }

// hookCheckout materialises any pointers left in the working tree, which
// happens when a checkout ran with the filters unavailable or in lazy mode.
func hookCheckout(ctx context.Context) error {
	e, err := newEnv()
	if err != nil {
		return err
	}
	if e.cfg.Lazy || !e.cfg.Configured() {
		return nil
	}
	if _, err := e.checkout(ctx, nil); err != nil {
		fmt.Fprintf(os.Stderr, "git-s3fs: %v\n", err)
	}
	return nil
}

// itemsFromRevs collects the distinct objects referenced by a rev range.
func (e *env) itemsFromRevs(revs []string) ([]transfer.Item, error) {
	found, err := scan.History(e.repo, revs)
	if err != nil {
		return nil, err
	}
	items := make([]transfer.Item, 0, len(found))
	for _, f := range found {
		items = append(items, transfer.Item{Pointer: f.Pointer, Path: f.Path})
	}
	return items, nil
}

func revsFor(all bool, args []string) []string {
	if all {
		return []string{"--all"}
	}
	if len(args) > 0 {
		return args
	}
	return []string{"HEAD"}
}

func isZeroOID(s string) bool { return strings.Trim(s, "0") == "" }

// reporter prints one line per object as it is transferred.
func reporter(verb string) transfer.Reporter {
	return func(ev transfer.Event) {
		name := ev.Path
		if name == "" {
			name = ev.Pointer.OID[:12]
		}
		switch {
		case ev.Err != nil:
			fmt.Fprintf(os.Stderr, "  [%d/%d] failed  %s: %v\n", ev.Done, ev.Total, name, ev.Err)
		case ev.Skipped:
			// Quiet: skipping is the common case on a second run.
		default:
			fmt.Fprintf(os.Stderr, "  [%d/%d] %s %s (%s)\n", ev.Done, ev.Total, strings.ToLower(verb), name, config.FormatSize(ev.Pointer.Size))
		}
	}
}
