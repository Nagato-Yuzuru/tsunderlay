//go:build darwin

package install

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

// exitWith returns the error of a process that exits with status code.
func exitWith(t *testing.T, code string) error {
	t.Helper()
	return exec.CommandContext(t.Context(), "/bin/sh", "-c", "exit "+code).Run()
}

func TestPrintResult(t *testing.T) {
	killed := exec.CommandContext(t.Context(), "/bin/sh", "-c", "kill -KILL $$").Run()
	tests := []struct {
		name       string
		err        error
		wantLoaded bool
		wantErr    error
	}{
		{name: "status 0 is loaded", err: nil, wantLoaded: true},
		{name: "status 113 is not loaded", err: exitWith(t, "113"), wantLoaded: false},
		{name: "a usage error does not tell the state", err: exitWith(t, "64"), wantErr: ErrLaunchctl},
		{name: "a kill does not tell the state", err: killed, wantErr: ErrLaunchctl},
		{name: "launchctl did not run", err: exec.ErrNotFound, wantErr: ErrLaunchctl},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loaded, err := printResult(Service, tt.err)
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Fatalf("printResult error = %v, want %v", err, tt.wantErr)
			}
			if loaded != tt.wantLoaded {
				t.Errorf("printResult loaded = %v, want %v", loaded, tt.wantLoaded)
			}
		})
	}
}

// TestLockIsExclusiveUntilClose probes the lock with a non-blocking flock
// on a second open file, so the test needs no wait.
func TestLockIsExclusiveUntilClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install.lock")
	lock, err := OS{}.Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	probe := func() error {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close() //nolint:errcheck // a read-only probe; the close releases its lock
		return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	}

	if err := probe(); !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("flock while Lock holds the file: %v, want EWOULDBLOCK", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := probe(); err != nil {
		t.Errorf("flock after Close: %v, want the lock", err)
	}
}

// Uninstall removes the lock file while it holds the lock. A Lock that
// waited on the removed file must not return with it: a new process makes a
// new file and locks it at the same time. So the lock that Lock returns is
// on the file at the path, in each order of the open and the remove.
//
// The waiter opens the file before the remove in most rounds, not in each.
// Many rounds make a Lock without the check fail on each run.
func TestLockAfterRemoveIsOnTheFileAtThePath(t *testing.T) {
	const rounds = 200
	dir := t.TempDir()
	for i := range rounds {
		lockRound(t, filepath.Join(dir, fmt.Sprintf("install-%d.lock", i)))
		if t.Failed() {
			t.Fatalf("round %d of %d failed", i+1, rounds)
		}
	}
}

// lockRound removes the lock file while a second Lock waits, and checks
// that the second Lock holds the file at path.
func lockRound(t *testing.T, path string) {
	t.Helper()
	first, err := OS{}.Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		lock io.Closer
		err  error
	}
	done := make(chan result)
	go func() {
		l, err := OS{}.Lock(path)
		done <- result{l, err}
	}()

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := <-done
	if second.err != nil {
		t.Fatal(second.err)
	}
	defer second.lock.Close() //nolint:errcheck // the round ends; the close releases the lock

	f, err := os.Open(path)
	if err != nil {
		t.Errorf("no file at the path while Lock holds the lock: %v", err)
		return
	}
	defer f.Close() //nolint:errcheck // a read-only probe; the close releases its lock
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Errorf("flock of the file at the path: %v, want EWOULDBLOCK", err)
	}
}
