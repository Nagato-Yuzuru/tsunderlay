//go:build e2e && darwin

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/install"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/testenv"
)

// startWait is the longest wait for a start event of the daemon in the log.
const startWait = 15 * time.Second

// TestInstall installs the daemon into the system domain of launchd, the
// same as an operator. It then upgrades the binary and changes the flags,
// and checks each time that launchd runs the new daemon. The cleanup boots
// the job out and removes its files.
//
// The daemon deletes the owned routes that it does not need, also the
// routes of the other tests. So the e2e tasks run this test alone, after
// the other tests: a daemon that the cleanup cannot stop then cannot change
// the routes of another test.
func TestInstall(t *testing.T) {
	testenv.RequireDisposableHost(t)
	testenv.RequireRoot(t)
	// install prints the real path of the binary that it copies.
	bin := realPath(t, testenv.RequireExecutable(t, testenv.EnvBinary))
	next := realPath(t, testenv.RequireExecutable(t, testenv.EnvBinaryNext))
	if _, err := os.Lstat(install.PlistPath); err == nil {
		t.Fatalf("%s exists before the test; the test needs a host without an installed daemon", install.PlistPath)
	}
	// Loaded is false only when launchctl print exits 113.
	if loaded, err := (install.Launchctl{}).Loaded(t.Context(), install.Service); loaded || err != nil {
		t.Fatalf("launchd has %s before the test (loaded %v, %v); the test needs a host without it", install.Service, loaded, err)
	}
	t.Cleanup(func() { uninstall(t) })

	t.Log("first install")
	at := logSize(t)
	out := tsunderlay(t, bin, "install")
	for _, want := range []string{
		"copied " + bin + " to " + install.BinaryPath,
		"wrote " + install.PlistPath,
		"bootstrapped " + install.Service,
	} {
		if !slices.Contains(strings.Split(strings.TrimSpace(out), "\n"), want) {
			t.Errorf("first install output has no line %q", want)
		}
	}
	checkOwner(t, install.BinaryPath, 0o755)
	checkOwner(t, install.PlistPath, 0o644)
	if out, err := exec.CommandContext(t.Context(), "/bin/launchctl", "print", install.Service).CombinedOutput(); err != nil {
		t.Fatalf("launchctl print %s: %v\n%s", install.Service, err, out)
	}
	waitStart(t, at, []string{"https://controlplane.tailscale.com"})

	t.Log("second install with the same binary and flags")
	if out := tsunderlay(t, bin, "install"); out != "no change\n" {
		t.Errorf("second install output = %q, want %q", out, "no change\n")
	}

	t.Log("upgrade: install from a binary with other bytes")
	at = logSize(t)
	out = tsunderlay(t, next, "install")
	if want := "copied " + next + " to " + install.BinaryPath + "\nrestarted " + install.Service + "\n"; out != want {
		t.Errorf("upgrade output = %q, want %q", out, want)
	}
	waitStart(t, at, []string{"https://controlplane.tailscale.com"})

	t.Log("new flags")
	url := "https://hs.example.com"
	at = logSize(t)
	out = tsunderlay(t, next, "install", "--control-url", url)
	if want := "wrote " + install.PlistPath + "\nreloaded " + install.Service + "\n"; out != want {
		t.Errorf("new flags output = %q, want %q", out, want)
	}
	waitStart(t, at, []string{url})
}

// tsunderlay runs the binary at path and returns its standard output. It
// fails the test on a non-zero exit.
func tsunderlay(t *testing.T, path string, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(t.Context(), path, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("tsunderlay %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, &stdout, &stderr)
	}
	t.Logf("tsunderlay %s:\n%s", strings.Join(args, " "), &stdout)
	return stdout.String()
}

func realPath(t *testing.T, path string) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func checkOwner(t *testing.T, path string, perm fs.FileMode) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("%s: no stat_t", path)
	}
	if st.Uid != 0 || st.Gid != 0 || fi.Mode() != perm {
		t.Errorf("%s: uid %d, gid %d, mode %v; want root:wheel %v", path, st.Uid, st.Gid, fi.Mode(), perm)
	}
}

// uninstall boots the job out and removes its files.
func uninstall(t *testing.T) {
	t.Helper()
	// t.Context has ended when the cleanups run.
	if err := (install.Launchctl{}).Bootout(context.WithoutCancel(t.Context()), install.Service); err != nil {
		t.Errorf("cleanup: %v", err)
	}
	for _, p := range []string{install.PlistPath, install.BinaryPath, install.PendingPath, install.LockPath} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("cleanup: %v", err)
		}
	}
}

// logSize returns the size of install.LogPath, 0 when it does not exist.
func logSize(t *testing.T) int64 {
	t.Helper()
	fi, err := os.Stat(install.LogPath)
	if errors.Is(err, fs.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// waitStart waits for the first start event after the offset at in
// install.LogPath, and checks that the daemon keeps the control URLs urls.
// launchd gives no signal when the daemon started, so waitStart reads the
// log until the event is there.
func waitStart(t *testing.T, at int64, urls []string) {
	t.Helper()
	deadline := time.Now().Add(startWait)
	for {
		if got, ok := firstStart(t, at); ok {
			if !slices.Equal(got, urls) {
				t.Errorf("start event has control URLs %q, want %q", got, urls)
			}
			return
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(install.LogPath) //nolint:errcheck // the content is only a hint for the failure
			t.Fatalf("no start event in %s after %v; the log has:\n%s", install.LogPath, startWait, b)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// firstStart returns the control URLs of the first start event after the
// offset at.
func firstStart(t *testing.T, at int64) ([]string, bool) {
	t.Helper()
	b, err := os.ReadFile(install.LogPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(b[min(at, int64(len(b))):])) {
		var e struct {
			Msg  string   `json:"msg"`
			URLs []string `json:"tsunderlay.control.urls"`
		}
		if json.Unmarshal([]byte(line), &e) == nil && e.Msg == "start" {
			return e.URLs, true
		}
	}
	return nil, false
}
