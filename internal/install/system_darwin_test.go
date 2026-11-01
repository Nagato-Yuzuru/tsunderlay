//go:build darwin

package install

import (
	"errors"
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
