//go:build darwin

package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/install"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/rib"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/routecmd"
)

func newUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Stop the daemon and remove its files and its routes (run with sudo)",
		Long: `Stop the daemon and remove its files and its routes.

uninstall boots the job ` + install.Service + ` out, removes
  ` + install.PlistPath + `
  ` + install.RotationPath + `
  ` + install.BinaryPath + `
the change mark, and the temporary files that an interrupted install
leaves, and then deletes each route that the daemon owns (RTF_PROTO1).
Routes without RTF_PROTO1 stay. uninstall does not change the env file of
the Tailscale system app; it tells when the file still holds the setting
that install added.

uninstall prints one line for each removal, or "nothing to remove". Run it
again after a failure: it removes what is left.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			u := install.Uninstaller{
				FS:      install.OS{},
				Launchd: install.Launchctl{},
				Fetch:   rib.Fetch,
				Apply:   routecmd.Run,
				EUID:    os.Geteuid(),
			}
			return u.Uninstall(cmd.Context(), cmd.OutOrStdout())
		},
	}
}
