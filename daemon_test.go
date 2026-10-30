//go:build darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/underlay"
)

// recorder records the triggers of the reconciles that loop calls. Each
// reconcile returns the next value of waits, then 0.
type recorder struct {
	triggers []string
	waits    []time.Duration
}

func (r *recorder) reconcile(_ context.Context, trigger string) time.Duration {
	r.triggers = append(r.triggers, trigger)
	if len(r.waits) == 0 {
		return 0
	}
	w := r.waits[0]
	r.waits = r.waits[1:]
	return w
}

func TestLoop(t *testing.T) {
	tests := []struct {
		name string
		// drive sends route messages and moves the fake clock.
		drive func(wake chan<- struct{})
		waits []time.Duration
		want  []string
	}{
		{
			name:  "reconciles at start",
			drive: func(chan<- struct{}) {},
			want:  []string{triggerStart},
		},
		{
			name: "a burst of route messages gives one reconcile after the wait",
			drive: func(wake chan<- struct{}) {
				for range 5 {
					wake <- struct{}{}
					time.Sleep(100 * time.Millisecond)
				}
				time.Sleep(coalesce)
			},
			want: []string{triggerStart, triggerRouteEvent},
		},
		{
			name: "steady route messages do not hold the reconcile back",
			drive: func(wake chan<- struct{}) {
				for range 30 {
					wake <- struct{}{}
					time.Sleep(100 * time.Millisecond)
				}
			},
			want: []string{triggerStart, triggerRouteEvent, triggerRouteEvent, triggerRouteEvent},
		},
		{
			name:  "retries after the wait that a reconcile returns, without a route message",
			drive: func(chan<- struct{}) { time.Sleep(3 * time.Second) },
			waits: []time.Duration{time.Second, 2 * time.Second},
			want:  []string{triggerStart, triggerRetry, triggerRetry},
		},
		{
			name:  "reconciles at the backstop without route messages",
			drive: func(chan<- struct{}) { time.Sleep(backstop) },
			want:  []string{triggerStart, triggerBackstop},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				wake := make(chan struct{})
				r := recorder{waits: tt.waits}
				done := make(chan struct{})
				go func() {
					defer close(done)
					loop(ctx, wake, r.reconcile)
				}()
				tt.drive(wake)
				synctest.Wait()
				cancel()
				<-done
				if !slices.Equal(r.triggers, tt.want) {
					t.Errorf("triggers = %q, want %q", r.triggers, tt.want)
				}
			})
		})
	}
}

var (
	lanGw = netip.MustParseAddr("192.168.1.1")
	vpnGw = netip.MustParseAddr("198.18.64.1")
	ctl   = netip.MustParsePrefix("192.200.0.0/24")

	astrillTable = []underlay.Route{
		{Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: "en0", Via: lanGw},
		{Dst: netip.MustParsePrefix("0.0.0.0/1"), Iface: "utun5", Via: vpnGw},
		{Dst: netip.MustParsePrefix("128.0.0.0/1"), Iface: "utun5", Via: vpnGw},
	}
	r1 = underlay.Route{Dst: ctl, Iface: "en0", Scoped: true, Via: lanGw, Owned: true}
	r2 = underlay.Route{Dst: netip.MustParsePrefix("198.18.64.1/32"), Iface: "utun5", Owned: true}
)

// fakeHost is the routing table and DNS of a test.
type fakeHost struct {
	table    []underlay.Route
	fetchErr error
	dnsErr   error
	applyErr error
	applied  []underlay.Action
}

func (h *fakeHost) daemon(log *slog.Logger) *daemon {
	return &daemon{
		log:   log,
		hosts: []string{"controlplane.tailscale.com"},
		fetch: func() ([]underlay.Route, error) { return h.table, h.fetchErr },
		lookup: func(context.Context, string, string) ([]netip.Addr, error) {
			if h.dnsErr != nil {
				return nil, h.dnsErr
			}
			return []netip.Addr{netip.MustParseAddr("192.200.0.104")}, nil
		},
		apply: func(_ context.Context, a underlay.Action) error {
			h.applied = append(h.applied, a)
			return h.applyErr
		},
	}
}

