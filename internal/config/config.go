// Package config resolves git-s3fs settings from the layers a repository can
// configure them in.
//
// Precedence, highest first:
//
//  1. Environment variables (GITS3FS_*)
//  2. git config (s3fs.*), local then global
//  3. The committed .gits3fs file at the repository root
//  4. Built in defaults
//
// Credentials are deliberately absent from every layer: they always come from
// the standard AWS credential chain, so a repository can never leak a secret
// by committing its configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Source records where a resolved value came from, so that commands such as
// `git s3fs env` and `git s3fs doctor` can explain themselves.
type Source int

const (
	SourceDefault Source = iota
	SourceRepoFile
	SourceGitConfig
	SourceEnv
)

func (s Source) String() string {
	switch s {
	case SourceRepoFile:
		return FileName
	case SourceGitConfig:
		return "git config"
	case SourceEnv:
		return "environment"
	default:
		return "default"
	}
}

// Defaults.
const (
	DefaultConcurrency = 8
	DefaultChunkSize   = 16 << 20 // 16 MiB multipart part size
)

// Config is the resolved configuration for a repository.
type Config struct {
	// Bucket is the S3 bucket holding the objects.
	Bucket string
	// Prefix is an optional key prefix within the bucket, without a trailing
	// slash.
	Prefix string
	// Region is the S3 region.
	Region string
	// Endpoint overrides the S3 endpoint, for S3 compatible services such as
	// MinIO, Cloudflare R2 or Backblaze B2.
	Endpoint string
	// PathStyle forces path style addressing (endpoint/bucket/key) instead of
	// virtual host addressing (bucket.endpoint/key).
	PathStyle bool
	// PublicURL, when set, is the base URL written into pointer files instead
	// of the derived S3 URL. Use it to point readers at a CDN or a custom
	// domain in front of the bucket.
	PublicURL string
	// Anonymous skips credential resolution entirely and reads the bucket
	// unauthenticated. Useful for public, read only asset buckets.
	Anonymous bool
	// Profile selects a named AWS profile.
	Profile string
	// StorageClass is applied to uploaded objects, for example STANDARD_IA or
	// GLACIER_IR.
	StorageClass string
	// SSE is the server side encryption algorithm to request, for example
	// AES256 or aws:kms.
	SSE string
	// SSEKMSKeyID selects the KMS key when SSE is aws:kms.
	SSEKMSKeyID string
	// Concurrency bounds parallel transfers.
	Concurrency int
	// ChunkSize is the multipart upload part size in bytes.
	ChunkSize int64
	// Lazy makes the smudge filter leave pointers in place during checkout,
	// so that a clone is fast and content is fetched later with
	// `git s3fs pull`.
	Lazy bool

	// sources records the origin of each field, keyed by the canonical
	// lowercase key name (for example "bucket" or "publicurl").
	sources map[string]Source
	// repoFile holds the parsed .gits3fs, which is the only layer allowed to
	// influence the URL written into pointers.
	repoFile *File
	// repoFileExists reports whether .gits3fs was present on disk.
	repoFileExists bool
}

// GitConfigLookup reads a git config value. It is a function so that config
// resolution stays testable without a git repository.
type GitConfigLookup func(key string) (string, bool)

// Load resolves configuration for the repository rooted at root.
func Load(root string, gitConfig GitConfigLookup) (*Config, error) {
	path := filepath.Join(root, FileName)
	repoFile, err := ParseFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	_, statErr := os.Stat(path)

	c := &Config{
		Concurrency:    DefaultConcurrency,
		ChunkSize:      DefaultChunkSize,
		sources:        map[string]Source{},
		repoFile:       repoFile,
		repoFileExists: statErr == nil,
	}
	c.resolve(repoFile, gitConfig, os.Getenv)
	return c, nil
}

// field describes one setting and how to apply a string value to the Config.
type field struct {
	key   string // canonical lowercase key, used in .gits3fs and git config
	env   string // environment variable name
	apply func(c *Config, v string) error
}

func fields() []field {
	return []field{
		{"bucket", "GITS3FS_BUCKET", func(c *Config, v string) error { c.Bucket = v; return nil }},
		{"prefix", "GITS3FS_PREFIX", func(c *Config, v string) error {
			c.Prefix = strings.Trim(v, "/")
			return nil
		}},
		{"region", "GITS3FS_REGION", func(c *Config, v string) error { c.Region = v; return nil }},
		{"endpoint", "GITS3FS_ENDPOINT", func(c *Config, v string) error {
			c.Endpoint = strings.TrimRight(v, "/")
			return nil
		}},
		{"pathstyle", "GITS3FS_PATH_STYLE", func(c *Config, v string) error {
			b, err := parseBool(v)
			c.PathStyle = b
			return err
		}},
		{"publicurl", "GITS3FS_PUBLIC_URL", func(c *Config, v string) error {
			c.PublicURL = strings.TrimRight(v, "/")
			return nil
		}},
		{"anonymous", "GITS3FS_ANONYMOUS", func(c *Config, v string) error {
			b, err := parseBool(v)
			c.Anonymous = b
			return err
		}},
		{"profile", "GITS3FS_PROFILE", func(c *Config, v string) error { c.Profile = v; return nil }},
		{"storageclass", "GITS3FS_STORAGE_CLASS", func(c *Config, v string) error { c.StorageClass = v; return nil }},
		{"sse", "GITS3FS_SSE", func(c *Config, v string) error { c.SSE = v; return nil }},
		{"ssekmskeyid", "GITS3FS_SSE_KMS_KEY_ID", func(c *Config, v string) error { c.SSEKMSKeyID = v; return nil }},
		{"concurrency", "GITS3FS_CONCURRENCY", func(c *Config, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("concurrency: %w", err)
			}
			if n < 1 {
				return fmt.Errorf("concurrency must be at least 1, got %d", n)
			}
			c.Concurrency = n
			return nil
		}},
		{"chunksize", "GITS3FS_CHUNK_SIZE", func(c *Config, v string) error {
			n, err := ParseSize(v)
			if err != nil {
				return fmt.Errorf("chunksize: %w", err)
			}
			c.ChunkSize = n
			return nil
		}},
		{"lazy", "GITS3FS_LAZY", func(c *Config, v string) error {
			b, err := parseBool(v)
			c.Lazy = b
			return err
		}},
	}
}

