package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/google/go-cmp/cmp"
)

const source = "/opt/homebrew/Cellar/tsunderlay/1.0/bin/tsunderlay"

var (
	rootDir   = FileStat{Mode: fs.ModeDir | 0o755}
	helperDir = FileStat{Mode: fs.ModeDir | fs.ModeSticky | 0o755}
)

type file struct {
	data []byte
	st   FileStat
}

// fakeFS is a file system with root-owned directories. It records each
// call that reads or changes the installation in events, except Lstat.
type fakeFS struct {
	files  map[string]file
	dirs   map[string]FileStat
	events *[]string
	// failWrite makes WriteFile of this path fail.
	failWrite string
	// failRead makes ReadFile of this path fail.
	failRead string
	// lock is the lock of LockPath: a full channel is a held lock.
	lock chan struct{}
}

func (f *fakeFS) record(format string, args ...any) {
	*f.events = append(*f.events, fmt.Sprintf(format, args...))
}

func (f *fakeFS) ReadFile(name string) ([]byte, error) {
	f.record("read %s", name)
	if name == f.failRead {
		return nil, fmt.Errorf("open %s: %w", name, fs.ErrPermission)
	}
	b, ok := f.files[name]
	if !ok {
		return nil, fmt.Errorf("open %s: %w", name, fs.ErrNotExist)
	}
	return b.data, nil
}

func (f *fakeFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	if name == f.failWrite {
		return fmt.Errorf("write %s: %w", name, fs.ErrPermission)
	}
	f.record("write %s", name)
	f.files[name] = file{data: slices.Clone(data), st: FileStat{Mode: perm}}
	return nil
}

func (f *fakeFS) Remove(name string) error {
	if _, ok := f.files[name]; ok {
		f.record("remove %s", name)
	}
	delete(f.files, name)
	return nil
}

func (f *fakeFS) Lstat(name string) (FileStat, error) {
	if st, ok := f.dirs[name]; ok {
		return st, nil
	}
	if fl, ok := f.files[name]; ok {
		return fl.st, nil
	}
	return FileStat{}, fmt.Errorf("lstat %s: %w", name, fs.ErrNotExist)
}

func (f *fakeFS) Mkdir(name string, perm fs.FileMode) error {
	if _, ok := f.dirs[name]; ok {
		return fmt.Errorf("mkdir %s: %w", name, fs.ErrExist)
	}
	f.record("mkdir %s %v", name, perm)
	f.dirs[name] = FileStat{Mode: fs.ModeDir | perm}
	return nil
}

func (f *fakeFS) Lock(name string) (io.Closer, error) {
	f.lock <- struct{}{}
	f.record("lock %s", name)
	return unlocker{f}, nil
}

type unlocker struct{ f *fakeFS }

func (u unlocker) Close() error {
	u.f.record("unlock")
	<-u.f.lock
	return nil
}

// fakeLaunchd records each call in events.
type fakeLaunchd struct {
	loaded bool
	events *[]string
	// fail makes the call with this verb fail.
	fail string
}

func (l *fakeLaunchd) call(verb, arg string) error {
	*l.events = append(*l.events, verb+" "+arg)
	if verb == l.fail {
		return fmt.Errorf("%w: %s", ErrLaunchctl, verb)
	}
	return nil
}

func (l *fakeLaunchd) Loaded(_ context.Context, service string) (bool, error) {
	return l.loaded, l.call("print", service)
}

func (l *fakeLaunchd) Enable(_ context.Context, service string) error {
	return l.call("enable", service)
}

func (l *fakeLaunchd) Bootstrap(_ context.Context, plist string) error {
	if err := l.call("bootstrap", plist); err != nil {
		return err
	}
	l.loaded = true
	return nil
}

func (l *fakeLaunchd) Bootout(_ context.Context, service string) error {
	if err := l.call("bootout", service); err != nil {
		return err
	}
	l.loaded = false
	return nil
}

func (l *fakeLaunchd) Kickstart(_ context.Context, service string) error {
	return l.call("kickstart", service)
}

