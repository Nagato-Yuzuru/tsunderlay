// Package install installs the daemon as a launchd job. The job runs a
// root-owned copy of the binary, never the Homebrew binary: the Homebrew
// prefix is writable by the user, so a root job that runs it gives root to
// any process of the user at the next restart.
//
// Install compares each file with the state on disk and changes only the
// difference, so a repeat run with the same binary and flags changes
// nothing. The file system and launchctl are dependencies of Installer, so
// that tests run without root and without launchd.
package install

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
)

// The names and paths of the installed daemon. Each command that finds or
// removes the installation uses these.
const (
	// Label is the launchd label of the job.
	Label = "io.github.nagato-yuzuru.tsunderlay"
	// Service is the launchctl service target of the job.
	Service = "system/" + Label
	// BinaryPath is the root-owned copy of the binary that the job runs.
	BinaryPath = "/Library/PrivilegedHelperTools/" + Label
	// PlistPath is the job definition.
	PlistPath = "/Library/LaunchDaemons/" + Label + ".plist"
	// PendingPath marks a change of the files that launchd possibly did not
	// apply yet. Install writes it before it changes a file and removes it
	// after launchctl applied the change. When the mark is present, the next
	// install reloads the job, also when the files are already correct.
	PendingPath = BinaryPath + ".pending"
	// LogPath receives the standard error of the daemon.
	LogPath = "/var/log/tsunderlay.log"
)

const (
	binaryPerm fs.FileMode = 0o755
	plistPerm  fs.FileMode = 0o644
	// unsafeBits are the bits that let a user other than root change a
	// directory.
	unsafeBits fs.FileMode = 0o022
)

var (
	// ErrNotRoot means that the process does not run with effective uid 0.
	ErrNotRoot = errors.New("install needs root; run it with sudo")
	// ErrUnsafeDir means that a user other than root can change a directory
	// on the path to an installed file.
	ErrUnsafeDir = errors.New("a user other than root can change the directory")
	// ErrLaunchctl means that launchctl reported a failure.
	ErrLaunchctl = errors.New("launchctl failed")
)

// FileStat is the owner and the mode of a file.
type FileStat struct {
	UID  uint32
	Mode fs.FileMode
}

// FS is the file system that Install reads and changes.
type FS interface {
	// ReadFile returns the content of a file. The error wraps
	// fs.ErrNotExist when the file does not exist.
	ReadFile(name string) ([]byte, error)
	// WriteFile replaces name with data, owned by root:wheel with mode perm.
	// A reader sees the old or the new content, never a part.
	WriteFile(name string, data []byte, perm fs.FileMode) error
	// Remove removes a file. A missing file is not an error.
	Remove(name string) error
	// EvalSymlinks returns the path without symbolic links.
	EvalSymlinks(path string) (string, error)
	// Lstat returns the owner and the mode of a file. It does not follow a
	// symbolic link.
	Lstat(name string) (FileStat, error)
}

// Launchd controls the jobs of the system domain.
type Launchd interface {
	// Loaded tells whether launchd has the job of service.
	Loaded(ctx context.Context, service string) (bool, error)
	// Bootstrap loads the job of a plist into the system domain.
	Bootstrap(ctx context.Context, plist string) error
	// Bootout removes the job of service and returns after launchd no
	// longer has it.
	Bootout(ctx context.Context, service string) error
	// Kickstart stops the job of service and starts it again.
	Kickstart(ctx context.Context, service string) error
}

// Installer installs the daemon. All fields are necessary.
type Installer struct {
	FS      FS
	Launchd Launchd
	// EUID is the effective uid of the process.
	EUID int
}

// Install copies the binary at source to BinaryPath, writes the plist at
// PlistPath, and makes launchd run the job. Each argument in controlURLs
// becomes one --control-url argument of the job. Install writes one line to
// w for each change, or "no change".
//
// Errors: ErrNotRoot without root; ErrUnsafeDir when a user other than root
// can change a directory on the path to an installed file, before Install
// writes a file; ErrLaunchctl when launchctl fails.
func (in Installer) Install(ctx context.Context, w io.Writer, source string, controlURLs []string) error {
	if in.EUID != 0 {
		return ErrNotRoot
	}
	for _, p := range []string{BinaryPath, PlistPath} {
		if err := in.checkDirs(filepath.Dir(p)); err != nil {
			return err
		}
	}

	st, err := in.state(ctx, source, controlURLs)
	if err != nil {
		return err
	}
	r := reporter{w: w}
	if err := in.writeFiles(&r, source, st); err != nil {
		return err
	}
	if err := in.apply(ctx, &r, st.loaded, st.binChanged, st.plistChanged || st.pending); err != nil {
		return err
	}
	if st.binChanged || st.plistChanged || st.pending {
		if err := in.FS.Remove(PendingPath); err != nil {
			return fmt.Errorf("removing the change mark: %w", err)
		}
	}
	if !r.changed {
		r.printf("no change")
	}
	return r.err
}

// state is the installation on disk and in launchd, compared with the
// wanted one.
type state struct {
	bin, plist   []byte
	binChanged   bool
	plistChanged bool
	pending      bool
	loaded       bool
}

