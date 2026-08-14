// Package fakes3 is a tiny in-process S3 implementation for tests.
//
// It covers exactly the surface git-s3fs uses: HeadBucket, PutObject,
// HeadObject, GetObject and multipart uploads. It performs no authentication;
// requests are trusted.
package fakes3

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Server is a fake S3 endpoint using path style addressing.
type Server struct {
	*httptest.Server

	mu      sync.Mutex
	objects map[string][]byte
	uploads map[string]map[int][]byte
	nextID  int

	// Requests counts requests by method, for assertions about behaviour such
	// as skipping objects that already exist.
	Requests map[string]int
}

// New starts a fake S3 server holding the named bucket.
func New() *Server {
	s := &Server{
		objects:  map[string][]byte{},
		uploads:  map[string]map[int][]byte{},
		Requests: map[string]int{},
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// Endpoint returns the base URL to configure git-s3fs with.
func (s *Server) Endpoint() string { return s.Server.URL }

// Objects returns a snapshot of the stored keys.
func (s *Server) Objects() map[string][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]byte, len(s.objects))
	for k, v := range s.objects {
		out[k] = v
	}
	return out
}

// Get returns a stored object.
func (s *Server) Get(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.objects[key]
	return v, ok
}

// Put stores an object directly, without going through HTTP.
func (s *Server) Put(key string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = data
}

// Count returns how many requests of a method have been served.
func (s *Server) Count(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Requests[method]
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.Requests[r.Method]++
	s.mu.Unlock()

	// Path style: /<bucket>/<key...>
	trimmed := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, hasKey := strings.Cut(trimmed, "/")
	if bucket == "" {
		http.Error(w, "no bucket", http.StatusBadRequest)
		return
	}
	if !hasKey || key == "" {
		// Bucket level request, which is only ever HeadBucket here.
		w.WriteHeader(http.StatusOK)
		return
	}

	q := r.URL.Query()
	switch {
	case r.Method == http.MethodPost && q.Has("uploads"):
		s.initiateMultipart(w, bucket, key)
	case r.Method == http.MethodPut && q.Get("uploadId") != "":
		s.uploadPart(w, r, q.Get("uploadId"), q.Get("partNumber"))
	case r.Method == http.MethodPost && q.Get("uploadId") != "":
		s.completeMultipart(w, bucket, key, q.Get("uploadId"))
	case r.Method == http.MethodDelete && q.Get("uploadId") != "":
		s.mu.Lock()
		delete(s.uploads, q.Get("uploadId"))
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut:
		s.putObject(w, r, key)
	case r.Method == http.MethodHead, r.Method == http.MethodGet:
		s.getObject(w, r, key)
	default:
		http.Error(w, "unsupported", http.StatusMethodNotAllowed)
	}
}

func (s *Server) putObject(w http.ResponseWriter, r *http.Request, key string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.objects[key] = body
	s.mu.Unlock()
	w.Header().Set("ETag", `"`+fakeETag(body)+`"`)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) getObject(w http.ResponseWriter, r *http.Request, key string) {
	s.mu.Lock()
	body, ok := s.objects[key]
	s.mu.Unlock()
	if !ok {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		if r.Method == http.MethodGet {
			io.WriteString(w, `<Error><Code>NoSuchKey</Code><Message>not found</Message></Error>`)
		}
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("ETag", `"`+fakeETag(body)+`"`)
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		w.Write(body)
	}
}

func (s *Server) initiateMultipart(w http.ResponseWriter, bucket, key string) {
	s.mu.Lock()
	s.nextID++
	id := fmt.Sprintf("upload-%d", s.nextID)
	s.uploads[id] = map[int][]byte{}
	s.mu.Unlock()

	writeXML(w, struct {
		XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
		Bucket   string
		Key      string
		UploadId string
	}{Bucket: bucket, Key: key, UploadId: id})
}

func (s *Server) uploadPart(w http.ResponseWriter, r *http.Request, id, part string) {
	n, err := strconv.Atoi(part)
	if err != nil {
		http.Error(w, "bad part number", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	parts, ok := s.uploads[id]
	if ok {
		parts[n] = body
	}
	s.mu.Unlock()
	if !ok {
		http.Error(w, "no such upload", http.StatusNotFound)
		return
	}
	w.Header().Set("ETag", `"`+fakeETag(body)+`"`)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) completeMultipart(w http.ResponseWriter, bucket, key, id string) {
	s.mu.Lock()
	parts, ok := s.uploads[id]
	if ok {
		numbers := make([]int, 0, len(parts))
		for n := range parts {
			numbers = append(numbers, n)
		}
		sort.Ints(numbers)
		var joined []byte
		for _, n := range numbers {
			joined = append(joined, parts[n]...)
		}
		s.objects[key] = joined
		delete(s.uploads, id)
	}
	s.mu.Unlock()
	if !ok {
		http.Error(w, "no such upload", http.StatusNotFound)
		return
	}
	writeXML(w, struct {
		XMLName xml.Name `xml:"CompleteMultipartUploadResult"`
		Bucket  string
		Key     string
		ETag    string
	}{Bucket: bucket, Key: key, ETag: `"complete"`})
}

func writeXML(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, xml.Header)
	xml.NewEncoder(w).Encode(v)
}

// fakeETag is not MD5; nothing under test verifies it.
func fakeETag(b []byte) string { return fmt.Sprintf("%x", len(b)) }