type host struct {
	fs     *fakeFS
	ld     *fakeLaunchd
	events []string
}

func newHost() *host {
	h := &host{}
	h.fs = &fakeFS{
		files: map[string]file{source: {data: []byte("binary v1"), st: FileStat{UID: 501, GID: 20, Mode: 0o755}}},
		dirs: map[string]FileStat{
			"/":        rootDir,
			"/Library": rootDir,
			DaemonDir:  rootDir,
			HelperDir:  helperDir,
			// The system app is installed, and its file is right already.
			SystemAppContainer: rootDir,
		},
		events: &h.events,
		lock:   make(chan struct{}, 1),
	}
	h.fs.files[SystemAppEnvPath] = file{data: []byte(bindByRoute + "\n"), st: FileStat{Mode: 0o644}}
	h.ld = &fakeLaunchd{events: &h.events}
	return h
}

// install runs Install as root and returns its output. It forgets the
// events of earlier runs.
func (h *host) install(t *testing.T, urls ...string) (string, error) {
	t.Helper()
	h.events = nil
	var out bytes.Buffer
	err := Installer{FS: h.fs, Launchd: h.ld, EUID: 0}.Install(t.Context(), &out, source, urls)
	return out.String(), err
}

func (h *host) mustInstall(t *testing.T, urls ...string) string {
	t.Helper()
	out, err := h.install(t, urls...)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	return out
}

// changes returns the events that change the file system or launchd.
func (h *host) changes() []string {
	var c []string
	for _, e := range h.events {
		verb, _, _ := strings.Cut(e, " ")
		switch verb {
		case "write", "remove", "mkdir", "enable", "bootstrap", "bootout", "kickstart":
			c = append(c, e)
		}
	}
	return c
}

func (h *host) launchctl() []string {
	var c []string
	for _, e := range h.changes() {
		verb, _, _ := strings.Cut(e, " ")
		switch verb {
		case "enable", "bootstrap", "bootout", "kickstart":
			c = append(c, e)
		}
	}
	return c
}

func lines(s ...string) string { return strings.Join(s, "\n") + "\n" }

var (
	bootstrapCalls = []string{"enable " + Service, "bootstrap " + PlistPath}
	reloadCalls    = []string{"bootout " + Service, "enable " + Service, "bootstrap " + PlistPath}
)

func TestFirstInstallCopiesWritesAndBootstraps(t *testing.T) {
	h := newHost()
	out := h.mustInstall(t)

	want := lines(
		"copied "+source+" to "+BinaryPath,
		"wrote "+PlistPath,
		"bootstrapped "+Service,
	)
	if out != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
	bin := h.fs.files[BinaryPath]
	if string(bin.data) != "binary v1" || bin.st != (FileStat{Mode: 0o755}) {
		t.Errorf("installed binary = %q %+v, want the source content, root:wheel 0755", bin.data, bin.st)
	}
	plist := h.fs.files[PlistPath]
	if string(plist.data) != string(Plist(nil)) || plist.st != (FileStat{Mode: 0o644}) {
		t.Errorf("plist %+v:\n%s\nwant root:wheel 0644:\n%s", plist.st, plist.data, Plist(nil))
	}
	// The enable comes first: a disabled override makes each bootstrap fail.
	if diff := cmp.Diff(bootstrapCalls, h.launchctl()); diff != "" {
		t.Errorf("launchctl calls (-want +got):\n%s", diff)
	}
	if _, ok := h.fs.files[PendingPath]; ok {
		t.Errorf("%s is left after a complete install", PendingPath)
	}
}

func TestSecondInstallChangesNothing(t *testing.T) {
	h := newHost()
	h.mustInstall(t, "https://hs.example.com")

	out := h.mustInstall(t, "https://hs.example.com")

	if out != "no change\n" {
		t.Errorf("output = %q, want %q", out, "no change\n")
	}
	if c := h.changes(); len(c) != 0 {
		t.Errorf("changes = %q, want none", c)
	}
}

