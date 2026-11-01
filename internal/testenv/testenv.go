// Package testenv decides whether the current host can run an end-to-end
// test. When a requirement is not met, the test skips with a reason. In
// strict mode (TSUNDERLAY_E2E_STRICT=1, set by CI), the test fails instead,
// so that a broken environment cannot pass as a skip.
package testenv

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// DisposableMarker is the file that marks a host whose network state a test
// can change: a CI runner or a disposable VM. End-to-end tests add routes and
// interfaces, so they never run on a host without this file. The file must
// be owned by root, so that only root can mark a host. The e2e mise task
// does not create it; the CI job and `mise run vm:e2e` do.
const DisposableMarker = "/var/db/tsunderlay-e2e-disposable"

const (
	// EnvStrict turns every unmet requirement into a failure.
	EnvStrict = "TSUNDERLAY_E2E_STRICT"
	// EnvBinary is the path of the tsunderlay binary that `mise run
	// e2e:build` builds.
	EnvBinary = "TSUNDERLAY_E2E_BINARY"
	// EnvBinaryNext is the path of a second tsunderlay binary. It has other
	// bytes than EnvBinary and the same behavior, so it acts as an upgrade.
	EnvBinaryNext = "TSUNDERLAY_E2E_BINARY_NEXT"
)

// RequireDisposableHost skips the test unless DisposableMarker exists and
// root owns it.
func RequireDisposableHost(t testing.TB) {
	t.Helper()
	if err := checkMarker(DisposableMarker, 0); err != nil {
		unmet(t, err.Error()+"; the host is not disposable. Run the tests through `mise run vm:e2e` or CI")
	}
}

var (
	errMarkerMissing   = errors.New("disposable marker does not exist")
	errMarkerNoOwner   = errors.New("disposable marker has no owner information")
	errMarkerWrongUser = errors.New("disposable marker has the wrong owner")
)

// checkMarker returns nil when path exists and its owner has the uid owner.
func checkMarker(path string, owner uint32) error {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", errMarkerMissing, path)
	}
	if err != nil {
		return fmt.Errorf("disposable marker: %w", err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%w: %s", errMarkerNoOwner, path)
	}
	if st.Uid != owner {
		return fmt.Errorf("%w: %s is owned by uid %d, not uid %d", errMarkerWrongUser, path, st.Uid, owner)
	}
	return nil
}

// RequireRoot skips the test unless the process runs as root.
func RequireRoot(t testing.TB) {
	t.Helper()
	if os.Geteuid() != 0 {
		unmet(t, "requires root")
	}
}

// RequireCommand skips the test unless name is in PATH. An absolute name
// must be an executable file.
func RequireCommand(t testing.TB, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		unmet(t, "requires command "+name+": "+err.Error())
	}
}

// RequireExecutable returns the path in the environment variable env. It
// skips the test unless the path is an executable file.
func RequireExecutable(t testing.TB, env string) string {
	t.Helper()
	path := os.Getenv(env)
	if path == "" {
		unmet(t, env+" is not set; run through `mise run e2e`")
		return ""
	}
	RequireCommand(t, path)
	return path
}

func unmet(t testing.TB, reason string) {
	t.Helper()
	if os.Getenv(EnvStrict) == "1" {
		t.Fatalf("environment requirement not met in strict mode: %s", reason)
	}
	t.Skip(reason)
}
