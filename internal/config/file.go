package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileName is the name of the repository level, committed configuration file.
const FileName = ".gits3fs"

// File is a minimal git-config style configuration file. We parse it
// ourselves rather than shelling out to `git config -f` so that the clean
// filter stays fast and the parser stays unit testable, but the syntax is
// deliberately git's own: users can edit .gits3fs with `git config -f`.
type File struct {
	// values maps "section.key" (lowercased) to its value.
	values map[string]string
	order  []string
}

// NewFile returns an empty configuration file.
func NewFile() *File {
	return &File{values: map[string]string{}}
}

// Get returns the value for "section.key", which must already be lowercase.
func (f *File) Get(key string) (string, bool) {
	if f == nil {
		return "", false
	}
	v, ok := f.values[key]
	return v, ok
}

// Set stores a value, preserving insertion order for new keys.
func (f *File) Set(key, value string) {
	key = strings.ToLower(key)
	if _, exists := f.values[key]; !exists {
		f.order = append(f.order, key)
	}
	f.values[key] = value
}

// Unset removes a key.
func (f *File) Unset(key string) {
	key = strings.ToLower(key)
	delete(f.values, key)
	for i, k := range f.order {
		if k == key {
			f.order = append(f.order[:i], f.order[i+1:]...)
			break
		}
	}
}

// Keys returns the configured keys in insertion order.
func (f *File) Keys() []string {
	out := make([]string, len(f.order))
	copy(out, f.order)
	return out
}

// ParseFile reads a configuration file from disk. A missing file is not an
// error; it yields an empty File.
func ParseFile(path string) (*File, error) {
	fh, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewFile(), nil
		}
		return nil, err
	}
	defer fh.Close()
	return Parse(fh)
}

// Parse reads git-config style data.
func Parse(r io.Reader) (*File, error) {
	f := NewFile()
	section := ""

	sc := bufio.NewScanner(r)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") || strings.HasPrefix(text, ";") {
			continue
		}
		if strings.HasPrefix(text, "[") {
			end := strings.Index(text, "]")
			if end < 0 {
				return nil, fmt.Errorf("line %d: unterminated section header", line)
			}
			// Subsections ([a "b"]) collapse to a.b for our purposes.
			name := strings.TrimSpace(text[1:end])
			name = strings.ReplaceAll(name, `"`, "")
			section = strings.ToLower(strings.Join(strings.Fields(name), "."))
			continue
		}
		key, value, ok := strings.Cut(text, "=")
		if !ok {
			// A bare key means boolean true, as in git.
			key, value = text, "true"
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = trimValue(value)
		if section == "" {
			return nil, fmt.Errorf("line %d: key %q outside of any section", line, key)
		}
		f.Set(section+"."+key, value)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return f, nil
}

// trimValue strips comments, surrounding whitespace and optional quoting.
func trimValue(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, `"`) {
		if end := strings.LastIndex(v, `"`); end > 0 {
			return strings.ReplaceAll(v[1:end], `\"`, `"`)
		}
	}
	// Trailing comments are only recognised on unquoted values.
	if i := strings.IndexAny(v, "#;"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// Bytes renders the file, grouping keys by section.
func (f *File) Bytes() []byte {
	bySection := map[string][]string{}
	var sections []string
	for _, full := range f.order {
		section, key, ok := cutLast(full, ".")
		if !ok {
			continue
		}
		if _, seen := bySection[section]; !seen {
			sections = append(sections, section)
		}
		bySection[section] = append(bySection[section], key)
	}
	sort.Strings(sections)

	var b strings.Builder
	b.WriteString("# git-s3fs repository configuration.\n")
	b.WriteString("# Committed to the repository so that clones work without extra setup.\n")
	b.WriteString("# Never put credentials here: they come from the AWS credential chain.\n")
	for i, section := range sections {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "\n[%s]\n", section)
		for _, key := range bySection[section] {
			fmt.Fprintf(&b, "\t%s = %s\n", key, f.values[section+"."+key])
		}
	}
	return []byte(b.String())
}

// WriteFile writes the configuration to path atomically.
func (f *File) WriteFile(path string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".gits3fs-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(f.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func cutLast(s, sep string) (before, after string, found bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}