func TestNewBinaryIsCopiedAndRestarted(t *testing.T) {
	h := newHost()
	h.mustInstall(t)
	h.fs.files[source] = file{data: []byte("binary v2"), st: h.fs.files[source].st}

	out := h.mustInstall(t)

	want := lines("copied "+source+" to "+BinaryPath, "restarted "+Service)
	if out != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
	if got := string(h.fs.files[BinaryPath].data); got != "binary v2" {
		t.Errorf("installed binary = %q, want %q", got, "binary v2")
	}
	if diff := cmp.Diff([]string{"kickstart " + Service}, h.launchctl()); diff != "" {
		t.Errorf("launchctl calls (-want +got):\n%s", diff)
	}
}

// launchd reads the plist only when it loads the job, so new flags need a
// bootout and a bootstrap. A kickstart would restart the old arguments.
func TestNewFlagsRewritePlistAndReload(t *testing.T) {
	h := newHost()
	h.mustInstall(t)

	out := h.mustInstall(t, "https://hs.example.com", "https://ts.example.org")

	want := lines("wrote "+PlistPath, "reloaded "+Service)
	if out != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
	plist := string(h.fs.files[PlistPath].data)
	for _, s := range []string{
		"<string>--control-url</string>\n\t\t<string>https://hs.example.com</string>",
		"<string>--control-url</string>\n\t\t<string>https://ts.example.org</string>",
	} {
		if !strings.Contains(plist, s) {
			t.Errorf("plist does not contain %q:\n%s", s, plist)
		}
	}
	if diff := cmp.Diff(reloadCalls, h.launchctl()); diff != "" {
		t.Errorf("launchctl calls (-want +got):\n%s", diff)
	}
}

func TestPlistOnDiskWithoutJobIsBootstrapped(t *testing.T) {
	h := newHost()
	h.mustInstall(t)
	h.ld.loaded = false

	out := h.mustInstall(t)

	if want := lines("bootstrapped " + Service); out != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
	if diff := cmp.Diff(bootstrapCalls, h.changes()); diff != "" {
		t.Errorf("changes (-want +got):\n%s", diff)
	}
}

// A clean macOS has no HelperDir. Install makes it with the mode that
// macOS uses.
func TestMissingHelperDirIsMade(t *testing.T) {
	h := newHost()
	delete(h.fs.dirs, HelperDir)

	out := h.mustInstall(t)

	if got := h.fs.dirs[HelperDir]; got != helperDir {
		t.Errorf("%s = %+v, want root:wheel %v", HelperDir, got, helperDir.Mode)
	}
	if h.events[0] != fmt.Sprintf("mkdir %s %v", HelperDir, fs.ModeSticky|0o755) {
		t.Errorf("first event = %q, want the mkdir of %s", h.events[0], HelperDir)
	}
	if !strings.HasPrefix(out, "copied ") {
		t.Errorf("output = %q, want a first install", out)
	}
}

func TestMissingDirectoryRefuses(t *testing.T) {
	tests := []struct {
		name    string
		change  func(f *fakeFS)
		wantErr error
	}{
		{
			name:    "no LaunchDaemons",
			change:  func(f *fakeFS) { delete(f.dirs, DaemonDir) },
			wantErr: fs.ErrNotExist,
		},
		{
			name: "no HelperDir under a /Library of a user",
			change: func(f *fakeFS) {
				delete(f.dirs, HelperDir)
				f.dirs["/Library"] = FileStat{UID: 501, Mode: fs.ModeDir | 0o755}
			},
			wantErr: ErrUnsafeDir,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHost()
			tt.change(h.fs)

			out, err := h.install(t)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if out != "" || len(h.events) != 0 {
				t.Errorf("output %q, events %q; want none", out, h.events)
			}
		})
	}
}

