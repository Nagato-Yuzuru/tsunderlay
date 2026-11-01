// Package install installs the daemon as a launchd job, and uninstalls it.
// The job runs a root-owned copy of the binary, never the Homebrew binary:
// the Homebrew prefix is writable by the user, so a root job that runs it
// gives root to any process of the user at the next restart.
//
// Install compares each file with the state on disk and changes only the
// difference, so a repeat run with the same binary and flags changes
// nothing. Uninstall removes only what exists, so a repeat run removes
// nothing. The file system, launchctl, and the routing table are
// dependencies of Installer and Uninstaller, so that tests run without root,
// without launchd, and without a change to the routes of the host.
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
	// HelperDir holds the binary, the lock, and the change mark. A clean
	// macOS does not have it, so Install makes it.
	HelperDir = "/Library/PrivilegedHelperTools"
	// DaemonDir holds the plist. Each macOS has it.
	DaemonDir = "/Library/LaunchDaemons"
	// BinaryPath is the root-owned copy of the binary that the job runs.
	BinaryPath = HelperDir + "/" + Label
	// PlistPath is the job definition.
	PlistPath = DaemonDir + "/" + Label + ".plist"
	// PendingPath marks a change of the files that launchd possibly did not
	// apply yet. Install writes it before it changes a file and removes it
	// after launchctl applied the change. When the mark is present, the next
	// install reloads the job, also when the files are already correct.
	PendingPath = BinaryPath + ".pending"
	// LockPath is the lock file of Install and Uninstall. Two of them at the
	// same time run one after the other.
	LockPath = BinaryPath + ".lock"
	// LogPath receives the standard error of the daemon.
	LogPath = "/var/log/tsunderlay.log"
	// NewsyslogDir holds the rotation rules of newsyslog. It is the real
	// path of /etc/newsyslog.d: /etc is a symbolic link, and checkDirs
	// refuses a link. Each macOS has it.
	NewsyslogDir = "/private/etc/newsyslog.d"
	// RotationPath is the newsyslog rule that rotates LogPath.
	RotationPath = NewsyslogDir + "/" + Label + ".conf"
)

// rotationRule makes newsyslog rotate LogPath. newsyslog runs each hour at
// minute 30 (com.apple.newsyslog) and rotates the file when it is 1024 KB
// or more. One event is a JSON line of some hundred bytes, and the daemon
// writes at most about one each second, also with a VPN that flaps. Thus
// the file grows by at most about 2 MB between two runs, and a quiet
// daemon fills it in months. 5 gzip archives (Z) keep at least some hours
// of a flapping VPN, in less disk than the live file. The size and the
// count are the same as in the rules of macOS in /etc/newsyslog.conf.
//
// B: newsyslog adds no text line, so each line stays JSON. N: newsyslog
// sends no signal. launchd opens LogPath only when it starts the job, so
// the daemon itself opens the new file after a rotation (see followLog in
// package main).
const rotationRule = "# Written by tsunderlay install. Removed by tsunderlay uninstall.\n" +
	LogPath + "\troot:wheel\t644\t5\t1024\t*\tBNZ\n"

const (
	binaryPerm fs.FileMode = 0o755
	plistPerm  fs.FileMode = 0o644
	// helperDirMode is the mode that macOS gives HelperDir.
	helperDirMode = fs.ModeSticky | 0o755
	// unsafeBits are the bits that let a user other than root change a
	// directory.
	unsafeBits fs.FileMode = 0o022
)

var (
	// ErrNotRoot means that the process does not run with effective uid 0.
	ErrNotRoot = errors.New("this command needs root; run it with sudo")
	// ErrUnsafeDir means that a user other than root can change a directory
	// on the path to an installed file, or that the path has a symbolic link.
	ErrUnsafeDir = errors.New("a user other than root can change the directory")
	// ErrLaunchctl means that launchctl reported a failure.
	ErrLaunchctl = errors.New("launchctl failed")
)

