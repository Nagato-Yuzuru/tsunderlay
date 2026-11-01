package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/underlay"
)

var (
	ownedR2 = underlay.Route{Dst: netip.MustParsePrefix("10.8.0.1/32"), Iface: "utun4", Owned: true}
	ownedR1 = underlay.Route{
		Dst: netip.MustParsePrefix("192.200.0.0/24"), Iface: "en0", Scoped: true,
		Via: netip.MustParseAddr("192.168.1.1"), Owned: true,
	}
	operator = underlay.Route{Dst: netip.MustParsePrefix("10.9.0.1/32"), Iface: "utun4"}

	equateRoutes = cmpopts.EquateComparable(netip.Addr{}, netip.Prefix{})
)

// kept is the note of Uninstall when the env file of the system app holds
// bindByRoute, as newHost makes it.
const kept = "kept " + bindByRoute + " in " + SystemAppEnvPath + "; the system Tailscale app still looks up the route of each dial"

// fakeRoutes is a routing table. It records each delete in events.
type fakeRoutes struct {
	table  []underlay.Route
	events *[]string
	// fail makes the delete of this route fail.
	fail underlay.Route
}

func (r *fakeRoutes) fetch() ([]underlay.Route, error) { return slices.Clone(r.table), nil }

func (r *fakeRoutes) apply(_ context.Context, a underlay.Action) error {
	d, ok := a.(underlay.Delete)
	if !ok {
		return fmt.Errorf("uninstall applied %T, want only deletes", a)
	}
	*r.events = append(*r.events, "route delete "+d.Route.Dst.String())
	if d.Route == r.fail {
		return errors.New("route: writing to routing socket: operation not permitted")
	}
	r.table = slices.DeleteFunc(r.table, func(o underlay.Route) bool { return o == d.Route })
	return nil
}

// uninstall runs Uninstall as root on the host with the routing table rt,
// and returns its output. It forgets the events of earlier runs.
func (h *host) uninstall(t *testing.T, rt *fakeRoutes) (string, error) {
	t.Helper()
	h.events = nil
	rt.events = &h.events
	var out bytes.Buffer
	u := Uninstaller{FS: h.fs, Launchd: h.ld, Fetch: rt.fetch, Apply: rt.apply, EUID: 0}
	err := u.Uninstall(t.Context(), &out)
	return out.String(), err
}

func (h *host) mustUninstall(t *testing.T, rt *fakeRoutes) string {
	t.Helper()
	out, err := h.uninstall(t, rt)
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	return out
}

// installed returns the files of tsunderlay that exist: the written files
// and the lock file.
func (h *host) installed() []string {
	var got []string
	for _, p := range append(writtenFiles(), LockPath) {
		if _, ok := h.fs.files[p]; ok {
			got = append(got, p)
		}
	}
	return got
}

func TestUninstallRemovesAFullInstallation(t *testing.T) {
	h := newHost()
	h.mustInstall(t)
	rt := &fakeRoutes{table: []underlay.Route{ownedR2, operator, ownedR1}}

	out := h.mustUninstall(t, rt)

	want := lines(
		"booted out "+Service,
		"removed "+PlistPath,
		"removed "+RotationPath,
		"removed "+BinaryPath,
		"deleted the owned route 10.8.0.1/32 on utun4",
		"deleted the owned route 192.200.0.0/24 on en0",
		kept,
	)
	if out != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
	// The bootout comes before the route deletes, so that the daemon cannot
	// add a route again. The lock file goes last, while the lock is held.
	wantEvents := []string{
		"lock " + LockPath,
		"print " + Service,
		"bootout " + Service,
		"remove " + PlistPath,
		"remove " + RotationPath,
		"remove " + BinaryPath,
		"route delete 10.8.0.1/32",
		"route delete 192.200.0.0/24",
		"read " + SystemAppEnvPath,
		"remove " + LockPath,
		"unlock",
	}
	if diff := cmp.Diff(wantEvents, h.events); diff != "" {
		t.Errorf("events (-want +got):\n%s", diff)
	}
	if got := h.installed(); len(got) != 0 {
		t.Errorf("files left: %q", got)
	}
	if h.ld.loaded {
		t.Error("launchd still has the job")
	}
	if diff := cmp.Diff([]underlay.Route{operator}, rt.table, equateRoutes); diff != "" {
		t.Errorf("routes left (-want +got):\n%s", diff)
	}
	if _, ok := h.fs.dirs[HelperDir]; !ok {
		t.Errorf("%s was removed; other programs use it", HelperDir)
	}
	if _, ok := h.fs.files[SystemAppEnvPath]; !ok {
		t.Errorf("%s was removed; it belongs to the system app", SystemAppEnvPath)
	}
}