// A target with the right bytes is not correct when a user other than root
// can change it, or when launchd refuses it.
func TestTargetWithWrongTypeOwnerOrModeIsReplaced(t *testing.T) {
	tests := []struct {
		name string
		path string
		st   FileStat
		want string
	}{
		{name: "binary owned by a user", path: BinaryPath, st: FileStat{UID: 501, Mode: 0o755}},
		{name: "binary of group staff", path: BinaryPath, st: FileStat{GID: 20, Mode: 0o755}},
		{name: "binary writable by all", path: BinaryPath, st: FileStat{Mode: 0o777}},
		{name: "binary is a symbolic link", path: BinaryPath, st: FileStat{Mode: fs.ModeSymlink | 0o755}},
		{name: "plist writable by all", path: PlistPath, st: FileStat{Mode: 0o666}},
		{name: "plist owned by a user", path: PlistPath, st: FileStat{UID: 501, Mode: 0o644}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHost()
			h.mustInstall(t)
			h.fs.files[tt.path] = file{data: h.fs.files[tt.path].data, st: tt.st}

			out, err := h.install(t)
			if err != nil {
				t.Fatal(err)
			}

			if !slices.Contains(h.changes(), "write "+tt.path) {
				t.Errorf("changes %q have no write of %s", h.changes(), tt.path)
			}
			if out == "no change\n" {
				t.Errorf("output = %q, want a change", out)
			}
			if got := h.fs.files[tt.path].st; got.UID != 0 || got.GID != 0 || !got.Mode.IsRegular() {
				t.Errorf("%s after install = %+v, want a regular file of root:wheel", tt.path, got)
			}
		})
	}
}

// A failed restart must not turn into "no change" on the next run: the
// running daemon would keep the old binary.
func TestFailedRestartIsAppliedByTheNextInstall(t *testing.T) {
	h := newHost()
	h.mustInstall(t)
	h.fs.files[source] = file{data: []byte("binary v2"), st: h.fs.files[source].st}
	h.ld.fail = "kickstart"

	if _, err := h.install(t); !errors.Is(err, ErrLaunchctl) {
		t.Fatalf("Install with a failing kickstart: err = %v, want ErrLaunchctl", err)
	}

	h.ld.fail = ""
	out := h.mustInstall(t)
	if want := lines("reloaded " + Service); out != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
	if diff := cmp.Diff(reloadCalls, h.launchctl()); diff != "" {
		t.Errorf("launchctl calls (-want +got):\n%s", diff)
	}
	if out := h.mustInstall(t); out != "no change\n" {
		t.Errorf("third install output = %q, want %q", out, "no change\n")
	}
}

// The mark exists before each file write, so a failure at any write leaves
// it for the next install.
func TestFailedWriteLeavesTheMark(t *testing.T) {
	tests := []struct {
		name      string
		newBinary bool
		urls      []string
		failWrite string
	}{
		{name: "binary write fails", newBinary: true, failWrite: BinaryPath},
		{name: "plist write fails after the binary", newBinary: true, urls: []string{"https://hs.example.com"}, failWrite: PlistPath},
		{name: "plist write fails", urls: []string{"https://hs.example.com"}, failWrite: PlistPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHost()
			h.mustInstall(t)
			if tt.newBinary {
				h.fs.files[source] = file{data: []byte("binary v2"), st: h.fs.files[source].st}
			}
			h.fs.failWrite = tt.failWrite

			if _, err := h.install(t, tt.urls...); !errors.Is(err, fs.ErrPermission) {
				t.Fatalf("err = %v, want the write error", err)
			}
			if _, ok := h.fs.files[PendingPath]; !ok {
				t.Errorf("no %s after a failed write; events %q", PendingPath, h.events)
			}
			if c := h.launchctl(); len(c) != 0 {
				t.Errorf("launchctl calls %q after a failed write, want none", c)
			}
		})
	}
}

