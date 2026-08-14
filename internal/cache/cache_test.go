package cache

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	helloOID = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	otherOID = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
)

func newCache(t *testing.T) *Cache {
	t.Helper()
	return New(filepath.Join(t.TempDir(), "s3fs"))
}

func TestWriterCommit(t *testing.T) {
	c := newCache(t)
	w, err := c.NewWriter()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "hello"); err != nil {
		t.Fatal(err)
	}
	oid, size, err := w.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if oid != helloOID {
		t.Errorf("oid = %s", oid)
	}
	if size != 5 {
		t.Errorf("size = %d", size)
	}
	if !c.Has(oid) {
		t.Error("the object is not in the cache")
	}

	f, err := c.Open(oid)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, _ := io.ReadAll(f)
	if string(got) != "hello" {
		t.Errorf("content = %q", got)
	}
}

func TestWriterFansOutByOID(t *testing.T) {
	c := newCache(t)
	want := filepath.Join(c.Root(), "objects", helloOID[0:2], helloOID[2:4], helloOID)
	if got := c.Path(helloOID); got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

func TestWriterAbortLeavesNothing(t *testing.T) {
	c := newCache(t)
	w, err := c.NewWriter()
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(w, "hello")
	w.Abort()

	if c.Has(helloOID) {
		t.Error("an aborted write should not be committed")
	}
	entries, err := os.ReadDir(filepath.Join(c.Root(), "tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("temporary files were left behind: %v", entries)
	}
}

func TestCommitTwiceIsSafe(t *testing.T) {
	c := newCache(t)
	for range 2 {
		w, err := c.NewWriter()
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, "hello")
		if _, _, err := w.Commit(); err != nil {
			t.Fatalf("committing identical content twice failed: %v", err)
		}
	}
	if size, err := c.Size(helloOID); err != nil || size != 5 {
		t.Errorf("Size = %d, %v", size, err)
	}
}

func TestPutVerifiesContent(t *testing.T) {
	c := newCache(t)
	if _, err := c.Put(helloOID, strings.NewReader("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !c.Has(helloOID) {
		t.Error("the object is missing")
	}

	// A backend that hands back the wrong bytes must not poison the cache.
	if _, err := c.Put(otherOID, strings.NewReader("hello")); err == nil {
		t.Error("Put should reject content that does not match its object id")
	}
	if c.Has(otherOID) {
		t.Error("mismatched content was cached anyway")
	}
}

func TestVerifyDetectsCorruption(t *testing.T) {
	c := newCache(t)
	if _, err := c.Put(helloOID, strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	if err := c.Verify(helloOID); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if err := os.WriteFile(c.Path(helloOID), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.Verify(helloOID); err == nil {
		t.Error("Verify should have reported corruption")
	}
}

func TestListAndRemove(t *testing.T) {
	c := newCache(t)
	if _, err := c.Put(helloOID, strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	entries, err := c.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].OID != helloOID || entries[0].Size != 5 {
		t.Fatalf("List = %+v", entries)
	}

	if err := c.Remove(helloOID); err != nil {
		t.Fatal(err)
	}
	if c.Has(helloOID) {
		t.Error("the object survived removal")
	}
	if err := c.Remove(helloOID); err != nil {
		t.Errorf("removing a missing object should be fine: %v", err)
	}
}

func TestListEmptyCache(t *testing.T) {
	entries, err := newCache(t).List()
	if err != nil {
		t.Fatalf("listing a cache that was never written should work: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("List = %+v", entries)
	}
}

func TestCleanTmp(t *testing.T) {
	c := newCache(t)
	tmpDir := filepath.Join(c.Root(), "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(tmpDir, "obj-interrupted")
	if err := os.WriteFile(stale, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.CleanTmp(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("the stale temporary file survived")
	}
}
