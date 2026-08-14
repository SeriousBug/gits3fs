// Package gitcmd is a thin wrapper around the git command line.
//
// git-s3fs shells out to git rather than reimplementing repository access:
// it has to interoperate exactly with whatever git the user is running,
// including its attribute matching, index handling and hook conventions.
package gitcmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNotARepo is returned by Discover outside of a git repository.
var ErrNotARepo = errors.New("not inside a git repository")

// Repo is a discovered git repository.
type Repo struct {
	// Root is the top level of the working tree. It is empty for bare
	// repositories.
	Root string
	// GitDir is the absolute path of the .git directory.
	GitDir string
}

// Discover locates the repository containing the current directory.
func Discover() (*Repo, error) {
	gitDir, err := run("", "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, ErrNotARepo
	}
	r := &Repo{GitDir: strings.TrimSpace(gitDir)}
	if root, err := run("", "rev-parse", "--show-toplevel"); err == nil {
		r.Root = strings.TrimSpace(root)
	}
	return r, nil
}

// RequireWorkTree returns an error for bare repositories.
func (r *Repo) RequireWorkTree() error {
	if r.Root == "" {
		return errors.New("this command needs a working tree; the repository is bare")
	}
	return nil
}

// Path joins a path relative to the working tree root.
func (r *Repo) Path(parts ...string) string {
	return filepath.Join(append([]string{r.Root}, parts...)...)
}

// GitPath joins a path inside the .git directory.
func (r *Repo) GitPath(parts ...string) string {
	return filepath.Join(append([]string{r.GitDir}, parts...)...)
}

// Run executes git with the given arguments and returns trimmed stdout.
func (r *Repo) Run(args ...string) (string, error) {
	out, err := run(r.dir(), args...)
	return strings.TrimRight(out, "\n"), err
}

// RunRaw executes git and returns stdout verbatim, which matters for -z
// separated output.
func (r *Repo) RunRaw(args ...string) ([]byte, error) {
	cmd := r.Command(args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), wrap(args, stderr.String(), err)
	}
	return stdout.Bytes(), nil
}

// Command builds an *exec.Cmd for git, rooted in the repository.
func (r *Repo) Command(args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir()
	return cmd
}

func (r *Repo) dir() string {
	if r.Root != "" {
		return r.Root
	}
	return r.GitDir
}

// ConfigGet reads a git config value, honouring the usual system, global and
// local layering.
func (r *Repo) ConfigGet(key string) (string, bool) {
	out, err := r.Run("config", "--get", key)
	if err != nil || out == "" {
		return "", false
	}
	return out, true
}

// ConfigSet writes a git config value.
func (r *Repo) ConfigSet(key, value string, global bool) error {
	args := []string{"config"}
	if global {
		args = append(args, "--global")
	}
	args = append(args, key, value)
	_, err := r.Run(args...)
	return err
}

// ConfigUnset removes a git config value, tolerating a missing key.
func (r *Repo) ConfigUnset(key string, global bool) error {
	args := []string{"config"}
	if global {
		args = append(args, "--global")
	}
	args = append(args, "--unset-all", key)
	_, err := r.Run(args...)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 5 {
		return nil // key was not set
	}
	return err
}

// Version returns the running git version string.
func Version() (string, error) {
	out, err := run("", "version")
	return strings.TrimSpace(out), err
}

// Available reports whether git is on PATH.
func Available() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), wrap(args, stderr.String(), err)
	}
	return stdout.String(), nil
}

func wrap(args []string, stderr string, err error) error {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, stderr)
}

// InHook reports whether we are running inside a git hook, where git sets
// GIT_DIR and friends.
func InHook() bool { return os.Getenv("GIT_DIR") != "" }
