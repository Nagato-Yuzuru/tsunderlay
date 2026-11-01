//go:build darwin

package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// openLog opens path the same as launchd opens StandardErrorPath.
func openLog(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() }) //nolint:errcheck // a test file
	return f
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

// rotate does what newsyslog does: it renames the file, and makes a new one
// at path.
func rotate(t *testing.T, path string) {
	t.Helper()
	if err := os.Rename(path, path+".0"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLogFollowsRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tsunderlay.log")
	f := openLog(t, path)
	w := followLog(f, path)

	write(t, w, "before\n")
	rotate(t, path)
	write(t, w, "after\n")
	// A write to the descriptor itself, the same as a panic of the runtime.
	write(t, f, "direct\n")

	checkContent(t, path+".0", "before\n")
	checkContent(t, path, "after\ndirect\n")
}

// Between the rename and the create of newsyslog, the lines go to the old
// file. The first write after the create goes to the new file.
func TestLogKeepsTheOldFileUntilTheNewFileExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tsunderlay.log")
	f := openLog(t, path)
	w := followLog(f, path)

	if err := os.Rename(path, path+".0"); err != nil {
		t.Fatal(err)
	}
	write(t, w, "between\n")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	write(t, w, "after\n")

	checkContent(t, path+".0", "between\n")
	checkContent(t, path, "after\n")
}

// A daemon that an operator runs in a terminal does not write to the file
// at path.
func TestLogDoesNotFollowAnotherFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tsunderlay.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	other := openLog(t, filepath.Join(dir, "terminal"))

	if w := followLog(other, path); w != io.Writer(other) {
		t.Errorf("followLog = %T, want the file itself", w)
	}
}
