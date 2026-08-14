// Package cache stores object contents locally, inside .git, so that a file
// staged by the clean filter can be pushed later and a file fetched once is
// not fetched again.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Cache is a content addressed store on the local filesystem.
type Cache struct {
	root string
}

// New returns a cache rooted at dir, typically .git/s3fs.
func New(dir string) *Cache { return &Cache{root: dir} }

// Root returns the cache directory.
func (c *Cache) Root() string { return c.root }

// Path returns where an object is stored, whether or not it exists.
func (c *Cache) Path(oid string) string {
	if len(oid) < 4 {
		return filepath.Join(c.root, "objects", oid)
	}
	return filepath.Join(c.root, "objects", oid[0:2], oid[2:4], oid)
}

// Has reports whether the object is present locally.
func (c *Cache) Has(oid string) bool {
	st, err := os.Stat(c.Path(oid))
	return err == nil && st.Mode().IsRegular()
}

// Size returns the stored size of an object.
func (c *Cache) Size(oid string) (int64, error) {
	st, err := os.Stat(c.Path(oid))
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// Open opens an object for reading.
func (c *Cache) Open(oid string) (*os.File, error) { return os.Open(c.Path(oid)) }

// Remove deletes an object, tolerating a missing one.
func (c *Cache) Remove(oid string) error {
	err := os.Remove(c.Path(oid))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Writer accumulates content and hashes it on the way in.
type Writer struct {
	cache *Cache
	tmp   *os.File
	hash  interface {
		io.Writer
		Sum([]byte) []byte
	}
	size   int64
	closed bool
}

// NewWriter starts writing a new object. The content is streamed to a
// temporary file so that objects larger than memory are handled naturally.
func (c *Cache) NewWriter() (*Writer, error) {
	tmpDir := filepath.Join(c.root, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(tmpDir, "obj-*")
	if err != nil {
		return nil, err
	}
	return &Writer{cache: c, tmp: tmp, hash: sha256.New()}, nil
}

func (w *Writer) Write(p []byte) (int, error) {
	n, err := w.tmp.Write(p)
	if n > 0 {
		w.hash.Write(p[:n])
		w.size += int64(n)
	}
	return n, err
}

// OID returns the digest of everything written so far.
func (w *Writer) OID() string { return hex.EncodeToString(w.hash.Sum(nil)) }

// Size returns the number of bytes written so far.
func (w *Writer) Size() int64 { return w.size }

// Commit moves the temporary file into the cache and returns the object id.
func (w *Writer) Commit() (oid string, size int64, err error) {
	if w.closed {
		return "", 0, errors.New("writer already finished")
	}
	w.closed = true
	if err := w.tmp.Close(); err != nil {
		os.Remove(w.tmp.Name())
		return "", 0, err
	}
	oid = w.OID()
	dest := w.cache.Path(oid)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		os.Remove(w.tmp.Name())
		return "", 0, err
	}
	if _, err := os.Stat(dest); err == nil {
		// Already have it; identical content by construction.
		os.Remove(w.tmp.Name())
		return oid, w.size, nil
	}
	if err := os.Chmod(w.tmp.Name(), 0o644); err != nil {
		os.Remove(w.tmp.Name())
		return "", 0, err
	}
	if err := os.Rename(w.tmp.Name(), dest); err != nil {
		os.Remove(w.tmp.Name())
		return "", 0, err
	}
	return oid, w.size, nil
}

// Abort discards a partially written object.
func (w *Writer) Abort() {
	if w.closed {
		return
	}
	w.closed = true
	w.tmp.Close()
	os.Remove(w.tmp.Name())
}

// Put stores content whose object id is already known, verifying as it goes.
// A mismatch means the remote handed us the wrong bytes, and the object is
// discarded rather than cached.
func (c *Cache) Put(oid string, r io.Reader) (int64, error) {
	w, err := c.NewWriter()
	if err != nil {
		return 0, err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Abort()
		return 0, err
	}
	if got := w.OID(); got != oid {
		w.Abort()
		return 0, fmt.Errorf("content does not match its object id: expected %s, got %s", oid, got)
	}
	_, size, err := w.Commit()
	return size, err
}

// Verify rehashes a cached object and reports whether it is intact.
func (c *Cache) Verify(oid string) error {
	f, err := c.Open(oid)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != oid {
		return fmt.Errorf("cached object %s is corrupt (hashes to %s)", oid, got)
	}
	return nil
}

// Entry describes one cached object.
type Entry struct {
	OID  string
	Size int64
}

// List walks every object in the cache.
func (c *Cache) List() ([]Entry, error) {
	var out []Entry
	root := filepath.Join(c.root, "objects")
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if len(name) != 64 || strings.ContainsAny(name, "-.") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		out = append(out, Entry{OID: name, Size: info.Size()})
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return out, nil
}

// CleanTmp removes leftover temporary files from interrupted transfers.
func (c *Cache) CleanTmp() error {
	entries, err := os.ReadDir(filepath.Join(c.root, "tmp"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		os.Remove(filepath.Join(c.root, "tmp", e.Name()))
	}
	return nil
}