func (in Installer) state(ctx context.Context, source string, controlURLs []string) (state, error) {
	st := state{plist: Plist(controlURLs)}
	var err error
	if st.bin, err = in.FS.ReadFile(source); err != nil {
		return st, fmt.Errorf("reading the binary: %w", err)
	}
	if st.binChanged, err = in.differs(BinaryPath, st.bin); err != nil {
		return st, err
	}
	if st.plistChanged, err = in.differs(PlistPath, st.plist); err != nil {
		return st, err
	}
	if st.pending, err = in.exists(PendingPath); err != nil {
		return st, err
	}
	st.loaded, err = in.Launchd.Loaded(ctx, Service)
	return st, err
}

// writeFiles writes the files that differ. Before the first write, it
// writes the mark of a pending change.
func (in Installer) writeFiles(r *reporter, source string, st state) error {
	if (st.binChanged || st.plistChanged) && !st.pending {
		if err := in.FS.WriteFile(PendingPath, nil, plistPerm); err != nil {
			return fmt.Errorf("marking the change: %w", err)
		}
	}
	if st.binChanged {
		if err := in.FS.WriteFile(BinaryPath, st.bin, binaryPerm); err != nil {
			return fmt.Errorf("copying the binary: %w", err)
		}
		r.printf("copied %s to %s", source, BinaryPath)
	}
	if st.plistChanged {
		if err := in.FS.WriteFile(PlistPath, st.plist, plistPerm); err != nil {
			return fmt.Errorf("writing the plist: %w", err)
		}
		r.printf("wrote %s", PlistPath)
	}
	return nil
}

// apply makes launchd run the files on disk. launchd reads the plist only
// when it loads the job, so a new plist needs a bootout and a bootstrap. A
// kickstart is sufficient for a new binary.
func (in Installer) apply(ctx context.Context, r *reporter, loaded, binChanged, reload bool) error {
	switch {
	case !loaded:
		if err := in.Launchd.Bootstrap(ctx, PlistPath); err != nil {
			return err
		}
		r.printf("bootstrapped %s", Service)
	case reload:
		if err := in.Launchd.Bootout(ctx, Service); err != nil {
			return err
		}
		if err := in.Launchd.Bootstrap(ctx, PlistPath); err != nil {
			return err
		}
		r.printf("reloaded %s", Service)
	case binChanged:
		if err := in.Launchd.Kickstart(ctx, Service); err != nil {
			return err
		}
		r.printf("restarted %s", Service)
	}
	return nil
}

// checkDirs returns ErrUnsafeDir when a directory from / to dir is not
// owned by root or is writable by group or other. It checks the real path
// of dir, because a symbolic link can point anywhere.
func (in Installer) checkDirs(dir string) error {
	real, err := in.FS.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("resolving %s: %w", dir, err)
	}
	var chain []string
	for d := real; ; d = filepath.Dir(d) {
		chain = append(chain, d)
		if d == filepath.Dir(d) {
			break
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		d := chain[i]
		st, err := in.FS.Lstat(d)
		if err != nil {
			return fmt.Errorf("checking %s: %w", d, err)
		}
		if !st.Mode.IsDir() || st.UID != 0 || st.Mode.Perm()&unsafeBits != 0 {
			return fmt.Errorf("%w: %s (owner uid %d, mode %v); the installed daemon runs as root, so install refuses it",
				ErrUnsafeDir, d, st.UID, st.Mode)
		}
	}
	return nil
}

// differs tells whether the file name does not have the content want.
func (in Installer) differs(name string, want []byte) (bool, error) {
	got, err := in.FS.ReadFile(name)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", name, err)
	}
	return !bytes.Equal(got, want), nil
}

func (in Installer) exists(name string) (bool, error) {
	_, err := in.FS.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking %s: %w", name, err)
	}
	return true, nil
}

// reporter writes one line for each change. It keeps the first write
// error, so that a closed output does not stop the install halfway.
type reporter struct {
	w       io.Writer
	changed bool
	err     error
}

func (r *reporter) printf(format string, args ...any) {
	r.changed = true
	if _, err := fmt.Fprintf(r.w, format+"\n", args...); err != nil && r.err == nil {
		r.err = fmt.Errorf("writing the report: %w", err)
	}
}

// Plist returns the job definition. The output depends only on
// controlURLs, so that Install can compare it with the file byte for byte.
func Plist(controlURLs []string) []byte {
	args := []string{BinaryPath, "run"}
	for _, u := range controlURLs {
		args = append(args, "--control-url", u)
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + escape(Label) + `</string>
	<key>ProgramArguments</key>
	<array>
`)
	for _, a := range args {
		b.WriteString("\t\t<string>" + escape(a) + "</string>\n")
	}
	b.WriteString(`	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardErrorPath</key>
	<string>` + escape(LogPath) + `</string>
</dict>
</plist>
`)
	return []byte(b.String())
}

func escape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s)) //nolint:errcheck // a strings.Builder never returns a write error
	return b.String()
}
