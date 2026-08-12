package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/SeriousBug/gits3fs/internal/config"
	"github.com/SeriousBug/gits3fs/internal/pktline"
	"github.com/SeriousBug/gits3fs/internal/pointer"
	"github.com/SeriousBug/gits3fs/internal/transfer"
)

const usageClean = `
git s3fs clean -- <path>

The git clean filter. Reads file content on stdin and writes a pointer on
stdout, storing the content in the local object cache. git runs this for you;
you should not need to run it by hand.
`

const usageSmudge = `
git s3fs smudge -- <path>

The git smudge filter. Reads a pointer on stdin and writes the file content on
stdout, downloading it if it is not cached. git runs this for you.
`

const usageFilterProcess = `
git s3fs filter-process

The git long running filter process. git starts one of these per command and
streams every tracked file through it, which is much faster than spawning a
process per file. git runs this for you.
`

// warnOnce keeps the filters from repeating the same warning for every file
// in a checkout.
var warnOnce sync.Map

func warn(key, format string, args ...any) {
	if _, seen := warnOnce.LoadOrStore(key, true); seen {
		return
	}
	fmt.Fprintf(os.Stderr, "git-s3fs: "+format+"\n", args...)
}

// pointerURL builds the URL to record in a pointer for an object.
//
// Only repository level configuration is consulted, because the clean filter
// has to be deterministic: if the URL varied with a developer's personal
// settings, the same file would produce different pointers on different
// machines and git would report changes that are not there.
func (e *env) pointerURL(oid string) (string, error) {
	shared := e.cfg.URLConfig()
	if shared.Bucket != "" || shared.PublicURL != "" {
		if e.cfg.URLDrift() {
			warn("drift", "your local settings point somewhere other than %s, but pointers record the shared location; commits may reference objects you have not uploaded", config.FileName)
		}
		return shared.ObjectURL(oid)
	}
	if e.cfg.Bucket != "" || e.cfg.PublicURL != "" {
		warn("nofile", "no %s in this repository, so pointer URLs come from your local settings; run `git s3fs init --bucket ... --region ...` and commit %s so everyone agrees", config.FileName, config.FileName)
		return e.cfg.ObjectURL(oid)
	}
	warn("nobucket", "no bucket configured, so pointers will not carry a URL; run `git s3fs init --bucket <name> --region <region>`")
	return "", nil
}

// clean turns file content into a pointer, caching the content on the way.
func (e *env) clean(r io.Reader, w io.Writer) error {
	_, all, existing := splitPointer(r)
	if existing != nil {
		// Already a pointer: pass it through untouched so that re-staging a
		// pointer file is a no-op rather than a pointer to a pointer.
		_, err := io.Copy(w, all)
		return err
	}

	cw, err := e.cache.NewWriter()
	if err != nil {
		return err
	}
	if _, err := io.Copy(cw, all); err != nil {
		cw.Abort()
		return err
	}
	oid, size, err := cw.Commit()
	if err != nil {
		return err
	}
	url, err := e.pointerURL(oid)
	if err != nil {
		return err
	}
	_, err = w.Write(pointer.New(oid, size, url).Bytes())
	return err
}

// smudge turns a pointer back into file content.
func (e *env) smudge(ctx context.Context, path string, r io.Reader, w io.Writer) error {
	_, all, p := splitPointer(r)
	if p == nil || p.Kind != pointer.KindS3FS {
		// Not one of ours; hand the bytes back unchanged.
		_, err := io.Copy(w, all)
		return err
	}

	if e.cfg.Lazy && !e.cache.Has(p.OID) {
		warn("lazy", "s3fs.lazy is set, so pointers are left in place; run `git s3fs pull` to download content")
		_, err := w.Write(p.Bytes())
		return err
	}

	mgr, err := e.filterManager(ctx)
	if err == nil {
		var rc io.ReadCloser
		rc, err = mgr.Open(ctx, p)
		if err == nil {
			defer rc.Close()
			_, err = io.Copy(w, rc)
			return err
		}
	}
	// Leaving the pointer in place keeps the checkout going. `git s3fs
	// checkout` can materialise the file later once the problem is fixed.
	warn("fetch:"+p.OID, "could not fetch %s: %v", displayPath(path, p), err)
	_, werr := w.Write(p.Bytes())
	return werr
}

func displayPath(path string, p *pointer.Pointer) string {
	if path != "" {
		return path
	}
	return p.OID[:12]
}

// filterManager lazily builds the transfer manager, so that a checkout that
// touches no missing objects never has to resolve credentials.
func (e *env) filterManager(ctx context.Context) (*transfer.Manager, error) {
	e.mgrOnce.Do(func() {
		e.mgr, e.mgrErr = e.manager(ctx)
	})
	return e.mgr, e.mgrErr
}

