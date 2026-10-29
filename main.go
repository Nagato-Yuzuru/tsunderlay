//go:build darwin

// Command tsunderlay keeps the routes that Tailscale needs in place while a
// full-tunnel VPN is up on macOS.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
)

const usage = `usage: tsunderlay check

check  print the network facts and the planned route changes.
       Exit 0: no change is necessary. Exit 1: a change is necessary,
       or the VPN state is ambiguous. Exit 2: an error.
`

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		warn(stderr, usage)
		return 2
	}
	switch args[0] {
	case "check":
		code, err := check(ctx, stdout)
		if err != nil {
			warn(stderr, fmt.Sprintf("tsunderlay: %v\n", err))
		}
		return code
	default:
		warn(stderr, usage)
		return 2
	}
}

func warn(w io.Writer, s string) {
	_, _ = io.WriteString(w, s) //nolint:errcheck // the exit code already reports the failure, and stderr is the last place to report a write error
}
