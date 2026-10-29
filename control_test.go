//go:build darwin

package main

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"testing"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/underlay"
)

func TestControlHosts(t *testing.T) {
	tests := []struct {
		name    string
		urls    []string
		want    []string
		wantErr bool
	}{
		{name: "default", urls: []string{defaultControlURL}, want: []string{"controlplane.tailscale.com"}},
		{name: "port and path are dropped", urls: []string{"https://hs.example.com:8443/path"}, want: []string{"hs.example.com"}},
		{name: "http is accepted", urls: []string{"http://192.0.2.7"}, want: []string{"192.0.2.7"}},
		{name: "duplicates are removed", urls: []string{"https://a.example", "https://a.example:443", "https://b.example"}, want: []string{"a.example", "b.example"}},
		{name: "a bare host name is not a URL", urls: []string{"hs.example.com"}, wantErr: true},
		{name: "another scheme is rejected", urls: []string{"ftp://hs.example.com"}, wantErr: true},
		{name: "a URL without a host is rejected", urls: []string{"https://"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := controlHosts(tt.urls)
			if tt.wantErr {
				if !errors.Is(err, ErrControlURL) {
					t.Fatalf("controlHosts(%q) error = %v, want ErrControlURL", tt.urls, err)
				}
				return
			}
			if err != nil || !slices.Equal(got, tt.want) {
				t.Errorf("controlHosts(%q) = %q, %v; want %q", tt.urls, got, err, tt.want)
			}
		})
	}
}

func TestResolveControl(t *testing.T) {
	errNoHost := errors.New("no such host")
	answers := map[string][]netip.Addr{
		"controlplane.tailscale.com": {netip.MustParseAddr("192.200.0.104"), netip.MustParseAddr("192.200.0.7")},
		"hs.example.com":             {netip.MustParseAddr("198.51.100.20")},
	}
	var asked []string
	lookup := func(_ context.Context, network, host string) ([]netip.Addr, error) {
		if network != "ip4" {
			t.Errorf("lookup network = %q, want ip4", network)
		}
		asked = append(asked, host)
		if as, ok := answers[host]; ok {
			return as, nil
		}
		return nil, errNoHost
	}
	pfx := netip.MustParsePrefix
	tests := []struct {
		name      string
		hosts     []string
		want      underlay.Control
		wantErr   bool
		wantAsked []string
	}{
		{
			name:      "addresses of all hosts, as sorted /24s",
			hosts:     []string{"hs.example.com", "controlplane.tailscale.com"},
			want:      underlay.Control{Prefixes: []netip.Prefix{pfx("192.200.0.0/24"), pfx("198.51.100.0/24")}, Complete: true},
			wantAsked: []string{"hs.example.com", "controlplane.tailscale.com"},
		},
		{
			name:      "an IP literal needs no lookup",
			hosts:     []string{"203.0.113.7"},
			want:      underlay.Control{Prefixes: []netip.Prefix{pfx("203.0.113.0/24")}, Complete: true},
			wantAsked: nil,
		},
		{
			name:      "a failed lookup makes the answer incomplete, and the other hosts still count",
			hosts:     []string{"controlplane.tailscale.com", "down.example.com"},
			want:      underlay.Control{Prefixes: []netip.Prefix{pfx("192.200.0.0/24")}},
			wantErr:   true,
			wantAsked: []string{"controlplane.tailscale.com", "down.example.com"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asked = nil
			got, err := resolveControl(t.Context(), lookup, tt.hosts)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveControl error = %v, want error %t", err, tt.wantErr)
			}
			if got.Complete != tt.want.Complete || !slices.Equal(got.Prefixes, tt.want.Prefixes) {
				t.Errorf("resolveControl = %v, want %v", got, tt.want)
			}
			if !slices.Equal(asked, tt.wantAsked) {
				t.Errorf("looked up %q, want %q", asked, tt.wantAsked)
			}
		})
	}
}