func TestSecondUninstallRemovesNothing(t *testing.T) {
	h := newHost()
	h.mustInstall(t)
	rt := &fakeRoutes{table: []underlay.Route{ownedR2, operator}}
	h.mustUninstall(t, rt)

	out := h.mustUninstall(t, rt)

	// The note is not a change, so "nothing to remove" follows it.
	if want := lines(kept, "nothing to remove"); out != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
	// The lock file is not a file of the installation, so it gets no line,
	// and it does not stay.
	if diff := cmp.Diff([]string{"remove " + LockPath}, h.changes()); diff != "" {
		t.Errorf("changes (-want +got):\n%s", diff)
	}
	if got := h.installed(); len(got) != 0 {
		t.Errorf("files left: %q", got)
	}
}

// Install makes HelperDir before it takes the lock, so Uninstall does the
// same: without the lock, it could run between the steps of an install.
func TestUninstallWithNothingInstalled(t *testing.T) {
	locked := []string{
		"lock " + LockPath, "print " + Service, "read " + SystemAppEnvPath, "remove " + LockPath, "unlock",
	}
	tests := []struct {
		name        string
		noHelperDir bool
		wantEvents  []string
	}{
		{name: "HelperDir exists", wantEvents: locked},
		{
			name:        "clean macOS without HelperDir",
			noHelperDir: true,
			wantEvents:  append([]string{fmt.Sprintf("mkdir %s %v", HelperDir, fs.ModeSticky|0o755)}, locked...),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHost()
			if tt.noHelperDir {
				delete(h.fs.dirs, HelperDir)
			}
			delete(h.fs.files, SystemAppEnvPath)
			rt := &fakeRoutes{table: []underlay.Route{operator}}

			out := h.mustUninstall(t, rt)

			if out != "nothing to remove\n" {
				t.Errorf("output = %q, want %q", out, "nothing to remove\n")
			}
			if diff := cmp.Diff(tt.wantEvents, h.events); diff != "" {
				t.Errorf("events (-want +got):\n%s", diff)
			}
			if got := h.fs.dirs[HelperDir]; got != helperDir {
				t.Errorf("%s = %+v after Uninstall, want root:wheel %v", HelperDir, got, helperDir.Mode)
			}
		})
	}
}

// A daemon that stopped without a cleanup leaves its routes. Uninstall
// deletes them also when the job and the files are gone.
func TestUninstallDeletesOwnedRoutesWithoutAnInstallation(t *testing.T) {
	h := newHost()
	rt := &fakeRoutes{table: []underlay.Route{operator, ownedR2}}

	out := h.mustUninstall(t, rt)

	if want := lines("deleted the owned route 10.8.0.1/32 on utun4", kept); out != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
	if diff := cmp.Diff([]underlay.Route{operator}, rt.table, equateRoutes); diff != "" {
		t.Errorf("routes left (-want +got):\n%s", diff)
	}
}

// An install that failed halfway leaves the change mark and no job.
func TestUninstallRemovesTheChangeMark(t *testing.T) {
	h := newHost()
	h.fs.failWrite = PlistPath
	if _, err := h.install(t); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("Install with a failing plist write: err = %v, want the write error", err)
	}

	out := h.mustUninstall(t, &fakeRoutes{})

	if want := lines("removed "+BinaryPath, "removed "+PendingPath, kept); out != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
	if got := h.installed(); len(got) != 0 {
		t.Errorf("files left: %q", got)
	}
}