// tempPath is the temporary file that OS.WriteFile writes before it
// renames it to name. A crash during the write leaves it.
func tempPath(name string) string {
	return filepath.Join(filepath.Dir(name), "."+filepath.Base(name)+".tmp")
}

// writtenFiles returns each file that Install writes, and the temporary
// file of each write, with the plist first. The environment file of the
// system app is not in the list: it belongs to the system app, and only its
// temporary file is a file of tsunderlay.
func writtenFiles() []string {
	return []string{
		PlistPath, tempPath(PlistPath),
		RotationPath, tempPath(RotationPath),
		BinaryPath, tempPath(BinaryPath),
		PendingPath, tempPath(PendingPath),
		tempPath(SystemAppEnvPath),
	}
}

// FileStat is the type, the owner, and the mode of a file.
type FileStat struct {
	UID  uint32
	GID  uint32
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
	// Lstat returns the type, the owner, and the mode of a file. It does
	// not follow a symbolic link. The error wraps fs.ErrNotExist when the
	// file does not exist.
	Lstat(name string) (FileStat, error)
	// Mkdir makes the directory name, owned by root:wheel with mode perm.
	// The error wraps fs.ErrExist when name exists.
	Mkdir(name string, perm fs.FileMode) error
	// Lock takes an exclusive lock on the file name. It waits while another
	// process has the lock. Close releases the lock.
	Lock(name string) (io.Closer, error)
}

