// Package transfer moves objects between the local cache and object storage.
package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/SeriousBug/gits3fs/internal/cache"
	"github.com/SeriousBug/gits3fs/internal/config"
	"github.com/SeriousBug/gits3fs/internal/pointer"
	"github.com/SeriousBug/gits3fs/internal/store"
)

// Manager resolves which backend serves a given pointer and moves bytes.
//
// The configured bucket is always preferred. When a pointer refers to an
// object that is not there, or when the repository has no configuration at
// all, the manager falls back to the URL recorded in the pointer itself:
// first by talking S3 to the bucket that URL names, then, if the URL is a CDN
// or custom domain that cannot be mapped to a bucket, by plain HTTPS.
type Manager struct {
	cfg     *config.Config
	cache   *cache.Cache
	primary store.Store

	mu       sync.Mutex
	derived  map[string]store.Store
	fallback map[string]string // oid -> URL, for the plain HTTPS fallback
	http     *store.HTTP
}

// NewManager builds a transfer manager. A repository with no bucket
// configured still yields a usable manager: it can download, using pointer
// URLs, but not upload.
func NewManager(ctx context.Context, cfg *config.Config, c *cache.Cache) (*Manager, error) {
	m := &Manager{
		cfg:      cfg,
		cache:    c,
		derived:  map[string]store.Store{},
		fallback: map[string]string{},
	}
	m.http = store.NewHTTP(func(oid string) (string, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if u, ok := m.fallback[oid]; ok {
			return u, nil
		}
		return "", fmt.Errorf("no URL known for object %s", oid)
	})

	if cfg.Configured() {
		s, err := store.NewS3(ctx, cfg)
		if err != nil {
			return nil, err
		}
		m.primary = s
	}
	return m, nil
}

// Primary returns the configured store, or nil when the repository has no
// bucket configured.
func (m *Manager) Primary() store.Store { return m.primary }

// Cache returns the local object cache.
func (m *Manager) Cache() *cache.Cache { return m.cache }

// storesFor returns the backends to try for a pointer, in order.
func (m *Manager) storesFor(ctx context.Context, p *pointer.Pointer) []store.Store {
	var out []store.Store
	if m.primary != nil {
		out = append(out, m.primary)
	}
	if p.URL == "" {
		return out
	}
	loc, ok := config.ParseObjectURL(p.URL)
	if !ok {
		m.mu.Lock()
		m.fallback[p.OID] = p.URL
		m.mu.Unlock()
		return append(out, m.http)
	}
	prefix, ok := config.PrefixFromKey(loc.Key, p.OID)
	if !ok {
		// The URL does not follow our layout; the bytes are still fetchable
		// over HTTPS if the bucket is public.
		m.mu.Lock()
		m.fallback[p.OID] = p.URL
		m.mu.Unlock()
		return append(out, m.http)
	}

	key := loc.Endpoint + "|" + loc.Bucket + "|" + loc.Region + "|" + prefix
	m.mu.Lock()
	s, cached := m.derived[key]
	m.mu.Unlock()
	if !cached {
		derivedCfg := *m.cfg
		derivedCfg.Bucket = loc.Bucket
		derivedCfg.Region = loc.Region
		derivedCfg.Endpoint = loc.Endpoint
		derivedCfg.PathStyle = loc.PathStyle
		derivedCfg.Prefix = prefix
		derivedCfg.PublicURL = ""
		built, err := store.NewS3(ctx, &derivedCfg)
		if err == nil {
			s = built
		}
		m.mu.Lock()
		m.derived[key] = s
		m.mu.Unlock()
	}
	if s != nil && (m.primary == nil || !sameTarget(m.cfg, loc, prefix)) {
		out = append(out, s)
	}
	// Plain HTTPS remains the last resort even for recognised buckets, since
	// the object may be public while our credentials are not authorised.
	m.mu.Lock()
	m.fallback[p.OID] = p.URL
	m.mu.Unlock()
	return append(out, m.http)
}

func sameTarget(cfg *config.Config, loc *config.Location, prefix string) bool {
	return cfg.Bucket == loc.Bucket && cfg.Prefix == prefix && cfg.Endpoint == loc.Endpoint
}

