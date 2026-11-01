//go:build darwin

package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// newStderr returns a file that stands for the standard error of the
// process: openLog puts the log on its descriptor.
func newStderr(t *testing.T, dir string) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() }) //nolint:errcheck // a test file
	return f
}

func mustOpenLog(t *testing.T, f *os.File, path string) io.Writer {
	t.Helper()
	w, err := openLog(f, path)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func write(t *testing.T, w io.Writer, s string) {
	t.Helper()
	if _, err := io.WriteString(w, s); err != nil {
		t.Fatal(err)
	}
}

func checkContent(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != want {
		t.Errorf("%s = %q, want %q", path, b, want)
	}
}

// rotate does what newsyslog of macOS does: it links path to path.0, makes
// the new file at a temporary name, and renames it to path. path always
// names a file.
func rotate(t *testing.T, path string) {
	t.Helper()
	if err := os.Link(path, path+".0"); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(filepath.Dir(path), "newsyslog.tmp")
	if err := os.WriteFile(tmp, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// A write to the descriptor goes to the log, not to the old standard error.
func TestLogIsOnTheDescriptor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tsunderlay.log")
	f := newStderr(t, dir)

	w := mustOpenLog(t, f, path)
	write(t, w, "logger\n")
	write(t, f, "direct\n")

	checkContent(t, path, "logger\ndirect\n")
	checkContent(t, filepath.Join(dir, "stderr"), "")
}

func TestLogFollowsRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tsunderlay.log")
	f := newStderr(t, dir)
	w := mustOpenLog(t, f, path)

	write(t, w, "before\n")
	rotate(t, path)
	// A write to the descriptor before the next write of the logger, the
	// same as a crash report of the runtime, goes to the rotated file.
	write(t, f, "crash\n")
	write(t, w, "after\n")
	write(t, f, "direct\n")

	checkContent(t, path+".0", "before\ncrash\n")
	checkContent(t, path, "after\ndirect\n")
}

// An operator who removes the log gets a new file at the next write.
func TestLogMakesTheFileAgainAfterARemove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tsunderlay.log")
	f := newStderr(t, dir)
	w := mustOpenLog(t, f, path)
	write(t, w, "before\n")

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	write(t, w, "after\n")

	checkContent(t, path, "after\n")
}
