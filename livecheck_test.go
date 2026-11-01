//go:build darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/netip"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/underlay"
)

// liveCheckOutEnv names the file that TestLogAttributes writes the log
// attributes to, as the text input of `weaver registry live-check`. The
// mise task telemetry:live-check sets it. Without it, the test writes
// nothing.
const liveCheckOutEnv = "TSUNDERLAY_LIVECHECK_OUT"

// eventKinds are the messages of all daemon events.
var eventKinds = []string{
	"start", "routes changed", "route change failed", "reconcile failed",
	"degraded", "recovered", "exit",
}

// TestLogAttributes makes the daemon emit each event kind and collects the
// attributes of the events. The live check compares each attribute name
// with the registry, so a key that is not in the registry fails it.
func TestLogAttributes(t *testing.T) {
	var b bytes.Buffer
	emitLifecycle(t, &b)
	emitReconcileEvents(t, slog.New(slog.NewJSONHandler(&b, nil)))

	counts := map[string]int{}
	var lines []string
	for _, e := range rawEvents(t, &b) {
		var msg string
		if err := json.Unmarshal(e[slog.MessageKey], &msg); err != nil {
			t.Fatalf("event has no message: %v", err)
		}
		counts[msg]++
		lines = append(lines, attributeLines(e)...)
	}
	for _, k := range slices.Sorted(maps.Keys(counts)) {
		if !slices.Contains(eventKinds, k) {
			t.Errorf("event %q is not in eventKinds", k)
		}
	}
	for _, k := range eventKinds {
		if counts[k] == 0 {
			t.Errorf("no %q event: add a case that emits it", k)
		}
		t.Logf("%-20s %d", k, counts[k])
	}

	if path := os.Getenv(liveCheckOutEnv); path != "" {
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// emitLifecycle runs the daemon with a context that has already ended, to
// get the start and the exit event. It keeps only those two events: the
// reconcile at start reads the table of this host, so its events change
// from host to host. The ended context stops each route command before it
// runs, so the test does not change the table.
func emitLifecycle(t *testing.T, w *bytes.Buffer) {
	t.Helper()
	var b bytes.Buffer
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	hosts, err := controlHosts([]string{defaultControlURL})
	if err != nil {
		t.Fatal(err)
	}
	if err := runDaemon(ctx, slog.New(slog.NewJSONHandler(&b, nil)), []string{defaultControlURL}, hosts); err != nil {
		t.Fatalf("runDaemon: %v", err)
	}
	for line := range strings.Lines(b.String()) {
		var e struct{ Msg string }
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		if e.Msg == "start" || e.Msg == "exit" {
			w.WriteString(line)
		}
	}
}

// emitReconcileEvents emits each reconcile event on the fake host, and each
// attribute of the degraded event at least once.
func emitReconcileEvents(t *testing.T, log *slog.Logger) {
	t.Helper()
	ctx := t.Context()
	// routes changed
	(&fakeHost{table: append(slices.Clone(astrillTable), r1)}).daemon(log).reconcile(ctx, triggerRouteEvent)
	// route change failed
	(&fakeHost{table: slices.Clone(astrillTable), applyErr: errors.New("route failed")}).daemon(log).reconcile(ctx, triggerStart)
	// reconcile failed
	(&fakeHost{fetchErr: errors.New("sysctl failed")}).daemon(log).reconcile(ctx, triggerBackstop)

	// degraded by a DNS failure, then recovered
	h := &fakeHost{table: append(slices.Clone(astrillTable), r1, r2), dnsErr: errors.New("no such host")}
	d := h.daemon(log)
	d.reconcile(ctx, triggerRouteEvent)
	h.dnsErr = nil
	d.reconcile(ctx, triggerRouteEvent)

	// degraded by an ambiguous VPN
	ambiguous := []underlay.Route{
		astrillTable[0], astrillTable[1],
		{Dst: netip.MustParsePrefix("128.0.0.0/1"), Iface: "utun6", Via: netip.MustParseAddr("198.18.65.1")},
	}
	(&fakeHost{table: ambiguous}).daemon(log).reconcile(ctx, triggerBackstop)

	// degraded by a blocked route
	link := underlay.Route{Dst: netip.MustParsePrefix("192.168.2.0/24"), Iface: "en0"}
	operator := underlay.Route{Dst: ctl, Iface: "en0", Scoped: true, Via: netip.MustParseAddr("192.168.2.1")}
	(&fakeHost{table: append(slices.Clone(astrillTable), r2, link, operator)}).daemon(log).reconcile(ctx, triggerRouteEvent)

	// degraded by a plan without effect: apply does not change the table
	d = (&fakeHost{table: append(slices.Clone(astrillTable), r1)}).daemon(log)
	d.reconcile(ctx, triggerRouteEvent)
	d.reconcile(ctx, triggerRetry)
}

// rawEvents parses the JSON lines of the log, and keeps each value as JSON.
func rawEvents(t *testing.T, b *bytes.Buffer) []map[string]json.RawMessage {
	t.Helper()
	var es []map[string]json.RawMessage
	for line := range strings.Lines(b.String()) {
		var e map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		es = append(es, e)
	}
	return es
}

// attributeLines returns the attributes of an event as name=value lines,
// sorted by name. The live check reads a value as JSON, so an array or an
// object keeps its shape. The time, the level, and the message are fields
// of the log record and not attributes: an OTLP log record has them as the
// timestamp, the severity, and the body. The registry does not define them.
func attributeLines(e map[string]json.RawMessage) []string {
	var lines []string
	for _, k := range slices.Sorted(maps.Keys(e)) {
		switch k {
		case slog.TimeKey, slog.LevelKey, slog.MessageKey:
			continue
		}
		lines = append(lines, k+"="+string(e[k]))
	}
	return lines
}
