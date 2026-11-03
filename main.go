//go:build darwin

// Command tsunderlay keeps the routes that Tailscale needs in place while a
// full-tunnel VPN is up on macOS.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// exitCode is an exit status that is not a failure of the command itself,
// for example "check found a necessary change".
type exitCode int

func (c exitCode) Error() string { return fmt.Sprintf("exit status %d", int(c)) }

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	root := newRootCmd() //nolint:contextcheck // the commands get ctx from ExecuteContext below, through cmd.Context()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	err := root.ExecuteContext(ctx)
	if code, ok := errors.AsType[exitCode](err); ok {
		return int(code)
	}
	if err != nil {
		warn(stderr, fmt.Sprintf("tsunderlay: %v\n", err))
		return 2
	}
	return 0
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "tsunderlay",
		Short:   "Keep the routes that Tailscale needs while a full-tunnel VPN is up",
		Version: version(),
		// run prints errors itself, and a usage text hides a runtime error.
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.AddCommand(newCheckCmd(), newInstallCmd(), newRunCmd(), newUninstallCmd())
	return root
}

func newCheckCmd() *cobra.Command {
	var urls []string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Print the network facts and the planned route changes",
		Long: `Print the network facts and the planned route changes.

check also compares the installed daemon (see install) with this binary. It
prints "not installed" when there is none, which is not a failure.

Exit status 0: no change is necessary. 1: a change is necessary, the VPN
state is ambiguous, or the installed daemon differs from this binary or
cannot be checked. 2: an error.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			hosts, err := controlHosts(urls)
			if err != nil {
				return err
			}
			return check(cmd.Context(), cmd.OutOrStdout(), hosts)
		},
	}
	addControlURLFlag(cmd, &urls)
	return cmd
}

// addControlURLFlag adds --control-url. check and run take the same flag, so
// that check shows what the daemon does.
func addControlURLFlag(cmd *cobra.Command, urls *[]string) {
	cmd.Flags().StringArrayVar(urls, "control-url", []string{defaultControlURL},
		"control server URL of a Tailscale node on this Mac; repeat for each server (for example a Headscale server). Replaces the default")
}

func warn(w io.Writer, s string) {
	_, _ = io.WriteString(w, s) //nolint:errcheck // the exit code already reports the failure, and stderr is the last place to report a write error
}