// Launchd controls the jobs of the system domain.
type Launchd interface {
	// Loaded tells whether launchd has the job of service.
	Loaded(ctx context.Context, service string) (bool, error)
	// Enable removes a disabled override of service.
	Enable(ctx context.Context, service string) error
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
// PlistPath and the rotation rule of LogPath at RotationPath, and makes
// launchd run the job. Each argument in controlURLs becomes one
// --control-url argument of the job. Install writes one line to w for each
// change, or "no change".
//
// Install also makes sure that the environment file of the system Tailscale
// app holds TS_BIND_TO_INTERFACE_BY_ROUTE=true. After a change of that
// file, it prints the command that restarts the network extension. It does
// not run the command: the restart drops the system node for some seconds.
// Install skips the file when the system app has no container.
//
// Install makes HelperDir when it is missing. It holds the lock at LockPath
// from the first read of the installation to the end.
//
// Errors: ErrNotRoot without root; ErrUnsafeDir when a user other than root
// can change a directory on the path to an installed file, before Install
// reads or writes a file of the installation; ErrLaunchctl when launchctl
// fails; ErrEnvNotRegular when the environment file of the system app is
// not a regular file.
func (in Installer) Install(ctx context.Context, w io.Writer, source string, controlURLs []string) (err error) {
	if in.EUID != 0 {
		return ErrNotRoot
	}
	if err := in.prepareDirs(); err != nil {
		return err
	}
	lock, err := in.FS.Lock(LockPath)
	if err != nil {
		return fmt.Errorf("locking %s: %w", LockPath, err)
	}
	defer func() { err = errors.Join(err, lock.Close()) }()

	st, err := in.state(ctx, source, controlURLs)
	if err != nil {
		return err
	}
	r := reporter{w: w}
	if err := in.writeFiles(&r, source, st); err != nil {
		return err
	}
	if err := in.apply(ctx, &r, st.action); err != nil {
		return err
	}
	if st.dirty() {
		if err := in.FS.Remove(PendingPath); err != nil {
			return fmt.Errorf("removing the change mark: %w", err)
		}
	}
	if err := in.setSystemAppEnv(&r); err != nil {
		return err
	}
	if !r.changed {
		r.printf("no change")
	}
	return r.err
}

// Match tells how the installed binary compares with a wanted one.
type Match int

const (
	// MatchUnspecified is the zero value. Installed never returns it.
	MatchUnspecified Match = iota
	// NotInstalled means that the installed binary does not exist.
	NotInstalled
	// Same means that the installed binary has the wanted content.
	Same
	// Different means that the installed binary has other content.
	Different
)

// Installed compares the installed binary at BinaryPath with want. It
// reads and does not change anything, so any user can run it. Only the
// content counts: owner and mode are the business of Install.
//
// Errors: ErrUnsafeDir when a user other than root can change a directory
// on the path to the binary, because then the content says nothing about
// what the job runs; any other error when the binary exists and cannot be
// read. A missing binary or a missing HelperDir is NotInstalled, not an
// error.
func (in Installer) Installed(want []byte) (Match, error) {
	err := checkDirs(in.FS, HelperDir)
	if errors.Is(err, fs.ErrNotExist) {
		return NotInstalled, nil
	}
	if err != nil {
		return MatchUnspecified, err
	}
	got, err := in.FS.ReadFile(BinaryPath)
	if errors.Is(err, fs.ErrNotExist) {
		return NotInstalled, nil
	}
	if err != nil {
		return MatchUnspecified, fmt.Errorf("reading %s: %w", BinaryPath, err)
	}
	if bytes.Equal(got, want) {
		return Same, nil
	}
	return Different, nil
}

// action is the launchctl change that makes launchd run the files on disk.
type action int

const (
	actNone action = iota
	// actBootstrap loads a job that launchd does not have.
	actBootstrap
	// actReload replaces the job definition. launchd reads the plist only
	// when it loads the job, so a new plist needs a bootout and a bootstrap.
	actReload
	// actKickstart restarts the job, so that it runs the new binary.
	actKickstart
)

// decide returns the action for the state of launchd and of the files. A
// pending mark means that an earlier install changed a file and possibly
// did not apply it, so the action is a reload.
func decide(loaded, binChanged, plistChanged, pending bool) action {
	switch {
	case !loaded:
		return actBootstrap
	case plistChanged || pending:
		return actReload
	case binChanged:
		return actKickstart
	default:
		return actNone
	}
}

// state is the installation on disk and in launchd, compared with the
// wanted one.
type state struct {
	bin, plist   []byte
	binChanged   bool
	plistChanged bool
	// ruleChanged needs no launchd action: newsyslog reads the rules at
	// each run.
	ruleChanged bool
	pending     bool
	action      action
}

// dirty tells whether a file changes now or changed in an earlier install
// that did not complete.
func (st state) dirty() bool { return st.binChanged || st.plistChanged || st.pending }

func (in Installer) state(ctx context.Context, source string, controlURLs []string) (state, error) {
	st := state{plist: Plist(controlURLs)}
	var err error
	if st.bin, err = in.FS.ReadFile(source); err != nil {
		return st, fmt.Errorf("reading the binary: %w", err)
	}
	if st.binChanged, err = in.differs(BinaryPath, st.bin, binaryPerm); err != nil {
		return st, err
	}
	if st.plistChanged, err = in.differs(PlistPath, st.plist, plistPerm); err != nil {
		return st, err
	}
	if st.ruleChanged, err = in.differs(RotationPath, []byte(rotationRule), plistPerm); err != nil {
		return st, err
	}
	if st.pending, err = exists(in.FS, PendingPath); err != nil {
		return st, err
	}
	loaded, err := in.Launchd.Loaded(ctx, Service)
	if err != nil {
		return st, err
	}
	st.action = decide(loaded, st.binChanged, st.plistChanged, st.pending)
	return st, nil
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
	if st.ruleChanged {
		if err := in.FS.WriteFile(RotationPath, []byte(rotationRule), plistPerm); err != nil {
			return fmt.Errorf("writing the rotation rule: %w", err)
		}
		r.printf("wrote %s", RotationPath)
	}
	return nil
}

func (in Installer) apply(ctx context.Context, r *reporter, a action) error {
	switch a {
	case actNone:
		return nil
	case actBootstrap:
		if err := in.bootstrap(ctx); err != nil {
			return err
		}
		r.printf("bootstrapped %s", Service)
	case actReload:
		if err := in.Launchd.Bootout(ctx, Service); err != nil {
			return err
		}
		if err := in.bootstrap(ctx); err != nil {
			return err
		}
		r.printf("reloaded %s", Service)
	case actKickstart:
		if err := in.Launchd.Kickstart(ctx, Service); err != nil {
			return err
		}
		r.printf("restarted %s", Service)
	default:
		panic(fmt.Sprintf("install: unknown action %d", a))
	}
	return nil
}

// bootstrap enables the service before the bootstrap. launchd refuses to
// bootstrap a service that has a disabled override, for example from an
// earlier "launchctl disable".
func (in Installer) bootstrap(ctx context.Context) error {
	if err := in.Launchd.Enable(ctx, Service); err != nil {
		return err
	}
	return in.Launchd.Bootstrap(ctx, PlistPath)
}

// prepareDirs checks the directories of the installed files, and makes
// HelperDir when it is missing.
func (in Installer) prepareDirs() error {
	if err := checkFixedDirs(in.FS); err != nil {
		return err
	}
	return prepareHelperDir(in.FS)
}

// checkFixedDirs checks the directories of the installed files that each
// macOS has: DaemonDir and NewsyslogDir.
func checkFixedDirs(fsys FS) error {
	for _, d := range []string{DaemonDir, NewsyslogDir} {
		if err := checkDirs(fsys, d); err != nil {
			return err
		}
	}
	return nil
}

// prepareHelperDir checks HelperDir, and makes it when it is missing. Only
// root can add an entry to a safe /Library, so no other process can make
// HelperDir first.
func prepareHelperDir(fsys FS) error {
	err := checkDirs(fsys, HelperDir)
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// checkDirs goes from / down and stops at the first missing directory,
	// so HelperDir is the missing one when its parent exists.
	if err := fsys.Mkdir(HelperDir, helperDirMode); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("making %s: %w", HelperDir, err)
	}
	return checkDirs(fsys, HelperDir)
}

