package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/SeriousBug/gits3fs/internal/config"
	"github.com/SeriousBug/gits3fs/internal/pointer"
	"github.com/SeriousBug/gits3fs/internal/version"
)

// stagedPointer reads the pointer git has in the index for a path.
func (e *env) stagedPointer(path string) (*pointer.Pointer, error) {
	raw, err := e.repo.RunRaw("cat-file", "blob", ":"+path)
	if err != nil {
		return nil, err
	}
	return pointer.Parse(raw)
}

const usageURL = `
git s3fs url <path|oid>...

Print the object URL for a tracked file or an object id. Handy for sharing a
direct link to an asset, or for fetching one with curl.

Examples:
  git s3fs url design/logo.psd
  curl -O "$(git s3fs url design/logo.psd)"
`

func cmdURL(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("url needs at least one path or object id")
	}
	e, err := newEnv()
	if err != nil {
		return err
	}
	for _, arg := range args {
		oid := arg
		if !isOID(arg) {
			p, err := e.pointerForPath(arg)
			if err != nil {
				return err
			}
			if p.URL != "" {
				// Prefer the URL the pointer actually records; it is what
				// anyone reading the repository will see.
				fmt.Println(p.URL)
				continue
			}
			oid = p.OID
		}
		url, err := e.cfg.ObjectURL(oid)
		if err != nil {
			return err
		}
		fmt.Println(url)
	}
	return nil
}

// pointerForPath finds the pointer for a working tree path, looking at the
// file itself first and falling back to the index.
func (e *env) pointerForPath(path string) (*pointer.Pointer, error) {
	if data, err := os.ReadFile(e.repo.Path(path)); err == nil && len(data) <= pointer.MaxSize {
		if p, err := pointer.Parse(data); err == nil {
			return p, nil
		}
	}
	p, err := e.stagedPointer(path)
	if err != nil {
		return nil, fmt.Errorf("%s is not tracked by git-s3fs", path)
	}
	return p, nil
}

func isOID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

const usageEnv = `
git s3fs env

Print the resolved configuration and where each setting came from.
`

func cmdEnv(ctx context.Context, args []string) error {
	e, err := newEnv()
	if err != nil {
		return err
	}
	c := e.cfg

	rows := [][3]string{
		{"bucket", c.Bucket, c.SourceOf("bucket").String()},
		{"region", c.Region, c.SourceOf("region").String()},
		{"prefix", c.Prefix, c.SourceOf("prefix").String()},
		{"endpoint", c.Endpoint, c.SourceOf("endpoint").String()},
		{"pathStyle", fmt.Sprint(c.PathStyle), c.SourceOf("pathstyle").String()},
		{"publicUrl", c.PublicURL, c.SourceOf("publicurl").String()},
		{"anonymous", fmt.Sprint(c.Anonymous), c.SourceOf("anonymous").String()},
		{"profile", c.Profile, c.SourceOf("profile").String()},
		{"storageClass", c.StorageClass, c.SourceOf("storageclass").String()},
		{"sse", c.SSE, c.SourceOf("sse").String()},
		{"concurrency", fmt.Sprint(c.Concurrency), c.SourceOf("concurrency").String()},
		{"chunkSize", config.FormatSize(c.ChunkSize), c.SourceOf("chunksize").String()},
		{"lazy", fmt.Sprint(c.Lazy), c.SourceOf("lazy").String()},
	}
	for _, r := range rows {
		value := r[1]
		if value == "" {
			value = "(unset)"
		}
		fmt.Printf("%-13s %-45s [%s]\n", r[0], value, r[2])
	}

	fmt.Printf("\n%-13s %s\n", "cache", e.cache.Root())
	fmt.Printf("%-13s %s\n", "git dir", e.repo.GitDir)
	if c.Configured() {
		if url, err := c.ExampleObjectURL(); err == nil {
			fmt.Printf("%-13s %s\n", "object URL", url)
		}
	}
	if c.URLDrift() {
		fmt.Printf("\nWarning: your local settings differ from %s. Pointers you create record\n", config.FileName)
		fmt.Println("the shared location, but your uploads go to your local one.")
	}
	return nil
}

const usageVersion = `
git s3fs version

Print the version of git-s3fs.
`

func cmdVersion(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	fmt.Println(version.String())
	return nil
}
