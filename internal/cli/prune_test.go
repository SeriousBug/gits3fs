package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SeriousBug/gits3fs/internal/pointer"
)

// testRepo is a throwaway git repository the prune tests drive directly.
type testRepo struct {
	t   *testing.T
	dir string
}

// oldCommitDate is far outside any retention window the tests use.
var oldCommitDate = time.Now().Add(-90 * 24 * time.Hour).Format(time.RFC3339)

func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	dir := t.TempDir()
	r := &testRepo{t: t, dir: dir}
	r.git("init", "-q", ".")
	r.git("config", "user.email", "test@example.com")
	r.git("config", "user.name", "Test")
	r.write(".gitattributes", "data/*.jsonl filter=s3fs -text\n")
	r.git("add", "-A")
	r.git("commit", "-qm", "track the datasets")
	return r
}

// git runs a git command in the repository.
func (r *testRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// gitOld runs a git command whose commit lands outside the retention window.
func (r *testRepo) gitOld(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+oldCommitDate, "GIT_COMMITTER_DATE="+oldCommitDate)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// write creates a file below the repository root.
func (r *testRepo) write(path, content string) {
	r.t.Helper()
	full := filepath.Join(r.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// pointerAt returns the contents of a pointer file naming oid.
func pointerAt(oid string) string {
	return string(pointer.New(oid, 4096, "").Bytes())
}

func oidOf(seed byte) string {
	return strings.Repeat(string("0123456789abcdef"[seed%16]), 64)
}

// expireReflog drops the history that lets git recover abandoned commits, so
// that a test sees only what refs still point at.
func (r *testRepo) expireReflog() {
	r.t.Helper()
	r.git("reflog", "expire", "--expire=now", "--all")
}

// referenced collects what prune would keep for this repository.
func (r *testRepo) referenced(window time.Duration) map[string]bool {
	r.t.Helper()
	r.t.Chdir(r.dir)
	e, err := newEnv()
	if err != nil {
		r.t.Fatalf("newEnv: %v", err)
	}
	got, err := e.referencedOIDs(window)
	if err != nil {
		r.t.Fatalf("referencedOIDs: %v", err)
	}
	return got
}

// A version of a tracked file kept only by an old, pushed commit is dropped
// once it falls out of the retention window.
func TestPruneForgetsPushedHistoryOutsideWindow(t *testing.T) {
	r := newTestRepo(t)
	oid := oidOf(11)

	r.write("data/large.jsonl", pointerAt(oid))
	r.git("add", "-A")
	r.gitOld("commit", "-qm", "add the dataset")

	// A later commit removes the file, so nothing but history refers to it.
	r.git("rm", "-q", "data/large.jsonl")
	r.git("commit", "-qm", "drop the dataset")

	// Pretend the whole thing was pushed, so no commit counts as local-only.
	r.git("update-ref", "refs/remotes/origin/main", "HEAD")
	r.expireReflog()

	if !r.referenced(0)[oid] {
		t.Error("with no window every commit's objects should be kept")
	}
	if r.referenced(time.Hour)[oid] {
		t.Error("an old pushed version should fall out of the window")
	}
}

// The same history is kept when it has never been pushed, because the local
// cache is then the only copy of those objects.
func TestPruneKeepsUnpushedHistory(t *testing.T) {
	r := newTestRepo(t)
	oid := oidOf(12)

	r.write("data/large.jsonl", pointerAt(oid))
	r.git("add", "-A")
	r.gitOld("commit", "-qm", "add the dataset")
	r.git("rm", "-q", "data/large.jsonl")
	r.git("commit", "-qm", "drop the dataset")

	if !r.referenced(time.Hour)[oid] {
		t.Error("unpushed commits must keep their objects")
	}
}

// A branch tip keeps its objects even when the commit is old, so switching to
// it does not need a download.
func TestPruneKeepsRefTips(t *testing.T) {
	r := newTestRepo(t)
	oid := oidOf(13)

	r.write("data/large.jsonl", pointerAt(oid))
	r.git("add", "-A")
	r.gitOld("commit", "-qm", "add the dataset")
	r.git("branch", "dataset")

	// The dataset is gone from the checked out branch, so only the other
	// branch's tip still holds the pointer.
	r.git("rm", "-q", "data/large.jsonl")
	r.git("commit", "-qm", "drop the dataset")
	r.git("update-ref", "refs/remotes/origin/main", "HEAD")

	if !r.referenced(time.Hour)[oid] {
		t.Error("a branch tip must keep its objects regardless of age")
	}
}

// Whatever the checked out tree uses stays cached, which is what makes a
// checkout work with no network at all.
func TestPruneKeepsWorkingTree(t *testing.T) {
	r := newTestRepo(t)
	oid := oidOf(14)

	r.write("data/large.jsonl", pointerAt(oid))
	r.git("add", "-A")
	r.gitOld("commit", "-qm", "add the dataset")
	r.git("update-ref", "refs/remotes/origin/main", "HEAD")

	if !r.referenced(time.Hour)[oid] {
		t.Error("the current checkout must always be kept")
	}
}

// A staged object that no commit has picked up yet is still only local.
func TestPruneKeepsStagedPointer(t *testing.T) {
	r := newTestRepo(t)
	old := oidOf(15)
	staged := oidOf(16)

	r.write("data/large.jsonl", pointerAt(old))
	r.git("add", "-A")
	r.gitOld("commit", "-qm", "add the dataset")

	// Retire the old version everywhere before staging the new one, so that
	// only the index and the working tree refer to it.
	r.git("rm", "-q", "data/large.jsonl")
	r.git("commit", "-qm", "drop the dataset")
	r.git("update-ref", "refs/remotes/origin/main", "HEAD")
	r.expireReflog()

	r.write("data/large.jsonl", pointerAt(staged))
	r.git("add", "-A")

	got := r.referenced(time.Hour)
	if !got[staged] {
		t.Error("a staged pointer must be kept")
	}
	if got[old] {
		t.Error("the retired old version should fall out of the window")
	}
}

// A stash holds files you are not otherwise using, and must not be pruned.
func TestPruneKeepsStash(t *testing.T) {
	r := newTestRepo(t)
	oid := oidOf(17)

	r.write("data/large.jsonl", pointerAt(oid))
	r.git("add", "-A")
	r.gitOld("commit", "-qm", "add the dataset")
	r.git("update-ref", "refs/remotes/origin/main", "HEAD")

	// Stash the file away, leaving a clean tree without it.
	r.git("rm", "--cached", "-q", "data/large.jsonl")
	if err := os.Remove(filepath.Join(r.dir, "data/large.jsonl")); err != nil {
		t.Fatal(err)
	}
	r.git("stash", "push", "-q", "-u")

	if !r.referenced(time.Hour)[oid] {
		t.Error("a stash must keep its objects")
	}
}
