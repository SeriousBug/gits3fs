package pointer

import (
	"io"
	"strings"
	"testing"
)

const testOID = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

func TestRoundTrip(t *testing.T) {
	url := "https://my-bucket.s3.eu-west-1.amazonaws.com/assets/objects/9f/86/" + testOID
	want := New(testOID, 13, url)

	got, err := Parse(want.Bytes())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.OID != want.OID || got.Size != want.Size || got.URL != want.URL {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
	if got.Kind != KindS3FS {
		t.Errorf("Kind = %v, want KindS3FS", got.Kind)
	}
}

func TestBytesIsDeterministic(t *testing.T) {
	// git re-runs the clean filter constantly; unstable output would show up
	// as phantom diffs.
	p := New(testOID, 42, "https://example.com/x")
	first := string(p.Bytes())
	for range 10 {
		if got := string(p.Bytes()); got != first {
			t.Fatalf("Bytes() is not deterministic:\n%q\n%q", first, got)
		}
	}
}

func TestBytesLayout(t *testing.T) {
	p := New(testOID, 13, "https://example.com/o")
	lines := strings.Split(strings.TrimRight(string(p.Bytes()), "\n"), "\n")
	want := []string{
		"version " + SpecURL,
		"url https://example.com/o",
		"oid sha256:" + testOID,
		"size 13",
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(want), lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestParseKeyOrderIsFlexible(t *testing.T) {
	in := "version " + SpecURL + "\nsize 7\noid sha256:" + testOID + "\nurl https://example.com/o\nfuture-key whatever\n"
	p, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Size != 7 || p.OID != testOID || p.URL != "https://example.com/o" {
		t.Errorf("got %+v", p)
	}
}

func TestParseWithoutURL(t *testing.T) {
	p := New(testOID, 1, "")
	if strings.Contains(string(p.Bytes()), "url") {
		t.Fatalf("an empty URL should be omitted: %q", p.Bytes())
	}
	got, err := Parse(p.Bytes())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.URL != "" {
		t.Errorf("URL = %q, want empty", got.URL)
	}
}

func TestParseLFSPointer(t *testing.T) {
	in := "version " + LFSSpecURL + "\noid sha256:" + testOID + "\nsize 13\n"
	p, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Kind != KindLFS {
		t.Errorf("Kind = %v, want KindLFS", p.Kind)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"plain text":        "hello world\n",
		"empty":             "",
		"no version":        "oid sha256:" + testOID + "\nsize 1\n",
		"unknown spec":      "version https://example.com/other\noid sha256:" + testOID + "\nsize 1\n",
		"missing size":      "version " + SpecURL + "\noid sha256:" + testOID + "\n",
		"bad size":          "version " + SpecURL + "\noid sha256:" + testOID + "\nsize twelve\n",
		"short oid":         "version " + SpecURL + "\noid sha256:abc\nsize 1\n",
		"uppercase oid":     "version " + SpecURL + "\noid sha256:" + strings.ToUpper(testOID) + "\nsize 1\n",
		"wrong algorithm":   "version " + SpecURL + "\noid sha1:" + testOID + "\nsize 1\n",
		"missing algorithm": "version " + SpecURL + "\noid " + testOID + "\nsize 1\n",
		"too large":         strings.Repeat("x", MaxSize+1),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if p, err := Parse([]byte(in)); err == nil {
				t.Errorf("Parse succeeded unexpectedly: %+v", p)
			}
		})
	}
}

func TestIsPointer(t *testing.T) {
	if !IsPointer(New(testOID, 1, "https://example.com/o").Bytes()) {
		t.Error("a real pointer was not recognised")
	}
	if IsPointer([]byte("binary\x00content")) {
		t.Error("binary content was mistaken for a pointer")
	}
	if IsPointer([]byte(strings.Repeat("a", MaxSize+1))) {
		t.Error("oversized content was mistaken for a pointer")
	}
}

func TestHash(t *testing.T) {
	var sink strings.Builder
	oid, size, err := Hash(&sink, strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if oid != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Errorf("oid = %s", oid)
	}
	if size != 5 {
		t.Errorf("size = %d, want 5", size)
	}
	if sink.String() != "hello" {
		t.Errorf("content was not copied through: %q", sink.String())
	}
}

func TestHashDiscard(t *testing.T) {
	oid, _, err := Hash(io.Discard, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if oid != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("empty oid = %s", oid)
	}
}
