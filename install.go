//go:build darwin

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/install"
)

func newInstallCmd() *cobra.Command {
	var urls []string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Make launchd run the daemon at boot and after a crash (run with sudo, and again after each upgrade)",
		Long: `Make launchd run the daemon at boot and after a crash.

install copies this binary to
  ` + install.BinaryPath + `
writes the job definition to
  ` + install.PlistPath + `
writes the newsyslog rule that rotates ` + install.LogPath + ` to
  ` + install.RotationPath + `
and loads the job. Run it again after an upgrade: it changes only what
differs, and restarts the daemon only after a change. The job gets one
--control-url for each --control-url of install, and none when install
uses the default.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := controlHosts(urls); err != nil {
				return err
			}
			var jobURLs []string
			if cmd.Flags().Changed("control-url") {
				jobURLs = urls
			}
			exe, err := executable()
			if err != nil {
				return err
			}
			in := install.Installer{FS: install.OS{}, Launchd: install.Launchctl{}, EUID: os.Geteuid()}
			return in.Install(cmd.Context(), cmd.OutOrStdout(), exe, jobURLs)
		},
	}
	addControlURLFlag(cmd, &urls)
	return cmd
}

// executable returns the path of this binary. Homebrew links the binary
// from its prefix, so the path is the file, not the link.
func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("finding this binary: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("resolving this binary: %w", err)
	}
	return exe, nil
}