// After a failure, the files that are left and the lock file stay for the
// next run, and the next run removes them.
func TestFailedUninstallIsCompletedByTheNextRun(t *testing.T) {
	tests := []struct {
		name      string
		fail      func(h *host, rt *fakeRoutes)
		wantErr   error
		wantFiles []string
		wantNext  string
	}{
		{
			// The daemon still runs, so no file goes.
			name:      "bootout fails",
			fail:      func(h *host, _ *fakeRoutes) { h.ld.fail = "bootout" },
			wantErr:   ErrLaunchctl,
			wantFiles: []string{PlistPath, RotationPath, BinaryPath, LockPath},
			wantNext: lines(
				"booted out "+Service,
				"removed "+PlistPath,
				"removed "+RotationPath,
				"removed "+BinaryPath,
				"deleted the owned route 10.8.0.1/32 on utun4",
				kept,
			),
		},
		{
			name:      "a route delete fails",
			fail:      func(_ *host, rt *fakeRoutes) { rt.fail = ownedR2 },
			wantFiles: []string{LockPath},
			wantNext:  lines("deleted the owned route 10.8.0.1/32 on utun4", kept),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHost()
			h.mustInstall(t)
			rt := &fakeRoutes{table: []underlay.Route{ownedR2}}
			tt.fail(h, rt)

			_, err := h.uninstall(t, rt)

			if err == nil || (tt.wantErr != nil && !errors.Is(err, tt.wantErr)) {
				t.Fatalf("err = %v, want a failure (%v)", err, tt.wantErr)
			}
			if diff := cmp.Diff(tt.wantFiles, h.installed()); diff != "" {
				t.Errorf("files after the failure (-want +got):\n%s", diff)
			}
			if len(h.fs.lock) != 0 {
				t.Error("the lock is held after Uninstall returned")
			}

			h.ld.fail, rt.fail = "", underlay.Route{}
			if out := h.mustUninstall(t, rt); out != tt.wantNext {
				t.Errorf("next run output:\n%s\nwant:\n%s", out, tt.wantNext)
			}
		})
	}
}

// DaemonDir holds the plist and NewsyslogDir the rotation rule, so an
// unsafe one stops Uninstall before any change.
func TestUninstallUnsafeFixedDirRefusesBeforeAnyAccess(t *testing.T) {
	for _, dir := range []string{"/Library", "/private/etc"} {
		t.Run(dir, func(t *testing.T) {
			h := newHost()
			h.mustInstall(t)
			h.fs.dirs[dir] = FileStat{UID: 501, Mode: fs.ModeDir | 0o755}

			out, err := h.uninstall(t, &fakeRoutes{table: []underlay.Route{ownedR2}})

			if !errors.Is(err, ErrUnsafeDir) {
				t.Fatalf("err = %v, want ErrUnsafeDir", err)
			}
			if out != "" || len(h.events) != 0 {
				t.Errorf("output %q, events %q; want none", out, h.events)
			}
		})
	}
}

// A user who can change HelperDir can replace the binary of the root job,
// so Uninstall stops the job and deletes its routes. It does not touch a
// file in HelperDir, and then refuses.
func TestUninstallUnsafeHelperDirStopsTheJobAndRefuses(t *testing.T) {
	h := newHost()
	h.mustInstall(t)
	h.fs.dirs[HelperDir] = FileStat{Mode: fs.ModeDir | fs.ModeSticky | 0o777}
	rt := &fakeRoutes{table: []underlay.Route{ownedR2, operator}}

	out, err := h.uninstall(t, rt)

	if !errors.Is(err, ErrUnsafeDir) {
		t.Fatalf("err = %v, want ErrUnsafeDir", err)
	}
	want := lines(
		"booted out "+Service,
		"removed "+PlistPath,
		"removed "+RotationPath,
		"deleted the owned route 10.8.0.1/32 on utun4",
		kept,
	)
	if out != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
	if h.ld.loaded {
		t.Error("launchd still has the job")
	}
	if diff := cmp.Diff([]string{BinaryPath, LockPath}, h.installed()); diff != "" {
		t.Errorf("files left (-want +got):\n%s", diff)
	}
	for _, e := range h.events {
		if strings.HasPrefix(e, "lock ") || strings.HasPrefix(e, "mkdir ") {
			t.Errorf("event %q in an unsafe %s", e, HelperDir)
		}
	}
	if diff := cmp.Diff([]underlay.Route{operator}, rt.table, equateRoutes); diff != "" {
		t.Errorf("routes left (-want +got):\n%s", diff)
	}
}

