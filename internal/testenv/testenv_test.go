package testenv

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckMarker(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "marker")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	self := uint32(os.Getuid())

	tests := []struct {
		name    string
		path    string
		owner   uint32
		wantErr error
	}{
		{"valid", file, self, nil},
		{"missing", filepath.Join(dir, "absent"), self, errMarkerMissing},
		{"other owner", file, self + 1, errMarkerWrongUser},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := checkMarker(tt.path, tt.owner); !errors.Is(err, tt.wantErr) {
				t.Fatalf("checkMarker(%q, %d) = %v, want %v", tt.path, tt.owner, err, tt.wantErr)
			}
		})
	}
}
