//go:build e2e && e2e_guest && darwin

package e2e

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/install"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/testenv"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/testnet"
)

// TestInstalledDaemonAddsOwnedRoutesWhenVPNComesUp installs the daemon,
// runs the "VPN up" scenario against the daemon that launchd runs, installs
// again, and uninstalls. The name starts with TestInstall, so the e2e tasks
// run it alone, after the other tests, with TestInstall.
func TestInstalledDaemonAddsOwnedRoutesWhenVPNComesUp(t *testing.T) {
	lan := guestScenario(t)
	bin := realPath(t, testenv.RequireExecutable(t, testenv.EnvBinary))
	if _, err := os.Lstat(install.PlistPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s before the test: %v; the test needs a guest without an installed daemon", install.PlistPath, err)
	}
	editHosts(t).Map(t, daemonHost, controlAddr)
	// This cleanup runs before the cleanups of guestScenario and editHosts,
	// so the daemon is gone when they restore the guest.
	t.Cleanup(func() { uninstall(t, bin) })

	at := logSize(t)
	tsunderlay(t, bin, "install", "--control-url", daemonControlURL())
	if loaded, err := (install.Launchctl{}).Loaded(t.Context(), install.Service); !loaded || err != nil {
		t.Fatalf("launchd has no %s after install (loaded %v, %v)", install.Service, loaded, err)
	}
	waitStart(t, at, []string{daemonControlURL()})
	d := launchdDaemon(t, bin, at)
	d.waitWatching(t)

	vpn := testnet.NewFakeVPN(t)
	up := time.Now()
	waitTable(t, changeWithin, "owned R1 and R2", hasAll(wantR1(lan, controlNet), wantR2(vpn)))
	t.Logf("owned R1 and R2 in place %v after the fake VPN came up on %s", time.Since(up), vpn.Iface())
	requireR1Path(t, lan, controlAddr)
	requireR2Path(t, vpn)
	if code, out := d.check(t); code != 0 {
		t.Errorf("tsunderlay check exit %d, want 0:\n%s", code, out)
	}

	t.Log("second install with the same binary and flags")
	pid := daemonPID(t)
	out := tsunderlay(t, bin, "install", "--control-url", daemonControlURL())
	if lines := strings.Split(strings.TrimSpace(out), "\n"); lines[len(lines)-1] != "no change" {
		t.Errorf("second install output = %q, want it to end with %q", out, "no change")
	}
	if got := daemonPID(t); got != pid {
		t.Errorf("daemon pid after the second install = %s, want %s: launchd restarted the daemon", got, pid)
	}
	if !hasAll(wantR1(lan, controlNet), wantR2(vpn))(fetch(t)) {
		t.Errorf("owned R1 or R2 is gone after the second install; owned routes: %+v", ownedRoutes(fetch(t)))
	}

	t.Log("uninstall while the VPN is up")
	tsunderlay(t, bin, "uninstall")
	checkUninstalled(t)
	if owned := ownedRoutes(fetch(t)); len(owned) != 0 {
		t.Errorf("owned routes after uninstall = %+v, want none", owned)
	}
}

// launchdDaemon returns a daemonProc for the daemon that launchd runs. Its
// log has the lines of install.LogPath from the offset at on. The tests use
// it for poke, events, and check only: launchd, not the test, stops this
// daemon.
func launchdDaemon(t *testing.T, bin string, at int64) *daemonProc {
	t.Helper()
	sink := newLogSink()
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		followLog(t, at, sink, stop)
	}()
	t.Cleanup(func() {
		close(stop)
		<-done
		if t.Failed() {
			for _, l := range sink.snapshot() {
				t.Logf("daemon: %s", l)
			}
		}
	})
	return &daemonProc{bin: bin, log: sink}
}

// followLog copies the bytes of install.LogPath from the offset at on to w
// until stop closes. launchd gives no signal for a new line, so followLog
// polls.
func followLog(t *testing.T, at int64, w io.Writer, stop <-chan struct{}) {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if b, err := os.ReadFile(install.LogPath); err == nil && int64(len(b)) > at {
			_, _ = w.Write(b[at:]) //nolint:errcheck // a logSink write does not fail
			at = int64(len(b))
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("read %s: %v", install.LogPath, err)
			return
		}
		select {
		case <-stop:
			return
		case <-tick.C:
		}
	}
}

// daemonPID returns the pid that launchctl print shows for the job.
func daemonPID(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "/bin/launchctl", "print", install.Service).CombinedOutput()
	if err != nil {
		t.Fatalf("launchctl print %s: %v\n%s", install.Service, err, out)
	}
	for line := range strings.Lines(string(out)) {
		if pid, ok := strings.CutPrefix(strings.TrimSpace(line), "pid = "); ok {
			return pid
		}
	}
	t.Fatalf("launchctl print %s shows no pid:\n%s", install.Service, out)
	return ""
}
