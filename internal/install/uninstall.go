package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/underlay"
)

// Uninstaller removes the daemon. All fields are necessary.
type Uninstaller struct {
	FS      FS
	Launchd Launchd
	// Fetch reads the routing table.
	Fetch func() ([]underlay.Route, error)
	// Apply applies one route action, the same as the daemon.
	Apply func(context.Context, underlay.Action) error
	// EUID is the effective uid of the process.
	EUID int
}

// Uninstall boots the job out when launchd has it, removes the files that
// Install wrote (writtenFiles), and deletes each owned route (RTF_PROTO1).
// The route deletes come after the bootout, so that the launchd job cannot
// add a route again; a daemon that an operator started by hand can.
// Uninstall keeps the routes without RTF_PROTO1, HelperDir, which other
// programs also use, and the env file of the Tailscale system app. It
// writes one line to w for each removal, or "nothing to remove", and a note
// when the env file still holds the setting that Install added.
//
// Uninstall makes HelperDir when it is missing, and holds the lock at
// LockPath, the same as Install, so that the two run one after the other.
// It removes the lock file last, while it holds the lock.
//
// When a user other than root can change HelperDir or NewsyslogDir,
// Uninstall still stops the job, removes the files in the other
// directories, and deletes the owned routes: the job runs a binary that
// such a user can replace. It does not touch a file in an unsafe
// directory, and then returns ErrUnsafeDir. It does not take the lock when
// HelperDir is unsafe.
//
// Errors: ErrNotRoot without root; ErrUnsafeDir when a user other than root
// can change a directory on the path to an installed file (for DaemonDir,
// which holds the plist, before any change); ErrLaunchctl when launchctl
// fails. After an error, a repeat run removes what is left.
func (u Uninstaller) Uninstall(ctx context.Context, w io.Writer) (err error) {
	if u.EUID != 0 {
		return ErrNotRoot
	}
	if err := checkDirs(u.FS, DaemonDir); err != nil {
		return err
	}
	unsafe := map[string]bool{}
	var unsafeErr error
	for _, d := range fixedDirs() {
		if d == DaemonDir {
			continue
		}
		switch err := checkDirs(u.FS, d); {
		case errors.Is(err, ErrUnsafeDir):
			unsafe[d], unsafeErr = true, errors.Join(unsafeErr, err)
		case err != nil:
			return err
		}
	}
	helperErr := prepareHelperDir(u.FS)
	switch {
	case errors.Is(helperErr, ErrUnsafeDir):
		unsafe[HelperDir], unsafeErr = true, errors.Join(unsafeErr, helperErr)
	case helperErr != nil:
		return helperErr
	default:
		lock, lockErr := u.FS.Lock(LockPath)
		if lockErr != nil {
			return fmt.Errorf("locking %s: %w", LockPath, lockErr)
		}
		defer func() { err = errors.Join(err, lock.Close()) }()
	}
	r := reporter{w: w}
	if err := u.remove(ctx, &r, unsafe); err != nil {
		return err
	}
	if unsafe[HelperDir] {
		return unsafeErr
	}
	// The lock file is not a file of the installation, so it gets no line.
	// A waiting OS.Lock sees that its file is gone, and locks a new file.
	if err := u.FS.Remove(LockPath); err != nil {
		return fmt.Errorf("removing %s: %w", LockPath, err)
	}
	if unsafeErr != nil {
		return unsafeErr
	}
	if !r.changed {
		r.printf("nothing to remove")
	}
	return r.err
}

// remove boots the job out, removes the written files, deletes the owned
// routes, and notes the env file of the system app. It skips the files in
// the unsafe directories.
func (u Uninstaller) remove(ctx context.Context, r *reporter, unsafe map[string]bool) error {
	loaded, err := u.Launchd.Loaded(ctx, Service)
	if err != nil {
		return err
	}
	if loaded {
		if err := u.Launchd.Bootout(ctx, Service); err != nil {
			return err
		}
		r.printf("booted out %s", Service)
	}
	for _, name := range writtenFiles() {
		if unsafe[filepath.Dir(name)] {
			continue
		}
		if err := u.removeFile(r, name); err != nil {
			return err
		}
	}
	table, err := u.Fetch()
	if err != nil {
		return err
	}
	for _, rt := range table {
		if !rt.Owned {
			continue
		}
		if err := u.Apply(ctx, underlay.Delete{Route: rt}); err != nil {
			return fmt.Errorf("deleting the owned route %s on %s: %w", rt.Dst, rt.Iface, err)
		}
		r.printf("deleted the owned route %s on %s", rt.Dst, rt.Iface)
	}
	u.noteSystemAppEnv(r)
	return nil
}

// noteSystemAppEnv tells the operator that the env file of the system app
// still holds bindByRoute. Uninstall does not change the file of another
// app.
func (u Uninstaller) noteSystemAppEnv(r *reporter) {
	content, err := u.FS.ReadFile(SystemAppEnvPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		r.note("cannot read %s: %v", SystemAppEnvPath, err)
	default:
		if _, changed := withBindByRoute(content); !changed {
			r.note("kept %s in %s; the system Tailscale app still looks up the route of each dial", bindByRoute, SystemAppEnvPath)
		}
	}
}

// removeFile removes name, and reports it when it existed.
func (u Uninstaller) removeFile(r *reporter, name string) error {
	ok, err := exists(u.FS, name)
	if err != nil || !ok {
		return err
	}
	if err := u.FS.Remove(name); err != nil {
		return fmt.Errorf("removing %s: %w", name, err)
	}
	r.printf("removed %s", name)
	return nil
}
