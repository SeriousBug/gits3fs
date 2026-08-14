// Package e2e drives the real git-s3fs binary through real git against a fake
// S3 endpoint, covering the paths that only exist when git is the one calling
// us: the clean and smudge filters, the pre-push hook, and a fresh clone.
package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/SeriousBug/gits3fs/internal/pointer"
	"github.com/SeriousBug/gits3fs/internal/testutil/fakes3"
)

// binDir holds the git-s3fs binary built for the test run.
var binDir string

// binName is the file name of the built binary; Windows will not execute it
// without the extension.
var binName = "git-s3fs" + exeSuffix()

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gits3fs-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(dir, binName), "../cmd/git-s3fs")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building git-s3fs: %v\n%s", err, out)
		os.Exit(1)
	}
	binDir = dir

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fixture is an isolated git and AWS environment for one test.
type fixture struct {
	t    *testing.T
	root string
	env  []string
	s3   *fakes3.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	srv := fakes3.New()
	t.Cleanup(srv.Close)

	f := &fixture{t: t, root: root, s3: srv}
	f.env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME="+home,
		// Keep the developer's real git and AWS configuration out of the test.
		"GIT_CONFIG_GLOBAL="+filepath.Join(home, ".gitconfig"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		"AWS_ACCESS_KEY_ID=test", "AWS_SECRET_ACCESS_KEY=test",
		"AWS_REGION=us-east-1", "AWS_EC2_METADATA_DISABLED=true",
		"AWS_CONFIG_FILE="+filepath.Join(home, "aws-config"),
		"AWS_SHARED_CREDENTIALS_FILE="+filepath.Join(home, "aws-credentials"),
	)
	return f
}

