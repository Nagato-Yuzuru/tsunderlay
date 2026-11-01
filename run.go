//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/rib"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/routecmd"
)

// ErrNotRoot means that run needs root to change the routing table.
var ErrNotRoot = errors.New("run needs root")

func newRunCmd() *cobra.Command {
	var (
		urls    []string
		logPath string
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Keep the routes in place until a signal stops the daemon (launchd runs this as root)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			hosts, err := controlHosts(urls)
			if err != nil {
				return err
			}
			if os.Geteuid() != 0 {
				return ErrNotRoot
			}
			w := cmd.ErrOrStderr()
			if logPath != "" {
				// The log goes on the standard error of the process, so that
				// a crash report of the runtime goes to the log too.
				if w, err = openLog(os.Stderr, logPath); err != nil {
					return err
				}
			}
			log := slog.New(slog.NewJSONHandler(w, nil))
			return runDaemon(cmd.Context(), log, urls, hosts)
		},
	}
	addControlURLFlag(cmd, &urls)
	cmd.Flags().StringVar(&logPath, "log-file", "",
		"write the log and the standard error to this file, and open it again after newsyslog rotates it (install sets it); without it, the log goes to the standard error")
	return cmd
}

// runDaemon runs until SIGTERM or SIGINT. It emits one event at start and
// one at exit.
func runDaemon(ctx context.Context, log *slog.Logger, urls, hosts []string) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	log.LogAttrs(ctx, slog.LevelInfo, "start",
		slog.String(attrServiceName, "tsunderlay"),
		slog.String(attrServiceVersion, version()),
		slog.Any(attrControlURLs, urls))

	d := &daemon{
		log:    log,
		hosts:  hosts,
		fetch:  rib.Fetch,
		lookup: net.DefaultResolver.LookupNetIP,
		apply:  routecmd.Run,
	}
	// One pending wake covers any number of route messages.
	wake := make(chan struct{}, 1)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return rib.Watch(gctx, wake) })
	g.Go(func() error {
		loop(gctx, wake, d.reconcile)
		return nil
	})
	err := g.Wait()

	reason := "signal"
	if err != nil {
		reason = err.Error()
	}
	log.LogAttrs(context.WithoutCancel(ctx), slog.LevelInfo, "exit", slog.String(attrExitReason, reason))
	if err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	return nil
}

// version is the module version that go build recorded, "(devel)" for a
// local build.
func version() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		return info.Main.Version
	}
	return "unknown"
}
