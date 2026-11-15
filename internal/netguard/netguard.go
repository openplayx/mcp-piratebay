// Package netguard is the outbound request policy, checked before every
// request to the source.
//
// Scheme, host allowlist, port, and resolved addresses (no loopback,
// private, link-local/metadata or other non-global targets unless enabled
// for LAN fixtures). Redirects are never followed (see package source).
//
// Residual risk: the address check and the connection resolve separately,
// so a hostile DNS server for an allowed host could still rebind between
// the two. Allowed hosts are administrator-chosen, which keeps this out of
// reach of API callers.
package netguard

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/openplayx/mcp-piratebay/internal/apierr"
	"github.com/openplayx/mcp-piratebay/internal/config"
)

type Resolver func(ctx context.Context, host string, port int) ([]string, error)

func SystemResolver(ctx context.Context, host string, port int) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

// Ranges that are not globally reachable although netip calls them global
// unicast (mirrors Python's ipaddress is_global).
var special = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "255.255.255.255/32",
		"64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// AddressOK reports whether addr is a public unicast address.
func AddressOK(addr string) bool {
	a, err := netip.ParseAddr(strings.SplitN(addr, "%", 2)[0])
	if err != nil {
		return false
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() {
		return false
	}
	for _, p := range special {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// CheckURL enforces the policy for one outbound request URL.
func CheckURL(ctx context.Context, u *url.URL, src config.Source, resolve Resolver) *apierr.Error {
	schemes := []string{"https"}
	if src.AllowHTTP {
		schemes = append(schemes, "http")
	}
	if !slices.Contains(schemes, u.Scheme) {
		return apierr.SourceBlocked()
	}
	host := strings.ToLower(u.Hostname())
	if !slices.Contains(src.AllowedHosts, host) {
		return apierr.SourceBlocked()
	}
	port := 443
	if u.Scheme == "http" {
		port = 80
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return apierr.SourceBlocked()
		}
		port = n
	}
	if port != src.Port() {
		return apierr.SourceBlocked()
	}
	if src.AllowPrivateAddresses {
		return nil
	}
	var addrs []string
	if _, err := netip.ParseAddr(host); err == nil {
		addrs = []string{host}
	} else {
		var err error
		if addrs, err = resolve(ctx, host, port); err != nil {
			return apierr.UpstreamUnavailable("The source host does not resolve.")
		}
	}
	if len(addrs) == 0 {
		return apierr.SourceBlocked()
	}
	for _, a := range addrs {
		if !AddressOK(a) {
			return apierr.SourceBlocked()
		}
	}
	return nil
}