// A crash during a write of install leaves the temporary file of
// OS.WriteFile next to the target. Without such a file, there is no line
// for it (TestUninstallRemovesAFullInstallation).
func TestUninstallRemovesLeftTemporaryFiles(t *testing.T) {
	tests := []struct {
		name    string
		targets []string
	}{
		{name: "temporary plist", targets: []string{PlistPath}},
		{name: "temporary binary", targets: []string{BinaryPath}},
		{name: "temporary rotation rule", targets: []string{RotationPath}},
		{name: "temporary change mark and plist", targets: []string{PlistPath, PendingPath}},
		{name: "temporary env file of the system app", targets: []string{SystemAppEnvPath}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHost()
			h.mustInstall(t)
			for _, target := range tt.targets {
				h.fs.files[tempPath(target)] = file{data: []byte("part"), st: FileStat{Mode: 0o600}}
			}

			out := h.mustUninstall(t, &fakeRoutes{})

			got := strings.Split(strings.TrimSpace(out), "\n")
			for _, target := range tt.targets {
				if want := "removed " + tempPath(target); !slices.Contains(got, want) {
					t.Errorf("output has no line %q:\n%s", want, out)
				}
				if _, ok := h.fs.files[tempPath(target)]; ok {
					t.Errorf("%s is left", tempPath(target))
				}
			}
			if _, ok := h.fs.files[SystemAppEnvPath]; !ok {
				t.Errorf("%s was removed; it belongs to the system app", SystemAppEnvPath)
			}
		})
	}
}

// The note is information, not a change: it shows only when the env file
// holds the setting that Install adds.
func TestUninstallNotesTheSettingOfTheSystemApp(t *testing.T) {
	tests := []struct {
		name    string
		content *string
		want    string
	}{
		{name: "the setting is there", content: new("A=1\n" + bindByRoute + "\n"), want: lines(kept, "nothing to remove")},
		{name: "another value", content: new(bindByRouteKey + "=false\n"), want: lines("nothing to remove")},
		{name: "no env file", want: lines("nothing to remove")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHost()
			delete(h.fs.files, SystemAppEnvPath)
			if tt.content != nil {
				h.fs.files[SystemAppEnvPath] = file{data: []byte(*tt.content), st: FileStat{Mode: 0o644}}
			}

			if out := h.mustUninstall(t, &fakeRoutes{}); out != tt.want {
				t.Errorf("output:\n%s\nwant:\n%s", out, tt.want)
			}
		})
	}
}

func TestUninstallNotRootRefuses(t *testing.T) {
	h := newHost()
	h.mustInstall(t)
	h.events = nil
	rt := &fakeRoutes{table: []underlay.Route{ownedR2}, events: &h.events}
	var out bytes.Buffer

	u := Uninstaller{FS: h.fs, Launchd: h.ld, Fetch: rt.fetch, Apply: rt.apply, EUID: 501}
	err := u.Uninstall(t.Context(), &out)

	if !errors.Is(err, ErrNotRoot) {
		t.Fatalf("err = %v, want ErrNotRoot", err)
	}
	if !strings.Contains(err.Error(), "sudo") {
		t.Errorf("error %q does not tell the operator to use sudo", err)
	}
	if out.Len() != 0 || len(h.events) != 0 {
		t.Errorf("output %q, events %q; want none", out.String(), h.events)
	}
}
