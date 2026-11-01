package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const source = "/opt/homebrew/Cellar/tsunderlay/1.0/bin/tsunderlay"

// fakeFS is a file system with root-owned directories and files.
type fakeFS struct {
	files  map[string][]byte
	perms  map[string]fs.FileMode
	dirs   map[string]FileStat
	links  map[string]string
	writes []string
}

func newFakeFS() *fakeFS {
	dir := FileStat{UID: 0, Mode: fs.ModeDir | 0o755}
	return &fakeFS{
		files: map[string][]byte{source: []byte("binary v1")},
		perms: map[string]fs.FileMode{source: 0o755},
		dirs: map[string]FileStat{
			"/":                              dir,
			"/Library":                       dir,
			"/Library/LaunchDaemons":         dir,
			"/Library/PrivilegedHelperTools": {UID: 0, Mode: fs.ModeDir | fs.ModeSticky | 0o755},
		},
		links: map[string]string{},
	}
}

func (f *fakeFS) ReadFile(name string) ([]byte, error) {
	b, ok := f.files[name]
	if !ok {
		return nil, fmt.Errorf("open %s: %w", name, fs.ErrNotExist)
	}
	return b, nil
}

func (f *fakeFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	f.files[name] = slices.Clone(data)
	f.perms[name] = perm
	f.writes = append(f.writes, name)
	return nil
}

func (f *fakeFS) Remove(name string) error {
	if _, ok := f.files[name]; ok {
		f.writes = append(f.writes, "remove "+name)
	}
	delete(f.files, name)
	delete(f.perms, name)
	return nil
}

func (f *fakeFS) EvalSymlinks(path string) (string, error) {
	if real, ok := f.links[path]; ok {
		return real, nil
	}
	return path, nil
}

func (f *fakeFS) Lstat(name string) (FileStat, error) {
	if st, ok := f.dirs[name]; ok {
		return st, nil
	}
	if _, ok := f.files[name]; ok {
		return FileStat{UID: 0, Mode: f.perms[name]}, nil
	}
	return FileStat{}, fmt.Errorf("lstat %s: %w", name, fs.ErrNotExist)
}

// fakeLaunchd records the calls that change the system domain.
type fakeLaunchd struct {
	loaded bool
	calls  []string
	// fail makes the call with this verb fail.
	fail string
}

func (l *fakeLaunchd) Loaded(context.Context, string) (bool, error) { return l.loaded, nil }

func (l *fakeLaunchd) call(verb, arg string) error {
	l.calls = append(l.calls, verb+" "+arg)
	if verb == l.fail {
		return fmt.Errorf("%w: %s", ErrLaunchctl, verb)
	}
	return nil
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
	fs *fakeFS
	ld *fakeLaunchd
}

func newHost() *host { return &host{fs: newFakeFS(), ld: &fakeLaunchd{}} }

// install runs Install as root and returns its output. It forgets the
// calls and writes of earlier runs.
func (h *host) install(t *testing.T, urls ...string) (string, error) {
	t.Helper()
	h.fs.writes, h.ld.calls = nil, nil
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

func lines(s ...string) string { return strings.Join(s, "\n") + "\n" }

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
	if got := string(h.fs.files[BinaryPath]); got != "binary v1" {
		t.Errorf("installed binary = %q, want the source content", got)
	}
	if got := h.fs.perms[BinaryPath]; got != 0o755 {
		t.Errorf("binary mode = %v, want 0755", got)
	}
	if got := string(h.fs.files[PlistPath]); got != string(Plist(nil)) {
		t.Errorf("plist:\n%s\nwant:\n%s", got, Plist(nil))
	}
	if diff := cmp.Diff([]string{"bootstrap " + PlistPath}, h.ld.calls); diff != "" {
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
	if len(h.fs.writes) != 0 {
		t.Errorf("writes = %q, want none", h.fs.writes)
	}
	if len(h.ld.calls) != 0 {
		t.Errorf("launchctl calls = %q, want none", h.ld.calls)
	}
}

func TestNewBinaryIsCopiedAndRestarted(t *testing.T) {
	h := newHost()
	h.mustInstall(t)
	h.fs.files[source] = []byte("binary v2")

	out := h.mustInstall(t)

	want := lines("copied "+source+" to "+BinaryPath, "restarted "+Service)
	if out != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
	if got := string(h.fs.files[BinaryPath]); got != "binary v2" {
		t.Errorf("installed binary = %q, want %q", got, "binary v2")
	}
	if diff := cmp.Diff([]string{"kickstart " + Service}, h.ld.calls); diff != "" {
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
	plist := string(h.fs.files[PlistPath])
	for _, s := range []string{
		"<string>--control-url</string>\n\t\t<string>https://hs.example.com</string>",
		"<string>--control-url</string>\n\t\t<string>https://ts.example.org</string>",
	} {
		if !strings.Contains(plist, s) {
			t.Errorf("plist does not contain %q:\n%s", s, plist)
		}
	}
	if diff := cmp.Diff([]string{"bootout " + Service, "bootstrap " + PlistPath}, h.ld.calls); diff != "" {
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
	if len(h.fs.writes) != 0 {
		t.Errorf("writes = %q, want none", h.fs.writes)
	}
}

// A failed restart must not turn into "no change" on the next run: the
// running daemon would keep the old binary.
func TestFailedRestartIsAppliedByTheNextInstall(t *testing.T) {
	h := newHost()
	h.mustInstall(t)
	h.fs.files[source] = []byte("binary v2")
	h.ld.fail = "kickstart"

	if _, err := h.install(t); !errors.Is(err, ErrLaunchctl) {
		t.Fatalf("Install with a failing kickstart: err = %v, want ErrLaunchctl", err)
	}

	h.ld.fail = ""
	out := h.mustInstall(t)
	if want := lines("reloaded " + Service); out != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
	if diff := cmp.Diff([]string{"bootout " + Service, "bootstrap " + PlistPath}, h.ld.calls); diff != "" {
		t.Errorf("launchctl calls (-want +got):\n%s", diff)
	}
	if out := h.mustInstall(t); out != "no change\n" {
		t.Errorf("third install output = %q, want %q", out, "no change\n")
	}
}

func TestUnsafeDirectoryRefusesBeforeAnyWrite(t *testing.T) {
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
			change:  func(f *fakeFS) { f.dirs["/"] = FileStat{UID: 0, Mode: fs.ModeDir | 0o757} },
			wantDir: "/",
		},
		{
			name: "the plist directory writable by group",
			change: func(f *fakeFS) {
				f.dirs["/Library/LaunchDaemons"] = FileStat{UID: 0, Mode: fs.ModeDir | 0o775}
			},
			wantDir: "/Library/LaunchDaemons",
		},
		{
			name: "a symbolic link to a directory of a user",
			change: func(f *fakeFS) {
				f.links["/Library/PrivilegedHelperTools"] = "/Users/me/tools"
				f.dirs["/Users"] = FileStat{UID: 0, Mode: fs.ModeDir | 0o755}
				f.dirs["/Users/me"] = user
				f.dirs["/Users/me/tools"] = user
			},
			wantDir: "/Users/me",
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
			if out != "" || len(h.fs.writes) != 0 || len(h.ld.calls) != 0 {
				t.Errorf("output %q, writes %q, launchctl calls %q; want none", out, h.fs.writes, h.ld.calls)
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
	if out.Len() != 0 || len(h.fs.writes) != 0 || len(h.ld.calls) != 0 {
		t.Errorf("output %q, writes %q, launchctl calls %q; want none", out.String(), h.fs.writes, h.ld.calls)
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
