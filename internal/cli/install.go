package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SeriousBug/gits3fs/internal/attrs"
	"github.com/SeriousBug/gits3fs/internal/config"
	"github.com/SeriousBug/gits3fs/internal/gitcmd"
)

const usageInit = `
git s3fs init [options]

Set up git-s3fs in the current repository: install the git filters and hooks,
and record which bucket objects go to.

The bucket settings are written to .gits3fs, which is meant to be committed.
Everyone who clones the repository then gets the right bucket automatically.
Credentials are never written there; they come from the AWS credential chain.

Options:
  --bucket <name>          S3 bucket holding the objects
  --region <region>        S3 region, for example us-east-1
  --prefix <path>          Key prefix inside the bucket
  --endpoint <url>         S3 compatible endpoint (MinIO, R2, B2, Spaces)
  --path-style             Use path style addressing (endpoint/bucket/key)
  --public-url <url>       Base URL to write into pointers, such as a CDN
  --anonymous              Read the bucket without credentials
  --storage-class <class>  Storage class for uploads, e.g. STANDARD_IA
  --sse <algorithm>        Server side encryption, e.g. AES256 or aws:kms
  --sse-kms-key-id <id>    KMS key id when --sse is aws:kms
  --local                  Write settings to .git/config instead of .gits3fs
  --global                 Install the filters for every repository
  --force                  Overwrite existing hooks that are not ours

Examples:
  git s3fs init --bucket my-assets --region eu-west-1 --prefix repos/website
  git s3fs init --bucket assets --endpoint https://s3.example.com --path-style
  git s3fs init --bucket assets --region auto \
      --endpoint https://<account>.r2.cloudflarestorage.com \
      --public-url https://cdn.example.com
`

func cmdInit(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		bucket       = fs.String("bucket", "", "")
		region       = fs.String("region", "", "")
		prefix       = fs.String("prefix", "", "")
		endpoint     = fs.String("endpoint", "", "")
		pathStyle    = fs.Bool("path-style", false, "")
		publicURL    = fs.String("public-url", "", "")
		anonymous    = fs.Bool("anonymous", false, "")
		storageClass = fs.String("storage-class", "", "")
		sse          = fs.String("sse", "", "")
		sseKMS       = fs.String("sse-kms-key-id", "", "")
		local        = fs.Bool("local", false, "")
		global       = fs.Bool("global", false, "")
		force        = fs.Bool("force", false, "")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := newEnv()
	if err != nil {
		return err
	}
	if err := installFilters(e.repo, *global); err != nil {
		return err
	}
	installed, skipped, err := installHooks(e.repo, *force)
	if err != nil {
		return err
	}

	settings := [][2]string{
		{"bucket", *bucket},
		{"region", *region},
		{"prefix", strings.Trim(*prefix, "/")},
		{"endpoint", strings.TrimRight(*endpoint, "/")},
		{"publicurl", strings.TrimRight(*publicURL, "/")},
		{"storageclass", *storageClass},
		{"sse", *sse},
		{"ssekmskeyid", *sseKMS},
	}
	if *pathStyle {
		settings = append(settings, [2]string{"pathstyle", "true"})
	}
	if *anonymous {
		settings = append(settings, [2]string{"anonymous", "true"})
	}

	wrote := false
	if *local {
		for _, kv := range settings {
			if kv[1] == "" {
				continue
			}
			if err := e.repo.ConfigSet("s3fs."+kv[0], kv[1], false); err != nil {
				return err
			}
			wrote = true
		}
	} else {
		path := e.configFilePath()
		file, err := config.ParseFile(path)
		if err != nil {
			return err
		}
		for _, kv := range settings {
			if kv[1] == "" {
				continue
			}
			file.Set("s3fs."+kv[0], kv[1])
			wrote = true
		}
		if wrote {
			if err := file.WriteFile(path); err != nil {
				return err
			}
		}
	}

	fmt.Println("Installed the git-s3fs filter for this repository.")
	for _, h := range installed {
		fmt.Printf("Installed hook %s\n", h)
	}
	for _, h := range skipped {
		fmt.Printf("Left existing hook %s alone; add `git s3fs %s \"$@\"` to it, or re-run with --force\n", h, filepath.Base(h))
	}
	if wrote {
		if *local {
			fmt.Println("Wrote settings to .git/config (this clone only).")
		} else {
			fmt.Printf("Wrote settings to %s. Commit it so clones are configured automatically.\n", config.FileName)
		}
	}

	// Re-read so the summary reflects what was just written.
	switch e2, err := newEnv(); {
	case err != nil:
		return err
	case e2.cfg.Configured():
		if url, err := e2.cfg.ExampleObjectURL(); err == nil {
			fmt.Printf("\nObjects will be stored at:\n  %s\n", url)
		}
	default:
		fmt.Println("\nNo bucket configured yet. Re-run with --bucket and --region, or edit " + config.FileName + ".")
	}
	fmt.Println("\nNext: git s3fs track '*.psd'")
	return nil
}