func (c *Config) resolve(repoFile *File, gitConfig GitConfigLookup, getenv func(string) string) {
	for _, f := range fields() {
		// Lowest precedence first, so later layers overwrite earlier ones.
		if v, ok := repoFile.Get("s3fs." + f.key); ok {
			c.set(f, v, SourceRepoFile)
		}
		if gitConfig != nil {
			if v, ok := gitConfig("s3fs." + f.key); ok && v != "" {
				c.set(f, v, SourceGitConfig)
			}
		}
		if v := getenv(f.env); v != "" {
			c.set(f, v, SourceEnv)
		}
	}
}

func (c *Config) set(f field, v string, src Source) {
	if err := f.apply(c, v); err != nil {
		fmt.Fprintf(os.Stderr, "git-s3fs: ignoring invalid %s from %s: %v\n", f.key, src, err)
		return
	}
	c.sources[f.key] = src
}

// SourceOf reports where a setting was resolved from.
func (c *Config) SourceOf(key string) Source { return c.sources[strings.ToLower(key)] }

// RepoFileExists reports whether the repository has a committed .gits3fs.
func (c *Config) RepoFileExists() bool { return c.repoFileExists }

// Configured reports whether enough is set to talk to a bucket.
func (c *Config) Configured() bool { return c.Bucket != "" }

// Validate checks that the configuration can actually be used for transfers.
func (c *Config) Validate() error {
	if c.Bucket == "" {
		return fmt.Errorf("no bucket configured; run `git s3fs init --bucket <name> --region <region>`")
	}
	if c.Region == "" && c.Endpoint == "" {
		return fmt.Errorf("no region or endpoint configured; set s3fs.region (AWS) or s3fs.endpoint (S3 compatible services)")
	}
	return nil
}

// URLConfig returns a copy of the configuration restricted to the layers that
// are shared by everyone working in the repository, namely the committed
// .gits3fs file.
//
// Pointer URLs are generated from this restricted view on purpose. The clean
// filter must be deterministic: if the URL depended on a developer's personal
// git config or environment, two developers staging the same file would
// produce different pointers and git would report phantom changes.
func (c *Config) URLConfig() *Config {
	u := &Config{sources: map[string]Source{}, repoFile: c.repoFile, repoFileExists: c.repoFileExists}
	u.resolve(c.repoFile, nil, func(string) string { return "" })
	return u
}

// URLDrift reports whether the object URL that would be written into pointers
// differs from the one implied by the fully resolved configuration. That
// happens when a user overrides the bucket locally, and it means their commits
// would carry URLs that disagree with where their bytes actually went.
func (c *Config) URLDrift() bool {
	u := c.URLConfig()
	return u.Bucket != c.Bucket || u.Prefix != c.Prefix || u.Region != c.Region ||
		u.Endpoint != c.Endpoint || u.PublicURL != c.PublicURL || u.PathStyle != c.PathStyle
}

func parseBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off", "":
		return false, nil
	}
	return false, fmt.Errorf("%q is not a boolean", v)
}

// ParseSize accepts a byte count with an optional binary unit suffix, such as
// "8mb", "512k" or "1073741824".
func ParseSize(v string) (int64, error) {
	s := strings.TrimSpace(strings.ToLower(v))
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "kb"), strings.HasSuffix(s, "k"):
		mult, s = 1<<10, strings.TrimSuffix(strings.TrimSuffix(s, "kb"), "k")
	case strings.HasSuffix(s, "mb"), strings.HasSuffix(s, "m"):
		mult, s = 1<<20, strings.TrimSuffix(strings.TrimSuffix(s, "mb"), "m")
	case strings.HasSuffix(s, "gb"), strings.HasSuffix(s, "g"):
		mult, s = 1<<30, strings.TrimSuffix(strings.TrimSuffix(s, "gb"), "g")
	case strings.HasSuffix(s, "b"):
		s = strings.TrimSuffix(s, "b")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a byte size", v)
	}
	if n <= 0 {
		return 0, fmt.Errorf("size must be positive, got %q", v)
	}
	return n * mult, nil
}

// FormatSize renders a byte count for humans.
func FormatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