func TestUnsafeDirectoryRefusesBeforeAnyAccess(t *testing.T) {
	user := FileStat{UID: 501, Mode: fs.ModeDir | 0o755}
	tests := []struct {
		name    string
		change  func(f *fakeFS)
		wantDir string
	}{
		{
			name:    "a parent owned by a user",
			change:  func(f *fakeFS) { f.dirs["/Library"] = user },
			wantDir: "/Library",
		},
		{
			name:    "the root directory writable by other",
			change:  func(f *fakeFS) { f.dirs["/"] = FileStat{Mode: fs.ModeDir | 0o757} },
			wantDir: "/",
		},
		{
			name:    "the plist directory writable by group",
			change:  func(f *fakeFS) { f.dirs[DaemonDir] = FileStat{Mode: fs.ModeDir | 0o775} },
			wantDir: DaemonDir,
		},
		{
			// The owner of the parent of a link can point it elsewhere
			// later, so a link fails even when it is root-owned.
			name:    "a symbolic link owned by root",
			change:  func(f *fakeFS) { f.dirs[HelperDir] = FileStat{Mode: fs.ModeSymlink | 0o755} },
			wantDir: HelperDir,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHost()
			tt.change(h.fs)

			out, err := h.install(t)

			if !errors.Is(err, ErrUnsafeDir) {
				t.Fatalf("err = %v, want ErrUnsafeDir", err)
			}
			if !strings.Contains(err.Error(), ": "+tt.wantDir+" (") {
				t.Errorf("error %q does not name %s", err, tt.wantDir)
			}
			// No read, no lock, no launchctl print: the check comes first.
			if out != "" || len(h.events) != 0 {
				t.Errorf("output %q, events %q; want none", out, h.events)
			}
		})
	}
}

func TestNotRootRefuses(t *testing.T) {
	h := newHost()
	var out bytes.Buffer

	err := Installer{FS: h.fs, Launchd: h.ld, EUID: 501}.Install(t.Context(), &out, source, nil)

	if !errors.Is(err, ErrNotRoot) {
		t.Fatalf("err = %v, want ErrNotRoot", err)
	}
	if out.Len() != 0 || len(h.events) != 0 {
		t.Errorf("output %q, events %q; want none", out.String(), h.events)
	}
}

// Install takes the lock before it reads the installation and releases it
// after the last change.
func TestInstallHoldsTheLockForAllWork(t *testing.T) {
	h := newHost()
	h.mustInstall(t)

	if h.events[0] != "lock "+LockPath || h.events[len(h.events)-1] != "unlock" {
		t.Errorf("events %q do not start with the lock and end with the unlock", h.events)
	}
	if len(h.fs.lock) != 0 {
		t.Error("the lock is held after Install returned")
	}
}

func TestSecondInstallWaitsForTheLock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHost()
		h.fs.lock <- struct{}{} // another install has the lock
		done := make(chan error)
		var out bytes.Buffer
		go func() {
			done <- Installer{FS: h.fs, Launchd: h.ld, EUID: 0}.Install(t.Context(), &out, source, nil)
		}()

		synctest.Wait()
		if len(h.events) != 0 {
			t.Fatalf("events %q while another install has the lock, want none", h.events)
		}

		<-h.fs.lock // the other install ends
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(out.String(), "bootstrapped "+Service+"\n") {
			t.Errorf("output = %q, want a complete install", out.String())
		}
	})
}

// TestDecide covers each state. Without the job, only a bootstrap loads it.
// A new plist or a pending mark needs a reload. Only a new binary alone
// takes a kickstart.
func TestDecide(t *testing.T) {
	tests := []struct {
		loaded, bin, plist, pending bool
		want                        action
	}{
		{loaded: false, bin: false, plist: false, pending: false, want: actBootstrap},
		{loaded: false, bin: false, plist: false, pending: true, want: actBootstrap},
		{loaded: false, bin: false, plist: true, pending: false, want: actBootstrap},
		{loaded: false, bin: false, plist: true, pending: true, want: actBootstrap},
		{loaded: false, bin: true, plist: false, pending: false, want: actBootstrap},
		{loaded: false, bin: true, plist: false, pending: true, want: actBootstrap},
		{loaded: false, bin: true, plist: true, pending: false, want: actBootstrap},
		{loaded: false, bin: true, plist: true, pending: true, want: actBootstrap},
		{loaded: true, bin: false, plist: false, pending: false, want: actNone},
		{loaded: true, bin: false, plist: false, pending: true, want: actReload},
		{loaded: true, bin: false, plist: true, pending: false, want: actReload},
		{loaded: true, bin: false, plist: true, pending: true, want: actReload},
		{loaded: true, bin: true, plist: false, pending: false, want: actKickstart},
		{loaded: true, bin: true, plist: false, pending: true, want: actReload},
		{loaded: true, bin: true, plist: true, pending: false, want: actReload},
		{loaded: true, bin: true, plist: true, pending: true, want: actReload},
	}
	for _, tt := range tests {
		if got := decide(tt.loaded, tt.bin, tt.plist, tt.pending); got != tt.want {
			t.Errorf("decide(loaded %v, bin %v, plist %v, pending %v) = %d, want %d",
				tt.loaded, tt.bin, tt.plist, tt.pending, got, tt.want)
		}
	}
}

