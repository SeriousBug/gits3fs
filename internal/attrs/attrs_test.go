package attrs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tempAttrs(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".gitattributes")
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestTrackCreatesFile(t *testing.T) {
	path := tempAttrs(t, "")
	added, err := Track(path, []string{"*.psd"})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0] != "*.psd" {
		t.Fatalf("added = %v", added)
	}
	want := "*.psd filter=s3fs diff=s3fs merge=s3fs -text\n"
	if got := read(t, path); got != want {
		t.Errorf("file =\n%q\nwant\n%q", got, want)
	}
}

func TestTrackIsIdempotent(t *testing.T) {
	path := tempAttrs(t, "")
	if _, err := Track(path, []string{"*.psd"}); err != nil {
		t.Fatal(err)
	}
	added, err := Track(path, []string{"*.psd", "*.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 || added[0] != "*.mp4" {
		t.Fatalf("added = %v, want only *.mp4", added)
	}
	if strings.Count(read(t, path), "*.psd") != 1 {
		t.Errorf("*.psd was duplicated:\n%s", read(t, path))
	}
}

func TestTrackPreservesOtherEntries(t *testing.T) {
	path := tempAttrs(t, "# keep me\n*.txt text\n*.bin filter=lfs diff=lfs merge=lfs -text")
	if _, err := Track(path, []string{"*.psd"}); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	for _, want := range []string{"# keep me", "*.txt text", "filter=lfs", "*.psd filter=s3fs"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// The original file had no trailing newline; the new entry must still
	// land on its own line.
	if strings.Contains(got, "-text*.psd") {
		t.Errorf("entries were joined:\n%s", got)
	}
}

func TestUntrack(t *testing.T) {
	path := tempAttrs(t, "*.psd filter=s3fs diff=s3fs merge=s3fs -text\n*.mp4 filter=s3fs diff=s3fs merge=s3fs -text\n*.txt text\n")
	removed, err := Untrack(path, []string{"*.psd"})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "*.psd" {
		t.Fatalf("removed = %v", removed)
	}
	got := read(t, path)
	if strings.Contains(got, "*.psd") {
		t.Errorf("*.psd survived:\n%s", got)
	}
	for _, want := range []string{"*.mp4 filter=s3fs", "*.txt text"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestUntrackLeavesForeignFilters(t *testing.T) {
	path := tempAttrs(t, "*.psd filter=lfs diff=lfs merge=lfs -text\n")
	removed, err := Untrack(path, []string{"*.psd"})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want nothing: the pattern belongs to git-lfs", removed)
	}
	if !strings.Contains(read(t, path), "filter=lfs") {
		t.Error("the git-lfs entry was deleted")
	}
}

func TestParse(t *testing.T) {
	body := `
# comment
*.psd filter=s3fs diff=s3fs merge=s3fs -text
*.txt text
"assets with spaces/**" filter=s3fs -text
*.bin filter=lfs
`
	got := Parse(".gitattributes", []byte(body))
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(got), got)
	}
	if got[0].Pattern != "*.psd" || got[0].Line != 3 {
		t.Errorf("entry 0 = %+v", got[0])
	}
	if got[1].Pattern != "assets with spaces/**" {
		t.Errorf("quoted pattern = %q", got[1].Pattern)
	}
}

func TestQuotedPatternRoundTrip(t *testing.T) {
	path := tempAttrs(t, "")
	pattern := "assets with spaces/**"
	if _, err := Track(path, []string{pattern}); err != nil {
		t.Fatal(err)
	}
	entries, err := ListFile(path, ".gitattributes")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Pattern != pattern {
		t.Fatalf("entries = %+v", entries)
	}
	if !strings.HasPrefix(read(t, path), `"assets with spaces/**"`) {
		t.Errorf("the pattern was not quoted:\n%s", read(t, path))
	}

	removed, err := Untrack(path, []string{pattern})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 {
		t.Errorf("removed = %v", removed)
	}
}

func TestListFileMissing(t *testing.T) {
	entries, err := ListFile(filepath.Join(t.TempDir(), "nope"), "nope")
	if err != nil {
		t.Fatalf("a missing file should not be an error: %v", err)
	}
	if entries != nil {
		t.Errorf("entries = %+v", entries)
	}
}
