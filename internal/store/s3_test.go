package store

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SeriousBug/gits3fs/internal/config"
	"github.com/SeriousBug/gits3fs/internal/testutil/fakes3"
)

const testOID = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

// newStore starts a fake S3 endpoint and a store pointed at it.
func newStore(t *testing.T, prefix string) (*S3, *fakes3.Server) {
	t.Helper()
	srv := fakes3.New()
	t.Cleanup(srv.Close)

	// Credentials have to resolve to something, or the store falls back to
	// anonymous access and refuses to upload.
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	cfg := &config.Config{
		Bucket:      "test-bucket",
		Prefix:      prefix,
		Region:      "us-east-1",
		Endpoint:    srv.Endpoint(),
		PathStyle:   true,
		Concurrency: 4,
		ChunkSize:   config.DefaultChunkSize,
	}
	s, err := NewS3(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}
	return s, srv
}

func TestPutGetHas(t *testing.T) {
	ctx := context.Background()
	s, srv := newStore(t, "")

	exists, _, err := s.Has(ctx, testOID)
	if err != nil {
		t.Fatalf("Has: %v", err)
	}
	if exists {
		t.Fatal("the object should not exist yet")
	}

	content := "hello from git-s3fs"
	if err := s.Put(ctx, testOID, strings.NewReader(content), int64(len(content))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	wantKey := "objects/9f/86/" + testOID
	if _, ok := srv.Get(wantKey); !ok {
		t.Errorf("object was not stored at %q; keys are %v", wantKey, keys(srv))
	}

	exists, size, err := s.Has(ctx, testOID)
	if err != nil {
		t.Fatalf("Has: %v", err)
	}
	if !exists || size != int64(len(content)) {
		t.Errorf("Has = %v, %d", exists, size)
	}

	var got bytes.Buffer
	if err := s.Get(ctx, testOID, &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.String() != content {
		t.Errorf("Get = %q, want %q", got.String(), content)
	}
}

func TestPrefixIsApplied(t *testing.T) {
	ctx := context.Background()
	s, srv := newStore(t, "repos/site")
	if err := s.Put(ctx, testOID, strings.NewReader("x"), 1); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.Get("repos/site/objects/9f/86/" + testOID); !ok {
		t.Errorf("the prefix was not applied; keys are %v", keys(srv))
	}
}

func TestGetMissingIsNotFound(t *testing.T) {
	s, _ := newStore(t, "")
	err := s.Get(context.Background(), testOID, &bytes.Buffer{})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestMultipartUpload(t *testing.T) {
	ctx := context.Background()
	srv := fakes3.New()
	t.Cleanup(srv.Close)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	// The SDK's minimum part size is 5 MiB, so 12 MiB spans three parts.
	cfg := &config.Config{
		Bucket: "test-bucket", Region: "us-east-1", Endpoint: srv.Endpoint(),
		PathStyle: true, Concurrency: 4, ChunkSize: 5 << 20,
	}
	s, err := NewS3(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}

	content := bytes.Repeat([]byte("git-s3fs"), (12<<20)/8)
	if err := s.Put(ctx, testOID, bytes.NewReader(content), int64(len(content))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	stored, ok := srv.Get("objects/9f/86/" + testOID)
	if !ok {
		t.Fatalf("nothing was stored; keys are %v", keys(srv))
	}
	if !bytes.Equal(stored, content) {
		t.Errorf("multipart upload reassembled %d bytes, want %d", len(stored), len(content))
	}
}

func TestCheckBucket(t *testing.T) {
	s, _ := newStore(t, "")
	if err := s.CheckBucket(context.Background()); err != nil {
		t.Errorf("CheckBucket: %v", err)
	}
}

func TestDescribe(t *testing.T) {
	s, _ := newStore(t, "assets")
	got := s.Describe()
	if !strings.Contains(got, "s3://test-bucket/assets") {
		t.Errorf("Describe = %q", got)
	}
}

func keys(srv *fakes3.Server) []string {
	var out []string
	for k := range srv.Objects() {
		out = append(out, k)
	}
	return out
}
