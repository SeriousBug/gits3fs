// Package store talks to the object storage backend holding the large files.
package store

import (
	"context"
	"errors"
	"io"
)

// ErrNotFound means the object is not in the backend.
var ErrNotFound = errors.New("object not found")

// Store is the object storage backend.
type Store interface {
	// Has reports whether an object is already stored, and its size.
	Has(ctx context.Context, oid string) (bool, int64, error)
	// Get streams an object into w.
	Get(ctx context.Context, oid string, w io.Writer) error
	// Put uploads an object. size may be -1 if unknown.
	Put(ctx context.Context, oid string, r io.Reader, size int64) error
	// Describe returns a human readable description of where objects go.
	Describe() string
	// URL returns the public URL of an object.
	URL(oid string) (string, error)
	// ReadOnly reports whether uploads are possible.
	ReadOnly() bool
}