// events parses the JSON lines of the log.
func events(t *testing.T, b *bytes.Buffer) []map[string]any {
	t.Helper()
	var es []map[string]any
	for line := range strings.Lines(b.String()) {
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		es = append(es, e)
	}
	return es
}

func TestReconcileCorrectTableEmitsNothing(t *testing.T) {
	var b bytes.Buffer
	h := &fakeHost{table: append(slices.Clone(astrillTable), r1, r2)}
	h.daemon(slog.New(slog.NewJSONHandler(&b, nil))).reconcile(t.Context(), triggerBackstop)
	if len(h.applied) != 0 || b.Len() != 0 {
		t.Errorf("applied %v, log %q; want nothing", h.applied, b.String())
	}
}

func TestReconcileAppliedChangeEmitsOneInfoEvent(t *testing.T) {
	var b bytes.Buffer
	h := &fakeHost{table: append(slices.Clone(astrillTable), r1)}
	h.daemon(slog.New(slog.NewJSONHandler(&b, nil))).reconcile(t.Context(), triggerRouteEvent)
	if want := []underlay.Action{underlay.Add{Route: r2}}; !slices.Equal(h.applied, want) {
		t.Fatalf("applied %v, want %v", h.applied, want)
	}
	es := events(t, &b)
	if len(es) != 1 {
		t.Fatalf("events = %v, want one", es)
	}
	e := es[0]
	if e["level"] != "INFO" || e[attrTrigger] != triggerRouteEvent || e[attrVPN] != "utun5 via 198.18.64.1" {
		t.Errorf("event = %v", e)
	}
	actions, _ := e[attrActions].([]any)
	if len(actions) != 1 || !strings.Contains(actions[0].(map[string]any)["command"].(string), "-host 198.18.64.1 -interface utun5") {
		t.Errorf("%s = %v", attrActions, e[attrActions])
	}
	if _, ok := e["error.type"]; ok {
		t.Errorf("event has error.type: %v", e)
	}
}

func TestReconcileFailedActionEmitsRouteError(t *testing.T) {
	var b bytes.Buffer
	h := &fakeHost{table: slices.Clone(astrillTable), applyErr: errors.New("route failed")}
	h.daemon(slog.New(slog.NewJSONHandler(&b, nil))).reconcile(t.Context(), triggerStart)
	es := events(t, &b)
	if len(es) != 1 || es[0]["level"] != "ERROR" || es[0]["error.type"] != errTypeRoute {
		t.Errorf("events = %v, want one ERROR with error.type %s", es, errTypeRoute)
	}
}

func TestReconcileFailedReadEmitsRIBError(t *testing.T) {
	var b bytes.Buffer
	h := &fakeHost{fetchErr: errors.New("sysctl failed")}
	h.daemon(slog.New(slog.NewJSONHandler(&b, nil))).reconcile(t.Context(), triggerStart)
	es := events(t, &b)
	if len(h.applied) != 0 || len(es) != 1 || es[0]["level"] != "ERROR" || es[0]["error.type"] != errTypeRIB {
		t.Errorf("applied %v, events %v", h.applied, es)
	}
}

func TestReconcileLastingDNSFailureWarnsOnce(t *testing.T) {
	var b bytes.Buffer
	h := &fakeHost{table: append(slices.Clone(astrillTable), r1, r2), dnsErr: errors.New("no such host")}
	d := h.daemon(slog.New(slog.NewJSONHandler(&b, nil)))
	for range 3 {
		d.reconcile(t.Context(), triggerRouteEvent)
	}
	h.dnsErr = nil
	for range 2 {
		d.reconcile(t.Context(), triggerRouteEvent)
	}
	var levels []any
	for _, e := range events(t, &b) {
		levels = append(levels, e["level"])
	}
	if want := []any{"WARN", "INFO"}; !slices.Equal(levels, want) {
		t.Errorf("levels = %v, want %v", levels, want)
	}
	if len(h.applied) != 0 {
		t.Errorf("applied %v during a DNS failure, want nothing: owned routes stay", h.applied)
	}
}

