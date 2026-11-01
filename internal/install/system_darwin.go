//go:build darwin

package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// OS is the file system of the host.
type OS struct{}

// ReadFile implements FS.
func (OS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

// EvalSymlinks implements FS.
func (OS) EvalSymlinks(path string) (string, error) { return filepath.EvalSymlinks(path) }

// Lstat implements FS.
func (OS) Lstat(name string) (FileStat, error) {
	fi, err := os.Lstat(name)
	if err != nil {
		return FileStat{}, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return FileStat{}, fmt.Errorf("%s: %w", name, errors.ErrUnsupported)
	}
	return FileStat{UID: st.Uid, Mode: fi.Mode()}, nil
}

// Remove implements FS.
func (OS) Remove(name string) error {
	if err := os.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// WriteFile implements FS. It writes a temporary file in the directory of
// name and renames it to name. The temporary file has a fixed name, so the
// next write replaces a file that a crash left. Install writes only in
// directories that only root can change, so no other user can put a file at
// that name.
func (OS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	tmp := filepath.Join(filepath.Dir(name), "."+filepath.Base(name)+".tmp")
	if err := writeTemp(tmp, data, perm); err != nil {
		return errors.Join(err, OS{}.Remove(tmp))
	}
	if err := os.Rename(tmp, name); err != nil {
		return errors.Join(err, os.Remove(tmp))
	}
	return nil
}

func writeTemp(tmp string, data []byte, perm fs.FileMode) (err error) {
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Chown(0, 0); err != nil {
		return err
	}
	if err := f.Chmod(perm); err != nil {
		return err
	}
	return f.Sync()
}

const (
	launchctlPath = "/bin/launchctl"
	// bootoutWait is the longest wait for launchd to remove a job after
	// bootout.
	bootoutWait = 10 * time.Second
	pollEvery   = 100 * time.Millisecond
)

// Launchctl controls launchd with /bin/launchctl.
type Launchctl struct{}

// Loaded implements Launchd. launchctl print exits non-zero when the
// domain has no job of service.
func (Launchctl) Loaded(ctx context.Context, service string) (bool, error) {
	err := exec.CommandContext(ctx, launchctlPath, "print", service).Run()
	if err == nil {
		return true, nil
	}
	if _, ok := errors.AsType[*exec.ExitError](err); ok {
		return false, nil
	}
	return false, fmt.Errorf("%w: print %s: %v", ErrLaunchctl, service, err)
}

// Bootstrap implements Launchd.
func (Launchctl) Bootstrap(ctx context.Context, plist string) error {
	return launchctl(ctx, "bootstrap", "system", plist)
}

// Kickstart implements Launchd.
func (Launchctl) Kickstart(ctx context.Context, service string) error {
	return launchctl(ctx, "kickstart", "-k", service)
}

// Bootout implements Launchd. bootout can return before launchd removed the
// job (status 36, "Operation now in progress"), and a bootstrap then fails.
// So Bootout waits until the job is gone, and the job state decides the
// result, not the status of bootout.
func (l Launchctl) Bootout(ctx context.Context, service string) error {
	bootErr := launchctl(ctx, "bootout", service)
	ctx, cancel := context.WithTimeout(ctx, bootoutWait)
	defer cancel()
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	for {
		loaded, err := l.Loaded(ctx, service)
		if err == nil && !loaded {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %s is still loaded after bootout: %v", ErrLaunchctl, service, errors.Join(bootErr, err))
		case <-tick.C:
		}
	}
}

func launchctl(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, launchctlPath, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s: %v: %s", ErrLaunchctl, strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return nil
}