const usageInstall = `
git s3fs install [--global] [--force]

Install the git-s3fs clean and smudge filters and the repository hooks. This
is the part of ` + "`git s3fs init`" + ` that touches git itself, without changing
any bucket settings. Run it after cloning a repository that already uses
git-s3fs, unless you installed the filters globally.
`

func cmdInstall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	global := fs.Bool("global", false, "")
	force := fs.Bool("force", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := newEnv()
	if err != nil {
		return err
	}
	if err := installFilters(e.repo, *global); err != nil {
		return err
	}
	installed, skipped, err := installHooks(e.repo, *force)
	if err != nil {
		return err
	}
	scope := "this repository"
	if *global {
		scope = "all repositories"
	}
	fmt.Printf("Installed the git-s3fs filter for %s.\n", scope)
	for _, h := range installed {
		fmt.Printf("Installed hook %s\n", h)
	}
	for _, h := range skipped {
		fmt.Printf("Left existing hook %s alone; re-run with --force to replace it\n", h)
	}
	return nil
}

const usageUninstall = `
git s3fs uninstall [--global]

Remove the git-s3fs filters and hooks. Pointer files and .gitattributes are
left untouched; use ` + "`git s3fs migrate export`" + ` first if you want the real
file contents back in the repository.
`

func cmdUninstall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	global := fs.Bool("global", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := newEnv()
	if err != nil {
		return err
	}
	for _, key := range []string{"process", "clean", "smudge", "required"} {
		if err := e.repo.ConfigUnset("filter."+attrs.Filter+"."+key, *global); err != nil {
			return err
		}
	}
	for _, name := range hookNames {
		path, err := hookPath(e.repo, name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), hookMarker) {
			if err := os.Remove(path); err != nil {
				return err
			}
			fmt.Printf("Removed hook %s\n", path)
		}
	}
	fmt.Println("Removed the git-s3fs filter.")
	return nil
}

// installFilters registers the clean and smudge filters with git.
//
// The `process` entry makes git use the long running filter protocol, which
// starts one git-s3fs process per git invocation instead of one per file.
func installFilters(repo *gitcmd.Repo, global bool) error {
	settings := [][2]string{
		{"filter." + attrs.Filter + ".process", "git-s3fs filter-process"},
		{"filter." + attrs.Filter + ".clean", "git-s3fs clean -- %f"},
		{"filter." + attrs.Filter + ".smudge", "git-s3fs smudge -- %f"},
		// required=true makes git fail loudly rather than silently committing
		// a pointer as if it were the real content.
		{"filter." + attrs.Filter + ".required", "true"},
	}
	for _, kv := range settings {
		if err := repo.ConfigSet(kv[0], kv[1], global); err != nil {
			return err
		}
	}
	return nil
}

const hookMarker = "git-s3fs-hook"

var hookNames = []string{"pre-push", "post-checkout", "post-merge"}

// hookPath resolves where a hook should live, honouring core.hooksPath.
func hookPath(repo *gitcmd.Repo, name string) (string, error) {
	dir := repo.GitPath("hooks")
	if custom, ok := repo.ConfigGet("core.hooksPath"); ok && custom != "" {
		if filepath.IsAbs(custom) {
			dir = custom
		} else {
			dir = filepath.Join(repo.Root, custom)
		}
	}
	return filepath.Join(dir, name), nil
}

// installHooks writes the repository hooks, refusing to clobber hooks that
// somebody else wrote unless force is set.
func installHooks(repo *gitcmd.Repo, force bool) (installed, skipped []string, err error) {
	for _, name := range hookNames {
		path, err := hookPath(repo, name)
		if err != nil {
			return nil, nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, nil, err
		}
		if existing, readErr := os.ReadFile(path); readErr == nil {
			if strings.Contains(string(existing), hookMarker) {
				// Already ours; rewrite in case the script changed.
			} else if !force {
				skipped = append(skipped, path)
				continue
			}
		}
		if err := os.WriteFile(path, []byte(hookScript(name)), 0o755); err != nil {
			return nil, nil, err
		}
		installed = append(installed, path)
	}
	return installed, skipped, nil
}

func hookScript(name string) string {
	return fmt.Sprintf(`#!/bin/sh
# %s: installed by git-s3fs. Safe to delete if you run `+"`git s3fs uninstall`"+`.
if ! command -v git-s3fs >/dev/null 2>&1; then
	printf >&2 "git-s3fs is not on PATH, so large files were not synchronised.\n"
	printf >&2 "Install it from https://github.com/SeriousBug/gits3fs or run: git s3fs uninstall\n"
	exit 2
fi
git s3fs %s "$@"
`, hookMarker, name)
}
