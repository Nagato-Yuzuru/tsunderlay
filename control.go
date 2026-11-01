//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/underlay"
)

// The control plane of tailscale.com.
const (
	defaultControlHost = "controlplane.tailscale.com"
	defaultControlURL  = "https://" + defaultControlHost
)

const dnsTimeout = 5 * time.Second

// ErrControlURL means that a --control-url value is not an absolute http or
// https URL in UTF-8.
var ErrControlURL = errors.New("control URL must be an absolute http or https URL in UTF-8")

// controlHosts returns the host of each control URL, without duplicates.
// It wraps ErrControlURL for a value that is not a URL with a host. A value
// must be UTF-8: install writes it into a plist, and a plist parser reads
// another string from bytes that are not UTF-8.
func controlHosts(urls []string) ([]string, error) {
	var hosts []string
	for _, s := range urls {
		u, err := url.Parse(s)
		if err != nil || !utf8.ValidString(s) || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return nil, fmt.Errorf("%w: %q", ErrControlURL, s)
		}
		if h := u.Hostname(); !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	return hosts, nil
}

// lookupFunc resolves a host name to its addresses. net.Resolver.LookupNetIP
// has this shape.
type lookupFunc func(ctx context.Context, network, host string) ([]netip.Addr, error)

// resolveControl resolves the control hosts. An IP literal needs no DNS.
// The answer holds the /24s of every host that resolved, and it is complete
// only when all of them did. The error joins the failed lookups.
func resolveControl(ctx context.Context, lookup lookupFunc, hosts []string) (underlay.Control, error) {
	ctx, cancel := context.WithTimeout(ctx, dnsTimeout)
	defer cancel()
	var addrs []netip.Addr
	var errs []error
	for _, h := range hosts {
		if a, err := netip.ParseAddr(h); err == nil {
			addrs = append(addrs, a)
			continue
		}
		as, err := lookup(ctx, "ip4", h)
		if err != nil {
			errs = append(errs, fmt.Errorf("resolving %s: %w", h, err))
			continue
		}
		addrs = append(addrs, as...)
	}
	c := underlay.Control{Prefixes: underlay.ControlPrefixes(addrs), Complete: len(errs) == 0}
	return c, errors.Join(errs...)
}

// undocumentedControl returns the resolved prefixes that are not in the list
// of the Tailscale firewall documentation. Only the default control URL has
// such a list: hosts of a self-hosted control server give no result.
func undocumentedControl(hosts []string, ps []netip.Prefix) []netip.Prefix {
	if !slices.Equal(hosts, []string{defaultControlHost}) {
		return nil
	}
	return underlay.UndocumentedControl(ps)
}
