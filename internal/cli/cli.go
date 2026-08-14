// Package cli implements the git-s3fs command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/SeriousBug/gits3fs/internal/cache"
	"github.com/SeriousBug/gits3fs/internal/config"
	"github.com/SeriousBug/gits3fs/internal/gitcmd"
	"github.com/SeriousBug/gits3fs/internal/transfer"
	"github.com/SeriousBug/gits3fs/internal/version"
)

// command is one subcommand.
type command struct {
	name    string
	summary string
	usage   string
	run     func(ctx context.Context, args []string) error
	// hidden keeps hook entry points out of the main help listing.
	hidden bool
}

func commands() []command {
	return []command{
		{name: "init", summary: "Configure a repository to use git-s3fs", usage: usageInit, run: cmdInit},
		{name: "install", summary: "Install the git filters and hooks", usage: usageInstall, run: cmdInstall},
		{name: "uninstall", summary: "Remove the git filters and hooks", usage: usageUninstall, run: cmdUninstall},
		{name: "track", summary: "Track file patterns with git-s3fs", usage: usageTrack, run: cmdTrack},
		{name: "untrack", summary: "Stop tracking file patterns", usage: usageUntrack, run: cmdUntrack},
		{name: "status", summary: "Show tracked files and pending uploads", usage: usageStatus, run: cmdStatus},
		{name: "ls-files", summary: "List files managed by git-s3fs", usage: usageLsFiles, run: cmdLsFiles},
		{name: "push", summary: "Upload objects to the bucket", usage: usagePush, run: cmdPush},
		{name: "fetch", summary: "Download objects into the local cache", usage: usageFetch, run: cmdFetch},
		{name: "checkout", summary: "Replace pointers in the working tree with content", usage: usageCheckout, run: cmdCheckout},
		{name: "pull", summary: "Fetch objects and check them out", usage: usagePull, run: cmdPull},
		{name: "prune", summary: "Delete unreferenced objects from the local cache", usage: usagePrune, run: cmdPrune},
		{name: "migrate", summary: "Convert files to or from git-s3fs pointers", usage: usageMigrate, run: cmdMigrate},
		{name: "doctor", summary: "Check the setup of this repository", usage: usageDoctor, run: cmdDoctor},
		{name: "env", summary: "Show the resolved configuration", usage: usageEnv, run: cmdEnv},
		{name: "url", summary: "Print the object URL for a tracked file", usage: usageURL, run: cmdURL},
		{name: "version", summary: "Print the version", usage: usageVersion, run: cmdVersion},

		{name: "clean", summary: "git clean filter", usage: usageClean, run: cmdClean, hidden: true},
		{name: "smudge", summary: "git smudge filter", usage: usageSmudge, run: cmdSmudge, hidden: true},
		{name: "filter-process", summary: "git long running filter", usage: usageFilterProcess, run: cmdFilterProcess, hidden: true},
		{name: "pre-push", summary: "pre-push hook", usage: usagePrePush, run: cmdPrePush, hidden: true},
		{name: "post-checkout", summary: "post-checkout hook", usage: usageHook, run: cmdPostCheckout, hidden: true},
		{name: "post-merge", summary: "post-merge hook", usage: usageHook, run: cmdPostMerge, hidden: true},
	}
}

// Main runs the command line and returns a process exit code.
func Main(ctx context.Context, args []string) int {
	if len(args) == 0 {
		printHelp(os.Stdout)
		return 0
	}
	name := args[0]
	rest := args[1:]

	switch name {
	case "-h", "--help", "help":
		if len(rest) > 0 {
			if c, ok := lookup(rest[0]); ok {
				fmt.Fprintln(os.Stdout, strings.TrimSpace(c.usage))
				return 0
			}
			fmt.Fprintf(os.Stderr, "git-s3fs: unknown command %q\n", rest[0])
			return 2
		}
		printHelp(os.Stdout)
		return 0
	case "-v", "--version":
		name = "version"
	}

	c, ok := lookup(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "git-s3fs: unknown command %q\nRun `git s3fs help` for usage.\n", name)
		return 2
	}
	for _, a := range rest {
		if a == "-h" || a == "--help" {
			fmt.Fprintln(os.Stdout, strings.TrimSpace(c.usage))
			return 0
		}
	}
	if err := c.run(ctx, rest); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "git-s3fs: interrupted")
			return 130
		}
		fmt.Fprintf(os.Stderr, "git-s3fs: %v\n", err)
		return 1
	}
	return 0
}

func lookup(name string) (command, bool) {
	for _, c := range commands() {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

func printHelp(w io.Writer) {
	fmt.Fprintf(w, `git-s3fs %s - large files in git, stored in your own S3 bucket

Usage:
  git s3fs <command> [options]

Commands:
`, version.Version)

	cmds := commands()
	sort.Slice(cmds, func(i, j int) bool { return cmds[i].name < cmds[j].name })
	width := 0
	for _, c := range cmds {
		if !c.hidden && len(c.name) > width {
			width = len(c.name)
		}
	}
	for _, c := range cmds {
		if c.hidden {
			continue
		}
		fmt.Fprintf(w, "  %-*s  %s\n", width, c.name, c.summary)
	}
	fmt.Fprint(w, `
Run `+"`git s3fs help <command>`"+` for details on a command.

Getting started:
  git s3fs init --bucket my-bucket --region us-east-1
  git s3fs track '*.psd'
  git add .gitattributes .gits3fs
  git commit -m "Track PSD files with git-s3fs"
`)
}

// env bundles the repository, its configuration and the local cache.
type env struct {
	repo  *gitcmd.Repo
	cfg   *config.Config
	cache *cache.Cache

	mgrOnce sync.Once
	mgr     *transfer.Manager
	mgrErr  error
}

// newEnv discovers the repository and loads its configuration.
func newEnv() (*env, error) {
	if !gitcmd.Available() {
		return nil, errors.New("git was not found on PATH")
	}
	repo, err := gitcmd.Discover()
	if err != nil {
		return nil, err
	}
	root := repo.Root
	if root == "" {
		root = repo.GitDir
	}
	cfg, err := config.Load(root, repo.ConfigGet)
	if err != nil {
		return nil, err
	}
	return &env{repo: repo, cfg: cfg, cache: cache.New(repo.GitPath("s3fs"))}, nil
}

// manager builds a transfer manager for the repository.
func (e *env) manager(ctx context.Context) (*transfer.Manager, error) {
	return transfer.NewManager(ctx, e.cfg, e.cache)
}

// configFilePath is the path of the committed configuration file.
func (e *env) configFilePath() string {
	root := e.repo.Root
	if root == "" {
		root = e.repo.GitDir
	}
	return root + string(os.PathSeparator) + config.FileName
}
