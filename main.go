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
	root := newRootCmd()
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
		Use:   "tsunderlay",
		Short: "Keep the routes that Tailscale needs while a full-tunnel VPN is up",
		// run prints errors itself, and a usage text hides a runtime error.
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.AddCommand(newCheckCmd())
	return root
}

func newCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Print the network facts and the planned route changes",
		Long: `Print the network facts and the planned route changes.

Exit status 0: no change is necessary. 1: a change is necessary, or the VPN
state is ambiguous. 2: an error.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return check(cmd.Context(), cmd.OutOrStdout())
		},
	}
}

func warn(w io.Writer, s string) {
	_, _ = io.WriteString(w, s) //nolint:errcheck // the exit code already reports the failure, and stderr is the last place to report a write error
}
