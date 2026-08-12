// Package attrs manages the .gitattributes entries that bind file patterns to
// the git-s3fs filter.
package attrs

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Filter is the filter name registered in git config and .gitattributes.
const Filter = "s3fs"

// attributes appended after a tracked pattern.
var attributes = []string{"filter=" + Filter, "diff=" + Filter, "merge=" + Filter, "-text"}

// Entry is one tracked pattern.
type Entry struct {
	// File is the .gitattributes file the pattern was found in, relative to
	// the repository root.
	File string
	// Line is the one based line number.
	Line int
	// Pattern is the file pattern.
	Pattern string
}

// Line renders the .gitattributes line for a pattern.
func Line(pattern string) string {
	return quote(pattern) + " " + strings.Join(attributes, " ")
}

// quote wraps a pattern in double quotes if git would otherwise mis-split it.
func quote(pattern string) string {
	if strings.ContainsAny(pattern, " \t\"") {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(pattern) + `"`
	}
	return pattern
}

func unquote(pattern string) string {
	if len(pattern) >= 2 && strings.HasPrefix(pattern, `"`) && strings.HasSuffix(pattern, `"`) {
		return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(pattern[1 : len(pattern)-1])
	}
	return pattern
}

// Parse extracts the patterns bound to the git-s3fs filter from the contents
// of a .gitattributes file.
func Parse(name string, data []byte) []Entry {
	var out []Entry
	sc := bufio.NewScanner(bytes.NewReader(data))
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		pattern, rest := splitPattern(text)
		if pattern == "" {
			continue
		}
		for _, f := range strings.Fields(rest) {
			if f == "filter="+Filter {
				out = append(out, Entry{File: name, Line: line, Pattern: unquote(pattern)})
				break
			}
		}
	}
	return out
}

// splitPattern separates the leading (possibly quoted) pattern from the
// attribute list.
func splitPattern(text string) (pattern, rest string) {
	if strings.HasPrefix(text, `"`) {
		for i := 1; i < len(text); i++ {
			if text[i] == '\\' {
				i++
				continue
			}
			if text[i] == '"' {
				return text[:i+1], strings.TrimSpace(text[i+1:])
			}
		}
		return "", ""
	}
	pattern, rest, _ = strings.Cut(text, " ")
	return pattern, strings.TrimSpace(rest)
}

// ListFile returns the tracked patterns in a single .gitattributes file.
func ListFile(path, name string) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return Parse(name, data), nil
}

// Track adds patterns to the .gitattributes file at path, skipping any that
// are already tracked there. It returns the patterns it added.
func Track(path string, patterns []string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	existing := map[string]bool{}
	for _, e := range Parse(filepath.Base(path), data) {
		existing[e.Pattern] = true
	}

	var added []string
	var b bytes.Buffer
	b.Write(data)
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		b.WriteString("\n")
	}
	for _, p := range patterns {
		if existing[p] {
			continue
		}
		existing[p] = true
		added = append(added, p)
		fmt.Fprintln(&b, Line(p))
	}
	if len(added) == 0 {
		return nil, nil
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		return nil, err
	}
	return added, nil
}

// Untrack removes patterns from the .gitattributes file at path. It returns
// the patterns it removed.
func Untrack(path string, patterns []string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	drop := map[string]bool{}
	for _, p := range patterns {
		drop[p] = true
	}

	var removed []string
	var b bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		pattern, rest := splitPattern(trimmed)
		isOurs := false
		for _, f := range strings.Fields(rest) {
			if f == "filter="+Filter {
				isOurs = true
				break
			}
		}
		if isOurs && drop[unquote(pattern)] {
			removed = append(removed, unquote(pattern))
			continue
		}
		fmt.Fprintln(&b, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(removed) == 0 {
		return nil, nil
	}
	out := bytes.TrimRight(b.Bytes(), "\n")
	if len(out) > 0 {
		out = append(out, '\n')
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return nil, err
	}
	return removed, nil
}
