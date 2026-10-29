// Package routecmd applies route actions with /sbin/route. The argument
// vector is the same as a manual repair, so a log line can be run by hand.
package routecmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/underlay"
)

// Path is the route command of macOS.
const Path = "/sbin/route"

// ErrRoute means that route reported a failure.
var ErrRoute = errors.New("route failed")

// Args returns the arguments of route for a, without the command name.
func Args(a underlay.Action) []string {
	switch a := a.(type) {
	case underlay.Add:
		// -proto1 sets RTF_PROTO1, the mark of a route that tsunderlay owns.
		return append([]string{"-n", "add", "-proto1"}, target(a.Route, true)...)
	case underlay.Delete:
		return append([]string{"-n", "delete"}, target(a.Route, false)...)
	default:
		panic(fmt.Sprintf("routecmd: unknown action %T", a))
	}
}

// target returns the destination part of the arguments. The kernel keys a
// route by destination and scope, so a delete does not need the gateway.
func target(r underlay.Route, withGateway bool) []string {
	var args []string
	if r.Scoped {
		args = append(args, "-ifscope", r.Iface)
	}
	if r.Dst.IsSingleIP() {
		args = append(args, "-host", r.Dst.Addr().String())
	} else {
		args = append(args, "-net", r.Dst.String())
	}
	if !withGateway {
		return args
	}
	if r.Via.IsValid() {
		return append(args, r.Via.String())
	}
	return append(args, "-interface", r.Iface)
}

// Run applies a. A route that is already there (add) or already gone
// (delete) is success: the action is then already in effect. Other failures
// wrap ErrRoute.
func Run(ctx context.Context, a underlay.Action) error {
	args := Args(a)
	out, err := exec.CommandContext(ctx, Path, args...).CombinedOutput()
	return result(a, out, err, strings.Join(args, " "))
}

func result(a underlay.Action, out []byte, err error, args string) error {
	if done(a, out) {
		return nil
	}
	// route exits 0 when the kernel rejects the change (seen on macOS 27 for
	// "File exists" and "not in table"), so the output decides.
	if err != nil || bytes.Contains(out, []byte("writing to routing socket")) {
		return fmt.Errorf("%w: route %s: %v: %s", ErrRoute, args, err, bytes.TrimSpace(out))
	}
	return nil
}

func done(a underlay.Action, out []byte) bool {
	switch a.(type) {
	case underlay.Add:
		return bytes.Contains(out, []byte("File exists"))
	case underlay.Delete:
		return bytes.Contains(out, []byte("not in table"))
	default:
		return false
	}
}
