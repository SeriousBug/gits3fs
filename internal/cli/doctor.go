package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/SeriousBug/gits3fs/internal/attrs"
	"github.com/SeriousBug/gits3fs/internal/config"
	"github.com/SeriousBug/gits3fs/internal/gitcmd"
	"github.com/SeriousBug/gits3fs/internal/store"
)

const usageDoctor = `
git s3fs doctor

Check that this repository is set up correctly: filters installed, hooks in
place, configuration committed, credentials available, bucket reachable.
Each check reports ok, a warning, or a failure with what to do about it.
`

type checkResult int

const (
	ok checkResult = iota
	warning
	failure
)

func (r checkResult) mark() string {
	switch r {
	case ok:
		return "ok  "
	case warning:
		return "warn"
	default:
		return "FAIL"
	}
}

type doctor struct {
	failures int
	warnings int
}

func (d *doctor) check(result checkResult, name, detail string) {
	fmt.Printf("[%s] %s", result.mark(), name)
	if detail != "" {
		fmt.Printf(": %s", detail)
	}
	fmt.Println()
	switch result {
	case failure:
		d.failures++
	case warning:
		d.warnings++
	}
}

func cmdDoctor(ctx context.Context, args []string) error {
	d := &doctor{}

	if v, err := gitcmd.Version(); err == nil {
		d.check(ok, "git", v)
	} else {
		d.check(failure, "git", "not found on PATH")
		return fmt.Errorf("git is required")
	}

	if path, err := exec.LookPath("git-s3fs"); err == nil {
		d.check(ok, "git-s3fs on PATH", path)
	} else {
		d.check(failure, "git-s3fs on PATH", "git cannot run the filters or hooks without it")
	}

	e, err := newEnv()
	if err != nil {
		d.check(failure, "repository", err.Error())
		return err
	}
	d.check(ok, "repository", e.repo.GitDir)

	// Filters.
	if v, set := e.repo.ConfigGet("filter." + attrs.Filter + ".process"); set && strings.Contains(v, "filter-process") {
		d.check(ok, "clean/smudge filter", v)
	} else if _, set := e.repo.ConfigGet("filter." + attrs.Filter + ".clean"); set {
		d.check(warning, "clean/smudge filter", "the fast long running filter is not configured; run `git s3fs install`")
	} else {
		d.check(failure, "clean/smudge filter", "not installed; run `git s3fs install`")
	}

	// Hooks.
	var missingHooks []string
	for _, name := range hookNames {
		path, err := hookPath(e.repo, name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(data), hookMarker) {
			missingHooks = append(missingHooks, name)
		}
	}
	if len(missingHooks) == 0 {
		d.check(ok, "hooks", strings.Join(hookNames, ", "))
	} else {
		d.check(warning, "hooks", "missing "+strings.Join(missingHooks, ", ")+"; run `git s3fs install --force`")
	}

	// Tracked patterns.
	patterns, err := trackedPatterns(e)
	if err != nil {
		return err
	}
	if len(patterns) > 0 {
		names := make([]string, 0, len(patterns))
		for _, p := range patterns {
			names = append(names, p.Pattern)
		}
		d.check(ok, "tracked patterns", strings.Join(names, ", "))
	} else {
		d.check(warning, "tracked patterns", "nothing is tracked; run `git s3fs track '*.psd'`")
	}

	// Configuration.
	switch {
	case !e.cfg.Configured():
		d.check(failure, "configuration", "no bucket; run `git s3fs init --bucket <name> --region <region>`")
	case !e.cfg.RepoFileExists():
		d.check(warning, "configuration", "no committed "+config.FileName+"; other clones will not know where objects live")
	case e.cfg.URLDrift():
		d.check(warning, "configuration", "local settings disagree with "+config.FileName+"; pointers you write will not match where you upload")
	default:
		d.check(ok, "configuration", config.FileName+" is committed")
	}

	if e.cfg.Configured() {
		if err := e.cfg.Validate(); err != nil {
			d.check(failure, "configuration", err.Error())
		} else {
			checkBucket(ctx, d, e)
		}
	}

	entries, err := e.cache.List()
	if err == nil {
		var total int64
		for _, ent := range entries {
			total += ent.Size
		}
		d.check(ok, "local cache", fmt.Sprintf("%d object(s), %s", len(entries), config.FormatSize(total)))
	}

	fmt.Println()
	switch {
	case d.failures > 0:
		return fmt.Errorf("%d check(s) failed, %d warning(s)", d.failures, d.warnings)
	case d.warnings > 0:
		fmt.Printf("No failures, %d warning(s).\n", d.warnings)
	default:
		fmt.Println("Everything looks good.")
	}
	return nil
}

func checkBucket(ctx context.Context, d *doctor, e *env) {
	s, err := store.NewS3(ctx, e.cfg)
	if err != nil {
		d.check(failure, "credentials", err.Error())
		return
	}
	if s.Anonymous() {
		d.check(warning, "credentials", "none found; reads may work on a public bucket, uploads will not")
	} else {
		d.check(ok, "credentials", "resolved from the AWS credential chain")
	}
	if err := s.CheckBucket(ctx); err != nil {
		d.check(failure, "bucket "+e.cfg.Bucket, summarise(err))
		return
	}
	d.check(ok, "bucket", s.Describe())
}

// summarise shortens the very long errors the AWS SDK produces.
func summarise(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, "https response error "); i >= 0 {
		msg = msg[i:]
	}
	if len(msg) > 200 {
		msg = msg[:200] + "..."
	}
	return msg
}
