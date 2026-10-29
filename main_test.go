//go:build darwin

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
	}{
		// The Homebrew formula installs the completion scripts from this
		// command.
		{name: "zsh completion", args: []string{"completion", "zsh"}, wantCode: 0, wantStdout: "#compdef tsunderlay"},
		{name: "bash completion", args: []string{"completion", "bash"}, wantCode: 0, wantStdout: "bash completion"},
		{name: "unknown command is a usage error", args: []string{"nope"}, wantCode: 2},
		{name: "check takes no arguments", args: []string{"check", "extra"}, wantCode: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(t.Context(), tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Fatalf("run(%q) = %d, want %d; stderr: %s", tt.args, code, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("run(%q) stdout does not contain %q", tt.args, tt.wantStdout)
			}
		})
	}
}
