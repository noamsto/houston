package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// hostGate is the set of Host values this server answers to. It exists because
// Origin checking cannot defend against DNS rebinding: a rebound attacker
// controls both Host and Origin, so they always agree. Host is the only field
// we can pin to something the machine knows about itself.
type hostGate struct {
	hosts map[string]struct{}
}

// deriveHosts builds the allowlist from what this machine knows about itself.
// It never resolves a client-supplied name: under rebinding the attacker's
// domain genuinely does resolve to our address, so a lookup would validate the
// attacker's own claim.
func deriveHosts(extra []string, allowedOrigins []string) *hostGate {
	return newHostGate(selfKnownHosts(), extra, allowedOrigins)
}

// selfKnownHosts asks the machine what it knows about itself: its hostname
// and its Tailscale addresses, reverse-resolved to their MagicDNS names.
// Isolated from newHostGate so tests can supply a fixed host list instead of
// depending on ambient machine/network state.
func selfKnownHosts() []string {
	var out []string
	if hn, err := os.Hostname(); err == nil {
		out = append(out, hn)
	}
	for _, ip := range tailnetAddrs() {
		out = append(out, ip)
		// Reverse lookup is safe here: the input is our own bound address,
		// not anything a client sent. Bounded so a dead resolver (e.g. a
		// laptop waking on a flaky network) can't hang startup — a timeout
		// only costs the MagicDNS name, which -hostname can cover instead.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		var resolver net.Resolver
		names, err := resolver.LookupAddr(ctx, ip)
		cancel()
		if err == nil {
			for _, n := range names {
				out = append(out, strings.TrimSuffix(n, "."))
			}
		}
	}
	return out
}

// newHostGate builds the allowlist from explicit inputs, with zero ambient
// machine or network dependency: loopback literals, the given self-known
// hosts (what the machine reports about itself, in production), the
// hostnames of allowedOrigins, and any extra hosts (e.g. from -hostname).
func newHostGate(selfKnown []string, extra []string, allowedOrigins []string) *hostGate {
	g := &hostGate{hosts: map[string]struct{}{}}

	for _, h := range []string{"localhost", "127.0.0.1", "::1"} {
		g.add(h)
	}
	for _, h := range selfKnown {
		g.add(h)
	}
	for _, o := range allowedOrigins {
		if u, err := url.Parse(o); err == nil && u.Hostname() != "" {
			g.add(u.Hostname())
		}
	}
	for _, h := range extra {
		g.add(h)
	}
	return g
}

// tailnetAddrs returns this machine's Tailscale addresses (CGNAT 100.64.0.0/10).
func tailnetAddrs() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	_, cgnat, err := net.ParseCIDR("100.64.0.0/10")
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if v4 := ipnet.IP.To4(); v4 != nil && cgnat.Contains(v4) {
			out = append(out, v4.String())
		}
	}
	return out
}

func (g *hostGate) add(h string) {
	h = strings.ToLower(strings.TrimSpace(h))
	if h != "" {
		g.hosts[h] = struct{}{}
	}
}

// allows compares the hostname only. The port is already pinned by the request
// having arrived on our listener, so comparing it just breeds "[::1]:9090"
// mismatches.
func (g *hostGate) allows(rawHost string) bool {
	host := rawHost
	if h, _, err := net.SplitHostPort(rawHost); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	_, ok := g.hosts[host]
	return ok
}

// middleware rejects an unrecognised Host before any handler runs — including
// the SPA handler, so an unknown host is never issued a token.
func (g *hostGate) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g == nil || !g.allows(r.Host) {
			host := r.Host
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			w.Header().Set("X-Content-Type-Options", "nosniff")
			http.Error(w,
				fmt.Sprintf("Host %q not recognized; if this address is legitimate, restart houston with -hostname %s", host, host),
				http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}