func TestReconcileBacksOffAPlanWithoutEffect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var b bytes.Buffer
		// apply does not change the table, like a try that the kernel
		// refuses.
		h := &fakeHost{table: append(slices.Clone(astrillTable), r1)}
		d := h.daemon(slog.New(slog.NewJSONHandler(&b, nil)))
		var waits []time.Duration
		// A route message each 100 ms for 10 s, as the tries themselves make.
		for range 100 {
			if w := d.reconcile(t.Context(), triggerRouteEvent); w > 0 {
				waits = append(waits, w)
			}
			time.Sleep(100 * time.Millisecond)
		}
		// Tries at 0 s, 1 s, 3 s, and 7 s: the waits double from retryMin.
		if len(h.applied) != 4 {
			t.Errorf("applied %d times in 10 s, want 4", len(h.applied))
		}
		if waits[0] != time.Second {
			t.Errorf("first wait = %v, want 1s", waits[0])
		}
		var warns int
		for _, e := range events(t, &b) {
			if e["level"] == "WARN" {
				warns++
			}
		}
		if warns != 1 {
			t.Errorf("WARN events = %d, want 1", warns)
		}
	})
}

func TestReconcileRetriesSoonWhenTheFirstTryHadNoEffect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var b bytes.Buffer
		// The first add of R2 has no effect: the new tunnel is not ready.
		// The second one works.
		h := &fakeHost{table: append(slices.Clone(astrillTable), r1)}
		d := h.daemon(slog.New(slog.NewJSONHandler(&b, nil)))
		d.apply = func(_ context.Context, a underlay.Action) error {
			h.applied = append(h.applied, a)
			if len(h.applied) == 2 {
				h.table = append(h.table, r2)
			}
			return nil
		}
		// The goal is a repair within about 1 s of the event.
		wait := d.reconcile(t.Context(), triggerRouteEvent)
		if wait != time.Second {
			t.Fatalf("wait after the first try = %v, want 1s", wait)
		}
		time.Sleep(wait)
		if w := d.reconcile(t.Context(), triggerRetry); len(h.applied) != 2 {
			t.Fatalf("applied %d times after the retry, want 2 (wait %v)", len(h.applied), w)
		}
		if w := d.reconcile(t.Context(), triggerRouteEvent); w != 0 || len(h.applied) != 2 {
			t.Errorf("after the table caught up: wait %v, applied %d; want 0 and 2", w, len(h.applied))
		}
	})
}

func TestReconcileBlockedRouteWarnsOnceAndAddsNothing(t *testing.T) {
	var b bytes.Buffer
	stale := underlay.Route{Dst: ctl, Iface: "en0", Scoped: true, Via: netip.MustParseAddr("192.168.2.1")}
	h := &fakeHost{table: append(slices.Clone(astrillTable), r2, stale)}
	d := h.daemon(slog.New(slog.NewJSONHandler(&b, nil)))
	for range 3 {
		d.reconcile(t.Context(), triggerRouteEvent)
	}
	if len(h.applied) != 0 {
		t.Errorf("applied %v, want nothing", h.applied)
	}
	es := events(t, &b)
	if len(es) != 1 || es[0]["level"] != "WARN" {
		t.Fatalf("events = %v, want one WARN", es)
	}
	blocked, _ := es[0][attrBlocked].([]any)
	if len(blocked) != 1 || !strings.Contains(blocked[0].(string), "/sbin/route -n delete -ifscope en0 -net 192.200.0.0/24") {
		t.Errorf("%s = %v, want the delete command of the operator route", attrBlocked, es[0][attrBlocked])
	}
}
