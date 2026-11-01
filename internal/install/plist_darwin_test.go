//go:build darwin

package install

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestPlistParsesAsJobDefinition reads the plist with plutil, the parser of
// macOS, so that a format error cannot pass as a byte comparison.
func TestPlistParsesAsJobDefinition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.plist")
	if err := os.WriteFile(path, Plist([]string{"https://hs.example.com/?a=1&b=2"}), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(t.Context(), "/usr/bin/plutil", "-convert", "json", "-o", "-", path).Output()
	if err != nil {
		t.Fatalf("plutil: %v", err)
	}
	type job struct {
		Label             string
		ProgramArguments  []string
		RunAtLoad         bool
		KeepAlive         bool
		StandardErrorPath string
	}
	var got job
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("plutil output %s: %v", out, err)
	}
	want := job{
		Label:             "io.github.nagato-yuzuru.tsunderlay",
		ProgramArguments:  []string{"/Library/PrivilegedHelperTools/io.github.nagato-yuzuru.tsunderlay", "run", "--log-file", "/var/log/tsunderlay.log", "--control-url", "https://hs.example.com/?a=1&b=2"},
		RunAtLoad:         true,
		KeepAlive:         true,
		StandardErrorPath: "/var/log/tsunderlay.log",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("job (-want +got):\n%s", diff)
	}
}
