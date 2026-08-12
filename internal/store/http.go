package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTP is a read only store that fetches objects over plain HTTPS using the
// URL recorded in the pointer file.
//
// It is the last resort for pointers whose URL is a CDN or a custom domain
// that cannot be mapped back to a bucket. Because pointers carry a real URL,
// a public repository stays usable by someone with no AWS setup at all: they
// clone, and the bytes come down over ordinary HTTP.
type HTTP struct {
	client *http.Client
	urlFor func(oid string) (string, error)
}

// NewHTTP returns a store that resolves object ids to URLs with urlFor.
func NewHTTP(urlFor func(oid string) (string, error)) *HTTP {
	return &HTTP{
		client: &http.Client{Timeout: 0, Transport: http.DefaultTransport},
		urlFor: urlFor,
	}
}

// ReadOnly always reports true.
func (h *HTTP) ReadOnly() bool { return true }

// Describe returns a human readable description.
func (h *HTTP) Describe() string { return "https (read only, from pointer URLs)" }

// URL returns the URL of an object.
func (h *HTTP) URL(oid string) (string, error) { return h.urlFor(oid) }

// Has reports whether the object is reachable.
func (h *HTTP) Has(ctx context.Context, oid string) (bool, int64, error) {
	url, err := h.urlFor(oid)
	if err != nil {
		return false, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false, 0, err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return false, 0, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden:
		return false, 0, nil
	case resp.StatusCode >= 400:
		return false, 0, fmt.Errorf("HEAD %s: %s", url, resp.Status)
	}
	return true, resp.ContentLength, nil
}

// Get streams an object into w.
func (h *HTTP) Get(ctx context.Context, oid string, w io.Writer) error {
	url, err := h.urlFor(oid)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(1<<attempt) * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := h.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			return fmt.Errorf("%w: %s", ErrNotFound, url)
		}
		if resp.StatusCode >= 400 {
			resp.Body.Close()
			lastErr = fmt.Errorf("GET %s: %s", url, resp.Status)
			if resp.StatusCode < 500 {
				return lastErr
			}
			continue
		}
		_, err = io.Copy(w, resp.Body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("downloading %s: %w", url, err)
		}
		return nil
	}
	return lastErr
}

// Put always fails: HTTP URLs are not writable.
func (h *HTTP) Put(context.Context, string, io.Reader, int64) error {
	return errors.New("cannot upload over plain HTTP; configure a bucket with `git s3fs init`")
}
