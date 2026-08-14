// Package scan finds git-s3fs pointers in a repository, both in committed
// history and in the working tree.
package scan

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/SeriousBug/gits3fs/internal/attrs"
	"github.com/SeriousBug/gits3fs/internal/gitcmd"
	"github.com/SeriousBug/gits3fs/internal/pointer"
)

// Found is a pointer discovered in the repository.
type Found struct {
	Pointer *pointer.Pointer
	// Blob is the git blob object id holding the pointer.
	Blob string
	// Path is a path the pointer was seen at. A pointer reachable from many
	// paths reports one of them.
	Path string
}

// History returns every distinct pointer reachable from the given rev-list
// arguments, for example []string{"--all"} or []string{"abc123", "--not",
// "--remotes=origin"}.
func History(repo *gitcmd.Repo, revArgs []string) ([]Found, error) {
	blobs, paths, err := revListObjects(repo, revArgs)
	if err != nil {
		return nil, err
	}
	if len(blobs) == 0 {
		return nil, nil
	}
	candidates, err := smallBlobs(repo, blobs)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	return readPointers(repo, candidates, paths)
}

// revListObjects lists candidate object ids and the path each was seen at.
func revListObjects(repo *gitcmd.Repo, revArgs []string) ([]string, map[string]string, error) {
	args := append([]string{"rev-list", "--objects"}, revArgs...)
	cmd := repo.Command(args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}

	var blobs []string
	paths := map[string]string{}
	seen := map[string]bool{}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		sha, path, _ := strings.Cut(sc.Text(), " ")
		if len(sha) < 40 || path == "" {
			// Commits and tags have no path; trees do but hold no content.
			continue
		}
		if !seen[sha] {
			seen[sha] = true
			blobs = append(blobs, sha)
			paths[sha] = path
		}
	}
	scanErr := sc.Err()
	if err := cmd.Wait(); err != nil {
		return nil, nil, err
	}
	return blobs, paths, scanErr
}

// smallBlobs filters object ids down to blobs small enough to be a pointer.
func smallBlobs(repo *gitcmd.Repo, shas []string) ([]string, error) {
	var out []string
	err := batch(repo, []string{"cat-file", "--batch-check"}, shas, func(r *bufio.Reader, line string) error {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[1] != "blob" {
			return nil
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size == 0 || size > pointer.MaxSize {
			return nil
		}
		out = append(out, fields[0])
		return nil
	})
	return out, err
}

// readPointers reads and parses the candidate blobs.
func readPointers(repo *gitcmd.Repo, shas []string, paths map[string]string) ([]Found, error) {
	var out []Found
	seen := map[string]bool{}
	err := batch(repo, []string{"cat-file", "--batch"}, shas, func(r *bufio.Reader, line string) error {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return fmt.Errorf("unexpected cat-file output %q", line)
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return fmt.Errorf("unexpected cat-file size in %q", line)
		}
		content := make([]byte, size)
		if _, err := io.ReadFull(r, content); err != nil {
			return err
		}
		// cat-file --batch terminates each record with a newline.
		if _, err := r.Discard(1); err != nil {
			return err
		}
		p, err := pointer.Parse(content)
		if err != nil || p.Kind != pointer.KindS3FS {
			return nil
		}
		if seen[p.OID] {
			return nil
		}
		seen[p.OID] = true
		out = append(out, Found{Pointer: p, Blob: fields[0], Path: paths[fields[0]]})
		return nil
	})
	return out, err
}

// batch feeds shas to a git cat-file batch command and calls handle for each
// response header line. handle may read further bytes from the reader.
func batch(repo *gitcmd.Repo, args []string, shas []string, handle func(*bufio.Reader, string) error) error {
	cmd := repo.Command(args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}

	writeErr := make(chan error, 1)
	go func() {
		w := bufio.NewWriter(stdin)
		for _, sha := range shas {
			if _, err := w.WriteString(sha + "\n"); err != nil {
				writeErr <- err
				stdin.Close()
				return
			}
		}
		err := w.Flush()
		stdin.Close()
		writeErr <- err
	}()

	r := bufio.NewReaderSize(stdout, 64*1024)
	var handleErr error
	for range shas {
		line, err := r.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			handleErr = err
			break
		}
		line = strings.TrimRight(line, "\n")
		if strings.HasSuffix(line, " missing") {
			continue
		}
		if err := handle(r, line); err != nil {
			handleErr = err
			break
		}
	}
	io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil && handleErr == nil {
		handleErr = err
	}
	if err := <-writeErr; err != nil && handleErr == nil {
		handleErr = err
	}
	return handleErr
}

// TrackedFile is a working tree file bound to the git-s3fs filter.
type TrackedFile struct {
	// Path is relative to the repository root.
	Path string
	// Pointer is set when the working tree copy currently holds a pointer
	// rather than the real content.
	Pointer *pointer.Pointer
	// Size is the size of the working tree file.
	Size int64
	// Missing reports that the file is tracked but absent from the working
	// tree.
	Missing bool
}

// WorkTree lists the files in the working tree that the git-s3fs filter
// applies to, restricted to pathspecs if any are given.
func WorkTree(repo *gitcmd.Repo, pathspecs []string) ([]TrackedFile, error) {
	if err := repo.RequireWorkTree(); err != nil {
		return nil, err
	}
	args := []string{"ls-files", "-z", "--cached"}
	if len(pathspecs) > 0 {
		args = append(args, "--")
		args = append(args, pathspecs...)
	}
	raw, err := repo.RunRaw(args...)
	if err != nil {
		return nil, err
	}
	files := splitNul(raw)
	if len(files) == 0 {
		return nil, nil
	}

	filtered, err := filterAttr(repo, files)
	if err != nil {
		return nil, err
	}

	out := make([]TrackedFile, 0, len(filtered))
	for _, path := range filtered {
		tf := TrackedFile{Path: path}
		full := repo.Path(path)
		st, err := os.Lstat(full)
		switch {
		case err != nil:
			tf.Missing = true
		case st.Mode().IsRegular():
			tf.Size = st.Size()
			if st.Size() <= pointer.MaxSize {
				if data, err := os.ReadFile(full); err == nil {
					if p, err := pointer.Parse(data); err == nil {
						tf.Pointer = p
					}
				}
			}
		}
		out = append(out, tf)
	}
	return out, nil
}

// filterAttr returns the subset of paths whose filter attribute is s3fs.
func filterAttr(repo *gitcmd.Repo, paths []string) ([]string, error) {
	cmd := repo.Command("check-attr", "-z", "--stdin", "filter")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		w := bufio.NewWriter(stdin)
		for _, p := range paths {
			w.WriteString(p)
			w.WriteByte(0)
		}
		w.Flush()
		stdin.Close()
	}()

	raw, readErr := io.ReadAll(stdout)
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		return nil, waitErr
	}

	fields := splitNul(raw)
	var out []string
	for i := 0; i+2 < len(fields); i += 3 {
		if fields[i+1] == "filter" && fields[i+2] == attrs.Filter {
			out = append(out, fields[i])
		}
	}
	return out, nil
}

func splitNul(b []byte) []string {
	s := string(b)
	s = strings.TrimSuffix(s, "\x00")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}
