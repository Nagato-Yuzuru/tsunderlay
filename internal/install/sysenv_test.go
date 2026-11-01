package install

import (
	"errors"
	"io/fs"
	"testing"
)

const restartHint = "restart the network extension: sudo killall io.tailscale.ipn.macsys.network-extension\n"

func TestSystemAppEnv(t *testing.T) {
	tests := []struct {
		name string
		// old is the file before install. nil means no file.
		old  *string
		want string
		// changed tells whether install writes the file and prints the hint.
		changed bool
		// mode is the mode of the written file: the old one, or 0644.
		mode fs.FileMode
	}{
		{"line present", ptr("A=1\n" + bindByRoute + "\nB=2\n"), "A=1\n" + bindByRoute + "\nB=2\n", false, 0o600},
		{"line absent", ptr("A=1\nB=2\n"), "A=1\nB=2\n" + bindByRoute + "\n", true, 0o600},
		{"no final newline", ptr("A=1"), "A=1\n" + bindByRoute + "\n", true, 0o600},
		{"value false", ptr("A=1\n" + bindByRouteKey + "=false\nB=2\n"), "A=1\n" + bindByRoute + "\nB=2\n", true, 0o600},
		{"empty file", ptr(""), bindByRoute + "\n", true, 0o600},
		{"file absent", nil, bindByRoute + "\n", true, 0o644},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHost()
			h.mustInstall(t)
			delete(h.fs.files, SystemAppEnvPath)
			if tt.old != nil {
				h.fs.files[SystemAppEnvPath] = file{data: []byte(*tt.old), st: FileStat{Mode: 0o600}}
			}

			out := h.mustInstall(t)

			got := h.fs.files[SystemAppEnvPath]
			if string(got.data) != tt.want {
				t.Errorf("file = %q, want %q", got.data, tt.want)
			}
			wantOut := "no change\n"
			if tt.changed {
				wantOut = "wrote " + SystemAppEnvPath + "\n" + restartHint
			}
			if out != wantOut {
				t.Errorf("output = %q, want %q", out, wantOut)
			}
			if tt.changed && got.st.Mode != tt.mode {
				t.Errorf("mode = %v, want %v", got.st.Mode, tt.mode)
			}
			if out := h.mustInstall(t); out != "no change\n" {
				t.Errorf("second install output = %q, want %q", out, "no change\n")
			}
		})
	}
}

func TestSystemAppNotInstalledIsSkipped(t *testing.T) {
	h := newHost()
	delete(h.fs.dirs, SystemAppContainer)
	delete(h.fs.files, SystemAppEnvPath)
	h.mustInstall(t)

	out := h.mustInstall(t)

	want := "the system Tailscale app is not installed: skipped " + SystemAppEnvPath + "\nno change\n"
	if out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
	if _, ok := h.fs.files[SystemAppEnvPath]; ok {
		t.Errorf("install made %s without the system app", SystemAppEnvPath)
	}
}

func TestSystemAppEnvLinkRefuses(t *testing.T) {
	h := newHost()
	h.fs.files[SystemAppEnvPath] = file{st: FileStat{Mode: fs.ModeSymlink | 0o777}}

	_, err := h.install(t)

	if !errors.Is(err, ErrEnvNotRegular) {
		t.Errorf("error = %v, want ErrEnvNotRegular", err)
	}
}

func ptr[T any](v T) *T { return &v }