// run executes a command in dir and fails the test if it does not succeed.
func (f *fixture) run(dir string, extraEnv []string, name string, args ...string) string {
	f.t.Helper()
	out, err := f.tryRun(dir, extraEnv, name, args...)
	if err != nil {
		f.t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return out
}

func (f *fixture) tryRun(dir string, extraEnv []string, name string, args ...string) (string, error) {
	f.t.Helper()
	// exec resolves the program against the parent's PATH, not cmd.Env, so
	// point at the freshly built binary directly. Anything git spawns still
	// finds it through the PATH we set below.
	if name == "git-s3fs" {
		name = filepath.Join(binDir, binName)
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(append([]string{}, f.env...), extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (f *fixture) git(dir string, args ...string) string {
	f.t.Helper()
	return f.run(dir, nil, "git", args...)
}

func (f *fixture) s3fs(dir string, args ...string) string {
	f.t.Helper()
	return f.run(dir, nil, "git-s3fs", args...)
}

func (f *fixture) path(parts ...string) string {
	return filepath.Join(append([]string{f.root}, parts...)...)
}

// setup creates a bare origin and a working repository configured for the
// fake S3 endpoint, with the filters installed globally so that clones pick
// them up too.
func (f *fixture) setup() (origin, work string) {
	f.t.Helper()
	origin = f.path("origin.git")
	work = f.path("work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		f.t.Fatal(err)
	}
	f.git(f.root, "init", "--bare", "-b", "main", origin)
	f.git(work, "init", "-b", "main")
	f.git(work, "remote", "add", "origin", origin)

	f.s3fs(work, "install", "--global")
	f.s3fs(work, "init",
		"--bucket", "test-bucket",
		"--region", "us-east-1",
		"--endpoint", f.s3.Endpoint(),
		"--path-style",
		"--prefix", "assets")
	return origin, work
}

// bigContent returns deterministic content large enough to be worth storing
// out of band.
func bigContent(seed byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = seed + byte(i%251)
	}
	return out
}

func TestCommitPushCloneRoundTrip(t *testing.T) {
	f := newFixture(t)
	origin, work := f.setup()

	content := bigContent(7, 1<<20)
	if err := os.WriteFile(filepath.Join(work, "data.bin"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	f.s3fs(work, "track", "*.bin")
	f.git(work, "add", "-A")
	f.git(work, "commit", "-m", "Add a large file")

	// What git stored must be a pointer, and it must carry a usable URL.
	blob := f.git(work, "cat-file", "blob", "HEAD:data.bin")
	p, err := pointer.Parse([]byte(blob))
	if err != nil {
		t.Fatalf("the committed blob is not a pointer: %v\n%s", err, blob)
	}
	if p.Size != int64(len(content)) {
		t.Errorf("pointer size = %d, want %d", p.Size, len(content))
	}
	wantURL := f.s3.Endpoint() + "/test-bucket/assets/objects/" + p.OID[0:2] + "/" + p.OID[2:4] + "/" + p.OID
	if p.URL != wantURL {
		t.Errorf("pointer URL =\n %q\nwant %q", p.URL, wantURL)
	}

	// The working tree keeps the real bytes.
	if got, _ := os.ReadFile(filepath.Join(work, "data.bin")); !bytes.Equal(got, content) {
		t.Error("the working tree file was replaced by a pointer")
	}

	// Nothing has been uploaded yet.
	if len(f.s3.Objects()) != 0 {
		t.Errorf("objects were uploaded before the push: %v", f.s3.Objects())
	}

	// The pre-push hook uploads as part of the push.
	f.git(work, "push", "-u", "origin", "main")
	key := "assets/objects/" + p.OID[0:2] + "/" + p.OID[2:4] + "/" + p.OID
	stored, ok := f.s3.Get(key)
	if !ok {
		t.Fatalf("the object was not uploaded to %q; keys are %v", key, objectKeys(f.s3))
	}
	if !bytes.Equal(stored, content) {
		t.Errorf("uploaded %d bytes, want %d", len(stored), len(content))
	}

	// A fresh clone gets the real content back through the smudge filter.
	clone := f.path("clone")
	f.git(f.root, "clone", origin, clone)
	got, err := os.ReadFile(filepath.Join(clone, "data.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("the clone has %d bytes, want %d", len(got), len(content))
	}
	if _, err := os.Stat(filepath.Join(clone, ".gits3fs")); err != nil {
		t.Errorf(".gits3fs was not committed, so the clone had to guess: %v", err)
	}
}

func TestCleanFilterIsDeterministic(t *testing.T) {
	f := newFixture(t)
	_, work := f.setup()

	content := bigContent(3, 4096)
	if err := os.WriteFile(filepath.Join(work, "a.bin"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	f.s3fs(work, "track", "*.bin")
	f.git(work, "add", "-A")
	f.git(work, "commit", "-m", "Add")

	// Re-staging identical content must produce an identical pointer, or git
	// would report changes that are not there.
	f.git(work, "add", "-A")
	if status := f.git(work, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("re-staging produced a diff:\n%s", status)
	}
}

func TestPointerFilesArePassedThroughUnchanged(t *testing.T) {
	f := newFixture(t)
	_, work := f.setup()

	f.s3fs(work, "track", "*.bin")
	content := bigContent(11, 2048)
	if err := os.WriteFile(filepath.Join(work, "a.bin"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	f.git(work, "add", "-A")
	f.git(work, "commit", "-m", "Add")
	first := f.git(work, "cat-file", "blob", "HEAD:a.bin")

	// Write the pointer itself into the file, as a checkout without content
	// would, and re-stage it. Cleaning a pointer must not wrap it in another
	// pointer.
	if err := os.WriteFile(filepath.Join(work, "a.bin"), []byte(first+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.git(work, "add", "-A")
	second := f.git(work, "cat-file", "blob", ":a.bin")
	if strings.TrimSpace(second) != strings.TrimSpace(first) {
		t.Errorf("a pointer was re-cleaned:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestLazyCheckoutThenPull(t *testing.T) {
	f := newFixture(t)
	origin, work := f.setup()

	content := bigContent(5, 64<<10)
	if err := os.WriteFile(filepath.Join(work, "big.bin"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	f.s3fs(work, "track", "*.bin")
	f.git(work, "add", "-A")
	f.git(work, "commit", "-m", "Add")
	f.git(work, "push", "-u", "origin", "main")

	// A lazy clone leaves pointers in the working tree.
	clone := f.path("lazy")
	f.run(f.root, []string{"GITS3FS_LAZY=1"}, "git", "clone", origin, clone)
	raw, err := os.ReadFile(filepath.Join(clone, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pointer.Parse(raw); err != nil {
		t.Fatalf("a lazy clone should leave a pointer in place, got %d bytes", len(raw))
	}

	// Pulling materialises it.
	f.s3fs(clone, "pull")
	got, err := os.ReadFile(filepath.Join(clone, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("after pull the file has %d bytes, want %d", len(got), len(content))
	}
}

func TestPushIsIdempotent(t *testing.T) {
	f := newFixture(t)
	_, work := f.setup()

	if err := os.WriteFile(filepath.Join(work, "a.bin"), bigContent(1, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	f.s3fs(work, "track", "*.bin")
	f.git(work, "add", "-A")
	f.git(work, "commit", "-m", "Add")

	f.s3fs(work, "push")
	uploads := f.s3.Count("PUT")
	if uploads == 0 {
		t.Fatal("nothing was uploaded")
	}
	out := f.s3fs(work, "push")
	if f.s3.Count("PUT") != uploads {
		t.Errorf("the second push re-uploaded objects:\n%s", out)
	}
	if !strings.Contains(out, "already present") {
		t.Errorf("push should report what it skipped:\n%s", out)
	}
}

func TestStatusLsFilesAndURL(t *testing.T) {
	f := newFixture(t)
	_, work := f.setup()

	if err := os.WriteFile(filepath.Join(work, "logo.bin"), bigContent(9, 512), 0o644); err != nil {
		t.Fatal(err)
	}
	f.s3fs(work, "track", "*.bin")
	f.git(work, "add", "-A")
	f.git(work, "commit", "-m", "Add")

	if out := f.s3fs(work, "status", "--no-remote"); !strings.Contains(out, "logo.bin") && !strings.Contains(out, "1 file") {
		t.Errorf("status did not mention the tracked file:\n%s", out)
	}
	if out := f.s3fs(work, "ls-files"); !strings.Contains(out, "logo.bin") {
		t.Errorf("ls-files output:\n%s", out)
	}
	url := strings.TrimSpace(f.s3fs(work, "url", "logo.bin"))
	if !strings.HasPrefix(url, f.s3.Endpoint()+"/test-bucket/assets/objects/") {
		t.Errorf("url = %q", url)
	}

	// The URL in the pointer and the one the command prints must agree.
	blob := f.git(work, "cat-file", "blob", "HEAD:logo.bin")
	p, err := pointer.Parse([]byte(blob))
	if err != nil {
		t.Fatal(err)
	}
	if p.URL != url {
		t.Errorf("`url` printed %q but the pointer says %q", url, p.URL)
	}
}

func TestUntrackedFilesAreLeftAlone(t *testing.T) {
	f := newFixture(t)
	_, work := f.setup()

	f.s3fs(work, "track", "*.bin")
	if err := os.WriteFile(filepath.Join(work, "notes.txt"), []byte("plain text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.git(work, "add", "-A")
	f.git(work, "commit", "-m", "Add notes")

	if got := f.git(work, "cat-file", "blob", "HEAD:notes.txt"); strings.TrimSpace(got) != "plain text" {
		t.Errorf("an untracked file went through the filter: %q", got)
	}
}

func TestDoctorReportsAHealthySetup(t *testing.T) {
	f := newFixture(t)
	_, work := f.setup()
	f.s3fs(work, "track", "*.bin")

	out, err := f.tryRun(work, nil, "git-s3fs", "doctor")
	if err != nil {
		t.Fatalf("doctor reported failures: %v\n%s", err, out)
	}
	for _, want := range []string{"clean/smudge filter", "hooks", "bucket", "tracked patterns"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor did not check %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "[FAIL]") {
		t.Errorf("doctor found failures:\n%s", out)
	}
}

func TestMigrateImportConvertsExistingFiles(t *testing.T) {
	f := newFixture(t)
	_, work := f.setup()

	content := bigContent(13, 8192)
	if err := os.WriteFile(filepath.Join(work, "old.bin"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	// Commit it as an ordinary file first.
	f.git(work, "add", "-A")
	f.git(work, "commit", "-m", "Add as a plain file")
	if blob := f.git(work, "cat-file", "blob", "HEAD:old.bin"); len(blob) < 8000 {
		t.Fatalf("expected the raw content in git, got %d bytes", len(blob))
	}

	f.s3fs(work, "migrate", "import", "--include", "*.bin")
	f.git(work, "commit", "-m", "Convert to git-s3fs")

	blob := f.git(work, "cat-file", "blob", "HEAD:old.bin")
	p, err := pointer.Parse([]byte(blob))
	if err != nil {
		t.Fatalf("migrate did not convert the file: %v\n%s", err, blob)
	}
	if p.Size != int64(len(content)) {
		t.Errorf("pointer size = %d, want %d", p.Size, len(content))
	}
	if got, _ := os.ReadFile(filepath.Join(work, "old.bin")); !bytes.Equal(got, content) {
		t.Error("the working tree copy changed during migration")
	}
}

func TestUntrackStopsFiltering(t *testing.T) {
	f := newFixture(t)
	_, work := f.setup()

	f.s3fs(work, "track", "*.bin")
	f.s3fs(work, "untrack", "*.bin")
	if err := os.WriteFile(filepath.Join(work, "a.bin"), []byte("raw bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.git(work, "add", "-A")
	f.git(work, "commit", "-m", "Add")

	if got := f.git(work, "cat-file", "blob", "HEAD:a.bin"); strings.TrimSpace(got) != "raw bytes" {
		t.Errorf("the file was still filtered: %q", got)
	}
}

func objectKeys(srv *fakes3.Server) []string {
	var out []string
	for k := range srv.Objects() {
		out = append(out, k)
	}
	return out
}