func TestPlistIsStableAndEscapesArguments(t *testing.T) {
	a := Plist([]string{"https://hs.example.com/?a=1&b=<2>"})
	if !bytes.Equal(a, Plist([]string{"https://hs.example.com/?a=1&b=<2>"})) {
		t.Error("Plist gives different bytes for the same arguments")
	}
	if want := "<string>https://hs.example.com/?a=1&amp;b=&lt;2&gt;</string>"; !bytes.Contains(a, []byte(want)) {
		t.Errorf("plist does not contain %q:\n%s", want, a)
	}
}

func TestInstalled(t *testing.T) {
	want := []byte("binary v1")
	tests := []struct {
		name    string
		change  func(f *fakeFS)
		want    Match
		wantErr error
	}{
		{name: "same content", want: Same},
		{
			name:   "other content",
			change: func(f *fakeFS) { f.files[BinaryPath] = file{data: []byte("binary v0")} },
			want:   Different,
		},
		{
			// The content check does not replace the owner and mode check
			// of Install.
			name:   "same content with a loose mode",
			change: func(f *fakeFS) { f.files[BinaryPath] = file{data: want, st: FileStat{UID: 501, Mode: 0o777}} },
			want:   Same,
		},
		{
			name:   "no binary",
			change: func(f *fakeFS) { delete(f.files, BinaryPath) },
			want:   NotInstalled,
		},
		{
			name:   "no helper directory",
			change: func(f *fakeFS) { delete(f.dirs, HelperDir); delete(f.files, BinaryPath) },
			want:   NotInstalled,
		},
		{
			name:    "a directory that a user can write",
			change:  func(f *fakeFS) { f.dirs[HelperDir] = FileStat{Mode: fs.ModeDir | 0o777} },
			wantErr: ErrUnsafeDir,
		},
		{
			name:    "a parent owned by a user",
			change:  func(f *fakeFS) { f.dirs["/Library"] = FileStat{UID: 501, Mode: fs.ModeDir | 0o755} },
			wantErr: ErrUnsafeDir,
		},
		{
			name:    "unreadable binary",
			change:  func(f *fakeFS) { f.failRead = BinaryPath },
			wantErr: fs.ErrPermission,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHost()
			h.mustInstall(t)
			if tt.change != nil {
				tt.change(h.fs)
			}
			h.events = nil

			got, err := Installer{FS: h.fs}.Installed(want)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("match = %v, want %v", got, tt.want)
			}
			if c := h.changes(); len(c) != 0 {
				t.Errorf("changes = %q, want none", c)
			}
		})
	}
}

func TestInstalledChecksDirectoriesBeforeReading(t *testing.T) {
	h := newHost()
	h.mustInstall(t)
	h.fs.dirs[HelperDir] = FileStat{Mode: fs.ModeDir | 0o777}
	h.events = nil

	if _, err := (Installer{FS: h.fs}).Installed([]byte("binary v1")); !errors.Is(err, ErrUnsafeDir) {
		t.Fatalf("err = %v, want ErrUnsafeDir", err)
	}
	if len(h.events) != 0 {
		t.Errorf("events = %q, want none: the binary of an unsafe directory says nothing", h.events)
	}
}