// checkDirs returns ErrUnsafeDir when a directory from / to dir is not a
// directory owned by root, or is writable by group or other. Lstat does not
// follow a symbolic link, so a link on the path also fails: launchd uses
// the path as it is, and the owner of the parent of a link can point the
// link to another directory later.
//
// The check reads only the mode bits, not an ACL. Only root can add an ACL
// to a root-owned directory, a stock macOS has none on these directories,
// and the check guards against a writer that is not root.
func checkDirs(fsys FS, dir string) error {
	var chain []string
	for d := dir; ; d = filepath.Dir(d) {
		chain = append(chain, d)
		if d == filepath.Dir(d) {
			break
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		d := chain[i]
		st, err := fsys.Lstat(d)
		if err != nil {
			return fmt.Errorf("checking %s: %w", d, err)
		}
		if !st.Mode.IsDir() || st.UID != 0 || st.Mode.Perm()&unsafeBits != 0 {
			return fmt.Errorf("%w: %s (owner uid %d, mode %v); the installed daemon runs as root, so tsunderlay refuses it",
				ErrUnsafeDir, d, st.UID, st.Mode)
		}
	}
	return nil
}

// differs tells whether the file name is not a regular file of root:wheel
// with mode perm and the content want. launchd skips a plist that others
// can write, and a user who can change the binary gets root.
func (in Installer) differs(name string, want []byte, perm fs.FileMode) (bool, error) {
	st, err := in.FS.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking %s: %w", name, err)
	}
	if st.Mode != perm || st.UID != 0 || st.GID != 0 {
		return true, nil
	}
	got, err := in.FS.ReadFile(name)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", name, err)
	}
	return !bytes.Equal(got, want), nil
}

func exists(fsys FS, name string) (bool, error) {
	_, err := fsys.Lstat(name)
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
	r.note(format, args...)
}

// note writes a line that is not a change.
func (r *reporter) note(format string, args ...any) {
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