// splitPointer inspects the head of a stream to decide whether it holds a
// pointer, and returns a reader that replays everything it consumed.
func splitPointer(r io.Reader) (head []byte, all io.Reader, p *pointer.Pointer) {
	buf := make([]byte, pointer.MaxSize+1)
	n, err := io.ReadFull(r, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		// Report the error through the copy that follows.
		return nil, io.MultiReader(bytes.NewReader(buf[:n]), errReader{err}), nil
	}
	head = buf[:n]
	all = io.MultiReader(bytes.NewReader(head), r)
	if n <= pointer.MaxSize {
		if parsed, err := pointer.Parse(head); err == nil {
			return head, all, parsed
		}
	}
	return head, all, nil
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func cmdClean(ctx context.Context, args []string) error {
	e, err := newEnv()
	if err != nil {
		return err
	}
	return e.clean(os.Stdin, os.Stdout)
}

func cmdSmudge(ctx context.Context, args []string) error {
	e, err := newEnv()
	if err != nil {
		return err
	}
	return e.smudge(ctx, filterPath(args), os.Stdin, os.Stdout)
}

// filterPath extracts the %f argument git passes after --.
func filterPath(args []string) string {
	for i, a := range args {
		if a == "--" && i+1 < len(args) {
			return args[i+1]
		}
	}
	if len(args) > 0 {
		return args[len(args)-1]
	}
	return ""
}

// cmdFilterProcess implements git's long running filter protocol.
//
// Protocol summary (see gitattributes(5), "Long Running Filter Process"):
// a handshake exchanges versions and capabilities, then git sends a request
// as a list of "key=value" lines terminated by a flush packet, followed by
// the content and another flush. We answer with a status list, a flush, the
// result content, a flush, and a final empty list.
func cmdFilterProcess(ctx context.Context, args []string) error {
	e, err := newEnv()
	if err != nil {
		return err
	}
	r := pktline.NewReader(os.Stdin)
	w := pktline.NewWriter(os.Stdout)

	intro, err := r.ReadPacketList()
	if err != nil {
		return fmt.Errorf("filter handshake: %w", err)
	}
	if !contains(intro, "git-filter-client") {
		return fmt.Errorf("filter handshake: unexpected greeting %q", strings.Join(intro, ", "))
	}
	if !contains(intro, "version=2") {
		return fmt.Errorf("filter handshake: git asked for %q, which git-s3fs does not speak", strings.Join(intro, ", "))
	}
	if err := writeList(w, "git-filter-server", "version=2"); err != nil {
		return err
	}

	caps, err := r.ReadPacketList()
	if err != nil {
		return fmt.Errorf("filter handshake: %w", err)
	}
	var ours []string
	for _, c := range caps {
		switch c {
		case "capability=clean", "capability=smudge":
			ours = append(ours, c)
		}
	}
	if err := writeList(w, ours...); err != nil {
		return err
	}

	for {
		req, err := r.ReadPacketList()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if len(req) == 0 {
			return nil // git closed the conversation
		}
		fields := map[string]string{}
		for _, line := range req {
			k, v, _ := strings.Cut(line, "=")
			fields[k] = v
		}

		var out bytes.Buffer
		var opErr error
		switch fields["command"] {
		case "clean":
			opErr = e.clean(&readerUntilFlush{r: r}, &out)
		case "smudge":
			opErr = e.smudge(ctx, fields["pathname"], &readerUntilFlush{r: r}, &out)
		default:
			r.Discard()
			opErr = fmt.Errorf("unsupported filter command %q", fields["command"])
		}
		if opErr != nil {
			warn("op:"+fields["pathname"], "%s failed for %s: %v", fields["command"], fields["pathname"], opErr)
			if err := writeList(w, "status=error"); err != nil {
				return err
			}
			if err := w.Flush(); err != nil {
				return err
			}
			continue
		}
		if err := writeList(w, "status=success"); err != nil {
			return err
		}
		if err := w.CopyFrom(bytes.NewReader(out.Bytes())); err != nil {
			return err
		}
		if err := w.WriteFlush(); err != nil {
			return err
		}
		if err := writeList(w); err != nil {
			return err
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
}

// readerUntilFlush adapts a pkt-line reader to io.Reader semantics, ending at
// the flush packet that terminates the payload.
//
// Packets can be larger than the buffer io.Copy offers, so leftovers are
// carried over between reads. The payload is copied out of the reader's
// scratch buffer, which it reuses for the next packet.
type readerUntilFlush struct {
	r     *pktline.Reader
	store []byte
	buf   []byte
	done  bool
}

func (rf *readerUntilFlush) Read(p []byte) (int, error) {
	for len(rf.buf) == 0 {
		if rf.done {
			return 0, io.EOF
		}
		pkt, err := rf.r.ReadPacket()
		if errors.Is(err, pktline.ErrFlush) {
			rf.done = true
			return 0, io.EOF
		}
		if err != nil {
			return 0, err
		}
		rf.store = append(rf.store[:0], pkt...)
		rf.buf = rf.store
	}
	n := copy(p, rf.buf)
	rf.buf = rf.buf[n:]
	return n, nil
}

func writeList(w *pktline.Writer, lines ...string) error {
	for _, l := range lines {
		if err := w.WritePacketText(l); err != nil {
			return err
		}
	}
	if err := w.WriteFlush(); err != nil {
		return err
	}
	return w.Flush()
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