// Ensure downloads an object into the local cache if it is not already there.
func (m *Manager) Ensure(ctx context.Context, p *pointer.Pointer) error {
	if m.cache.Has(p.OID) {
		return nil
	}
	stores := m.storesFor(ctx, p)
	if len(stores) == 0 {
		return fmt.Errorf("object %s is not available locally and no bucket is configured", p.OID[:12])
	}
	var errs []error
	for _, s := range stores {
		w, err := m.cache.NewWriter()
		if err != nil {
			return err
		}
		err = s.Get(ctx, p.OID, w)
		if err != nil {
			w.Abort()
			errs = append(errs, err)
			continue
		}
		if got := w.OID(); got != p.OID {
			w.Abort()
			errs = append(errs, fmt.Errorf("%s returned content that hashes to %s, not %s", s.Describe(), got[:12], p.OID[:12]))
			continue
		}
		if _, _, err := w.Commit(); err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("could not fetch %s: %w", p.OID[:12], errors.Join(errs...))
}

// Open returns a reader for an object, fetching it first if needed.
func (m *Manager) Open(ctx context.Context, p *pointer.Pointer) (io.ReadCloser, error) {
	if err := m.Ensure(ctx, p); err != nil {
		return nil, err
	}
	return m.cache.Open(p.OID)
}

// Upload sends one cached object to the configured bucket.
func (m *Manager) Upload(ctx context.Context, p *pointer.Pointer) error {
	if m.primary == nil {
		return errors.New("no bucket configured; run `git s3fs init --bucket <name> --region <region>`")
	}
	f, err := m.cache.Open(p.OID)
	if err != nil {
		return fmt.Errorf("object %s is missing from the local cache: %w", p.OID[:12], err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	return m.primary.Put(ctx, p.OID, f, st.Size())
}

// Event describes progress on one object.
type Event struct {
	Pointer *pointer.Pointer
	Path    string
	Done    int
	Total   int
	Skipped bool
	Err     error
}

// Reporter receives progress events. It is called from multiple goroutines
// and must be safe for concurrent use.
type Reporter func(Event)

// Item is one object to transfer.
type Item struct {
	Pointer *pointer.Pointer
	Path    string
}

// Result summarises a batch transfer.
type Result struct {
	Transferred int
	Skipped     int
	Bytes       int64
	Errors      []error
}

// Err returns a combined error if anything failed.
func (r Result) Err() error {
	if len(r.Errors) == 0 {
		return nil
	}
	return errors.Join(r.Errors...)
}

// Push uploads every item that is not already present in the bucket.
func (m *Manager) Push(ctx context.Context, items []Item, dryRun bool, report Reporter) (Result, error) {
	if m.primary == nil {
		return Result{}, errors.New("no bucket configured; run `git s3fs init --bucket <name> --region <region>`")
	}
	if m.primary.ReadOnly() {
		return Result{}, errors.New("no AWS credentials were found, so objects cannot be uploaded")
	}
	return m.run(ctx, items, report, func(ctx context.Context, it Item) (bool, int64, error) {
		exists, size, err := m.primary.Has(ctx, it.Pointer.OID)
		if err != nil {
			return false, 0, err
		}
		if exists {
			if size >= 0 && it.Pointer.Size >= 0 && size != it.Pointer.Size {
				return false, 0, fmt.Errorf("object %s already exists with a different size (%d remote, %d expected)", it.Pointer.OID[:12], size, it.Pointer.Size)
			}
			return true, 0, nil
		}
		if dryRun {
			return false, it.Pointer.Size, nil
		}
		if err := m.Upload(ctx, it.Pointer); err != nil {
			return false, 0, err
		}
		return false, it.Pointer.Size, nil
	})
}

// Fetch downloads every item that is not already cached.
func (m *Manager) Fetch(ctx context.Context, items []Item, report Reporter) (Result, error) {
	return m.run(ctx, items, report, func(ctx context.Context, it Item) (bool, int64, error) {
		if m.cache.Has(it.Pointer.OID) {
			return true, 0, nil
		}
		if err := m.Ensure(ctx, it.Pointer); err != nil {
			return false, 0, err
		}
		return false, it.Pointer.Size, nil
	})
}

func (m *Manager) run(ctx context.Context, items []Item, report Reporter, fn func(context.Context, Item) (bool, int64, error)) (Result, error) {
	concurrency := m.cfg.Concurrency
	if concurrency < 1 {
		concurrency = config.DefaultConcurrency
	}

	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		result Result
		done   int
	)
	sem := make(chan struct{}, concurrency)

	for _, it := range items {
		if err := ctx.Err(); err != nil {
			wg.Wait()
			return result, err
		}
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			skipped, n, err := fn(ctx, it)
			mu.Lock()
			done++
			ev := Event{Pointer: it.Pointer, Path: it.Path, Done: done, Total: len(items), Skipped: skipped, Err: err}
			switch {
			case err != nil:
				// Individual failures are collected rather than cancelling
				// the whole batch, so one bad object does not abandon the
				// rest.
				result.Errors = append(result.Errors, err)
			case skipped:
				result.Skipped++
			default:
				result.Transferred++
				result.Bytes += n
			}
			mu.Unlock()
			if report != nil {
				report(ev)
			}
		}()
	}
	wg.Wait()
	return result, nil
}
