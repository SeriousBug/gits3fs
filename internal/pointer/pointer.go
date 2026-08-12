// Package pointer implements the git-s3fs pointer file format.
//
// A pointer is the small text file that git actually stores in place of a
// large object. Unlike git-lfs pointers, a git-s3fs pointer carries the full
// URL of the object in S3, so that a human browsing the repository on a git
// hosting service can copy (or click) a link straight to the bytes, and so
// that a clone with no local configuration still knows where to look.
//
//	version https://github.com/SeriousBug/gits3fs/spec/v1
//	url https://my-bucket.s3.us-east-1.amazonaws.com/assets/objects/9f/86/9f86d0...
//	oid sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
//	size 13
//
// The format is line oriented: each line is "key SP value LF". The version
// line is always first; the remaining keys may appear in any order when
// parsing, but are always written in the order above.
package pointer

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	// SpecURL identifies the git-s3fs pointer format version.
	SpecURL = "https://github.com/SeriousBug/gits3fs/spec/v1"

	// LFSSpecURL identifies a git-lfs pointer, which we can recognise (so we
	// can give a useful error or migrate) but do not write.
	LFSSpecURL = "https://git-lfs.github.com/spec/v1"

	// MaxSize is the largest blob we will even attempt to parse as a pointer.
	// Anything bigger is definitely real content.
	MaxSize = 4096
)

// ErrNotPointer is returned by Parse when the input is not a pointer file.
var ErrNotPointer = errors.New("not a git-s3fs pointer")

// Kind distinguishes our pointers from git-lfs pointers.
type Kind int

const (
	KindS3FS Kind = iota
	KindLFS
)

// Pointer is a parsed pointer file.
type Pointer struct {
	Kind Kind
	// URL is the location of the object. Empty for git-lfs pointers.
	URL string
	// OID is the lowercase hex sha256 of the object contents.
	OID string
	// Size is the object size in bytes.
	Size int64
}

// New builds a pointer for an object with the given oid, size and URL.
func New(oid string, size int64, url string) *Pointer {
	return &Pointer{Kind: KindS3FS, OID: oid, Size: size, URL: url}
}

// Validate reports whether the pointer is structurally usable.
func (p *Pointer) Validate() error {
	if len(p.OID) != 64 {
		return fmt.Errorf("oid must be a 64 character hex sha256, got %d characters", len(p.OID))
	}
	if _, err := hex.DecodeString(p.OID); err != nil {
		return fmt.Errorf("oid is not valid hex: %w", err)
	}
	if p.OID != strings.ToLower(p.OID) {
		return errors.New("oid must be lowercase")
	}
	if p.Size < 0 {
		return errors.New("size must not be negative")
	}
	return nil
}

// Bytes renders the pointer in its canonical serialised form.
//
// The rendering must be deterministic: git invokes the clean filter on every
// staging of the file, and any variation between machines would show up as a
// spurious diff. That is why the URL is derived only from repository level
// configuration (the committed .gits3fs file) and never from a per-user
// setting.
func (p *Pointer) Bytes() []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "version %s\n", SpecURL)
	if p.URL != "" {
		fmt.Fprintf(&b, "url %s\n", p.URL)
	}
	fmt.Fprintf(&b, "oid sha256:%s\n", p.OID)
	fmt.Fprintf(&b, "size %d\n", p.Size)
	return b.Bytes()
}

func (p *Pointer) String() string { return string(p.Bytes()) }

// IsPointer reports whether b looks like a pointer file. It is deliberately
// cheap: it only inspects the first line, so it can be used to skip large
// blobs without parsing them.
func IsPointer(b []byte) bool {
	if len(b) > MaxSize {
		return false
	}
	line, _, _ := bytes.Cut(b, []byte("\n"))
	s := strings.TrimSpace(string(line))
	return s == "version "+SpecURL || s == "version "+LFSSpecURL
}

// Parse reads a pointer file.
func Parse(b []byte) (*Pointer, error) {
	if len(b) > MaxSize {
		return nil, ErrNotPointer
	}
	p := &Pointer{}
	var sawVersion, sawOID, sawSize bool

	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("%w: malformed line %q", ErrNotPointer, line)
		}
		switch key {
		case "version":
			if sawVersion {
				return nil, fmt.Errorf("%w: duplicate version line", ErrNotPointer)
			}
			switch value {
			case SpecURL:
				p.Kind = KindS3FS
			case LFSSpecURL:
				p.Kind = KindLFS
			default:
				return nil, fmt.Errorf("%w: unknown spec %q", ErrNotPointer, value)
			}
			sawVersion = true
		case "url":
			p.URL = value
		case "oid":
			algo, digest, ok := strings.Cut(value, ":")
			if !ok {
				return nil, fmt.Errorf("%w: oid %q is missing an algorithm prefix", ErrNotPointer, value)
			}
			if algo != "sha256" {
				return nil, fmt.Errorf("%w: unsupported oid algorithm %q", ErrNotPointer, algo)
			}
			p.OID = digest
			sawOID = true
		case "size":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%w: bad size %q", ErrNotPointer, value)
			}
			p.Size = n
			sawSize = true
		default:
			// Unknown keys are ignored so the format can grow.
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !sawVersion {
		return nil, fmt.Errorf("%w: missing version line", ErrNotPointer)
	}
	if !sawOID || !sawSize {
		return nil, fmt.Errorf("%w: missing oid or size line", ErrNotPointer)
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotPointer, err)
	}
	return p, nil
}

// Hash returns the lowercase hex sha256 of everything read from r, along with
// the number of bytes read. Content is copied to w as it is hashed; pass
// io.Discard if you only want the digest.
func Hash(w io.Writer, r io.Reader) (oid string, size int64, err error) {
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(h, w), r)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
