//go:build darwin

package main

import (
	"context"
	"log/slog"
	"strings"
	"time"

	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/routecmd"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/underlay"
)

const (
	// coalesce is the wait between the first route message and the
	// reconcile. Messages in the wait add nothing: the reconcile reads the
	// full table. The wait does not restart on each message: a steady stream
	// of messages would then hold the reconcile back without end.
	coalesce = time.Second
	// backstop is the longest time between two reconciles. It repairs a
	// change that sent no route message.
	backstop = 5 * time.Minute
	// retryMin and retryMax bound the wait before a plan is applied again
	// when the last try left the table unchanged.
	retryMin = time.Second
	retryMax = time.Minute
)

// loop calls reconcile at start, after route messages, at each backstop
// tick, and when a reconcile asks for a retry, until ctx ends. reconcile
// returns the wait before the retry, or 0 for no retry.
func loop(ctx context.Context, wake <-chan struct{}, reconcile func(context.Context, string) time.Duration) {
	var fire, retry <-chan time.Time
	schedule := func(wait time.Duration) {
		retry = nil
		if wait > 0 {
			retry = time.After(wait)
		}
	}
	schedule(reconcile(ctx, triggerStart))
	tick := time.NewTicker(backstop)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
			if fire == nil {
				fire = time.After(coalesce)
			}
		case <-fire:
			fire = nil
			schedule(reconcile(ctx, triggerRouteEvent))
		case <-retry:
			schedule(reconcile(ctx, triggerRetry))
		case <-tick.C:
			schedule(reconcile(ctx, triggerBackstop))
		}
	}
}

// daemon holds what one reconcile needs. Each field is a seam for tests.
type daemon struct {
	log    *slog.Logger
	hosts  []string
	fetch  func() ([]underlay.Route, error)
	lookup lookupFunc
	apply  func(context.Context, underlay.Action) error
	// lastWarning is the last degraded state that was logged. A route
	// message can come each second, so a lasting state is logged once.
	lastWarning string
	// lastApplied is the plan that the last reconcile applied, as command
	// lines. Empty when the last plan was empty.
	lastApplied string
	// retryDelay is the wait after the last try of lastApplied, and retryAt
	// is the earliest time of the next try.
	retryDelay time.Duration
	retryAt    time.Time
}

// actionResult is one applied action in the reconcile event.
type actionResult struct {
	Command string `json:"command"`
	Error   string `json:"error,omitempty"`
}

// reconcile reads the state, applies the plan, and emits one event when it
// applied an action or failed. A run on a correct table emits nothing. It
// returns the wait before the next try when it applied a plan, or 0.
func (d *daemon) reconcile(ctx context.Context, trigger string) time.Duration {
	start := time.Now()
	table, err := d.fetch()
	if err != nil {
		d.log.LogAttrs(ctx, slog.LevelError, "reconcile failed",
			slog.String(attrTrigger, trigger),
			slog.String(string(semconv.ErrorTypeKey), errTypeRIB),
			slog.String("error", err.Error()))
		return 0
	}
	control, dnsErr := resolveControl(ctx, d.lookup, d.hosts)
	f := underlay.DeriveFacts(table, control)
	plan := underlay.Plan(f)
	lines := planLines(plan)
	// The last reconcile applied this plan, and the table still needs it: the
	// try had no effect, for example because the new tunnel was not ready.
	// Each try makes route messages, so a try at each message would repeat
	// it each second. The tries back off instead, and loop wakes for each.
	noEffect := lines != "" && lines == d.lastApplied
	d.warnOnChange(ctx, f, dnsErr, underlay.Blocks(f), noEffect, lines)
	if lines == "" {
		d.lastApplied, d.retryDelay = "", 0
		return 0
	}
	if noEffect {
		if wait := d.retryAt.Sub(start); wait > 0 {
			return wait
		}
		d.retryDelay = min(2*d.retryDelay, retryMax)
	} else {
		d.retryDelay = retryMin
	}
	d.lastApplied, d.retryAt = lines, start.Add(d.retryDelay)

	level := slog.LevelInfo
	results := make([]actionResult, 0, len(plan))
	for _, a := range plan {
		r := actionResult{Command: routecmd.Line(a)}
		if err := d.apply(ctx, a); err != nil {
			r.Error = err.Error()
			level = slog.LevelError
		}
		results = append(results, r)
	}
	attrs := []slog.Attr{
		slog.String(attrTrigger, trigger),
		slog.String(attrLAN, lan(f.LAN)),
		slog.String(attrVPN, vpn(f.VPN)),
		slog.String(attrControl, joinPrefixes(f.Control.Prefixes)),
		slog.Any(attrActions, results),
		slog.Int64(attrDurationMS, time.Since(start).Milliseconds()),
	}
	if level == slog.LevelError {
		attrs = append(attrs, slog.String(string(semconv.ErrorTypeKey), errTypeRoute))
		d.log.LogAttrs(ctx, level, "route change failed", attrs...)
		return d.retryDelay
	}
	d.log.LogAttrs(ctx, level, "routes changed", attrs...)
	return d.retryDelay
}

// warnOnChange logs a degraded state when it starts or changes, and logs
// the recovery once. The daemon keeps working in each of these states.
func (d *daemon) warnOnChange(ctx context.Context, f underlay.Facts, dnsErr error, blocked []underlay.Blocked, noEffect bool, applied string) {
	var parts []string
	var attrs []slog.Attr
	if _, ok := f.VPN.(underlay.Ambiguous); ok {
		parts = append(parts, "ambiguous VPN")
		attrs = append(attrs, slog.String(attrVPN, vpn(f.VPN)))
	}
	if dnsErr != nil {
		parts = append(parts, dnsErr.Error())
		attrs = append(attrs, slog.String(attrDNSError, dnsErr.Error()))
	}
	if len(blocked) > 0 {
		bs := blockedLines(blocked)
		parts = append(parts, bs...)
		attrs = append(attrs, slog.Any(attrBlocked, bs))
	}
	if noEffect {
		parts = append(parts, "no effect: "+applied)
		attrs = append(attrs, slog.String(attrNoEffect, applied))
	}
	warning := strings.Join(parts, "; ")
	switch {
	case warning == d.lastWarning:
	case warning != "":
		d.log.LogAttrs(ctx, slog.LevelWarn, "degraded", attrs...)
	default:
		d.log.LogAttrs(ctx, slog.LevelInfo, "recovered")
	}
	d.lastWarning = warning
}

// planLines returns the command lines of a plan, one per line.
func planLines(plan []underlay.Action) string {
	lines := make([]string, len(plan))
	for i, a := range plan {
		lines[i] = routecmd.Line(a)
	}
	return strings.Join(lines, "\n")
}
