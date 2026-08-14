package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRepoFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadFromRepoFile(t *testing.T) {
	dir := writeRepoFile(t, `
# a comment
[s3fs]
	bucket = my-assets
	region = eu-west-1
	prefix = /repos/site/
	concurrency = 3
	pathStyle = true
`)
	c, err := Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Bucket != "my-assets" || c.Region != "eu-west-1" {
		t.Errorf("bucket/region = %q/%q", c.Bucket, c.Region)
	}
	if c.Prefix != "repos/site" {
		t.Errorf("prefix = %q, want the slashes trimmed", c.Prefix)
	}
	if c.Concurrency != 3 {
		t.Errorf("concurrency = %d", c.Concurrency)
	}
	if !c.PathStyle {
		t.Error("pathStyle should be true")
	}
	if !c.RepoFileExists() {
		t.Error("RepoFileExists should be true")
	}
	if got := c.SourceOf("bucket"); got != SourceRepoFile {
		t.Errorf("SourceOf(bucket) = %v, want %v", got, SourceRepoFile)
	}
}

func TestPrecedence(t *testing.T) {
	dir := writeRepoFile(t, "[s3fs]\n\tbucket = from-file\n\tregion = from-file\n")
	gitConfig := func(key string) (string, bool) {
		if key == "s3fs.bucket" {
			return "from-git-config", true
		}
		return "", false
	}
	t.Setenv("GITS3FS_REGION", "from-env")

	c, err := Load(dir, gitConfig)
	if err != nil {
		t.Fatal(err)
	}
	if c.Bucket != "from-git-config" {
		t.Errorf("git config should beat the repository file, got %q", c.Bucket)
	}
	if c.Region != "from-env" {
		t.Errorf("the environment should beat everything, got %q", c.Region)
	}
	if got := c.SourceOf("region"); got != SourceEnv {
		t.Errorf("SourceOf(region) = %v", got)
	}
}

func TestURLConfigIgnoresLocalLayers(t *testing.T) {
	// Pointer URLs have to be identical on every machine, so only the
	// committed file may influence them.
	dir := writeRepoFile(t, "[s3fs]\n\tbucket = shared\n\tregion = us-east-1\n")
	gitConfig := func(key string) (string, bool) {
		if key == "s3fs.bucket" {
			return "personal", true
		}
		return "", false
	}
	c, err := Load(dir, gitConfig)
	if err != nil {
		t.Fatal(err)
	}
	if c.Bucket != "personal" {
		t.Fatalf("resolved bucket = %q", c.Bucket)
	}
	if got := c.URLConfig().Bucket; got != "shared" {
		t.Errorf("URLConfig bucket = %q, want the committed one", got)
	}
	if !c.URLDrift() {
		t.Error("URLDrift should report the disagreement")
	}
}

func TestNoDriftWhenLayersAgree(t *testing.T) {
	dir := writeRepoFile(t, "[s3fs]\n\tbucket = shared\n\tregion = us-east-1\n")
	c, err := Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.URLDrift() {
		t.Error("URLDrift should be false when nothing overrides the file")
	}
}

func TestDefaults(t *testing.T) {
	c, err := Load(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Concurrency != DefaultConcurrency || c.ChunkSize != DefaultChunkSize {
		t.Errorf("defaults not applied: %d %d", c.Concurrency, c.ChunkSize)
	}
	if c.Configured() {
		t.Error("an empty directory should not count as configured")
	}
	if c.RepoFileExists() {
		t.Error("RepoFileExists should be false")
	}
	if err := c.Validate(); err == nil {
		t.Error("Validate should complain about the missing bucket")
	}
}

func TestValidateNeedsRegionOrEndpoint(t *testing.T) {
	dir := writeRepoFile(t, "[s3fs]\n\tbucket = b\n")
	c, _ := Load(dir, nil)
	if err := c.Validate(); err == nil {
		t.Error("Validate should require a region or an endpoint")
	}

	dir = writeRepoFile(t, "[s3fs]\n\tbucket = b\n\tendpoint = https://s3.example.com\n")
	c, _ = Load(dir, nil)
	if err := c.Validate(); err != nil {
		t.Errorf("an endpoint should satisfy Validate: %v", err)
	}
}

func TestInvalidValuesAreIgnored(t *testing.T) {
	dir := writeRepoFile(t, "[s3fs]\n\tbucket = b\n\tconcurrency = lots\n")
	c, err := Load(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Concurrency != DefaultConcurrency {
		t.Errorf("a bad concurrency should fall back to the default, got %d", c.Concurrency)
	}
}

func TestFileRoundTrip(t *testing.T) {
	f := NewFile()
	f.Set("s3fs.bucket", "my-assets")
	f.Set("s3fs.region", "us-east-1")

	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := f.WriteFile(path); err != nil {
		t.Fatal(err)
	}
	got, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := got.Get("s3fs.bucket"); v != "my-assets" {
		t.Errorf("bucket = %q", v)
	}
	if v, _ := got.Get("s3fs.region"); v != "us-east-1" {
		t.Errorf("region = %q", v)
	}
	if !strings.Contains(string(f.Bytes()), "[s3fs]") {
		t.Errorf("rendered file has no section header:\n%s", f.Bytes())
	}
}

func TestParseValueForms(t *testing.T) {
	f, err := Parse(strings.NewReader(`
[s3fs]
	quoted = "a value ; with punctuation"
	commented = plain # trailing
	bare
	Mixed = CaseKept
`))
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"s3fs.quoted":    "a value ; with punctuation",
		"s3fs.commented": "plain",
		"s3fs.bare":      "true",
		"s3fs.mixed":     "CaseKept",
	} {
		if got, _ := f.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestParseSize(t *testing.T) {
	cases := map[string]int64{
		"1024": 1024, "1k": 1 << 10, "8mb": 8 << 20, "2G": 2 << 30, "512B": 512,
	}
	for in, want := range cases {
		got, err := ParseSize(in)
		if err != nil {
			t.Errorf("ParseSize(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseSize(%q) = %d, want %d", in, got, want)
		}
	}
	for _, bad := range []string{"", "-1", "0", "many", "12x"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) should have failed", bad)
		}
	}
}

func TestFormatSize(t *testing.T) {
	for in, want := range map[int64]string{
		0: "0 B", 512: "512 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 1 << 20: "1.0 MiB", 3 << 30: "3.0 GiB",
	} {
		if got := FormatSize(in); got != want {
			t.Errorf("FormatSize(%d) = %q, want %q", in, got, want)
		}
	}
}
