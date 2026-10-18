// Package peer mirrors another houston's local runs into this one's registry,
// read-only: one Source per configured peer dials the peer's runs stream.
package peer

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

// Config is one -peer flag, validated.
type Config struct {
	Name      string
	URL       string // scheme://host[:port][/prefix], no trailing "/"
	TokenFile string
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// localName is the UI's name for this host's own group.
const localName = "local"

var (
	tailnetV4 = netip.MustParsePrefix("100.64.0.0/10")
	tailnetV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// ParseFlags validates -peer NAME=URL and -peer-token-file NAME=PATH values.
// A peer with no token-file flag reads <statusDir>/peers/NAME.token. Errors
// never echo a URL, which could carry userinfo.
func ParseFlags(peers, tokenFiles []string, statusDir string) ([]Config, error) {
	out := make([]Config, 0, len(peers))
	byName := map[string]int{}
	byURL := map[string]string{}
	for _, v := range peers {
		name, raw, ok := splitFlag(v)
		if !ok {
			return nil, errors.New("-peer must be NAME=URL")
		}
		if err := checkName(name); err != nil {
			return nil, fmt.Errorf("-peer %q: %w", name, err)
		}
		if _, dup := byName[name]; dup {
			return nil, fmt.Errorf("-peer %q: declared twice", name)
		}
		u, key, err := parseURL(raw)
		if err != nil {
			return nil, fmt.Errorf("-peer %q: %w", name, err)
		}
		if other, dup := byURL[key]; dup {
			return nil, fmt.Errorf("-peer %q: same URL as peer %q", name, other)
		}
		byName[name] = len(out)
		byURL[key] = name
		out = append(out, Config{Name: name, URL: u, TokenFile: filepath.Join(statusDir, "peers", name+".token")})
	}

	seen := map[string]bool{}
	for _, v := range tokenFiles {
		name, path, ok := splitFlag(v)
		if !ok {
			return nil, errors.New("-peer-token-file must be NAME=PATH")
		}
		i, declared := byName[name]
		if !declared {
			return nil, fmt.Errorf("-peer-token-file %q: no -peer of that name", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("-peer-token-file %q: given twice", name)
		}
		seen[name] = true
		out[i].TokenFile = path
	}
	return out, nil
}

func splitFlag(v string) (name, value string, ok bool) {
	name, value, ok = strings.Cut(v, "=")
	return name, value, ok && name != "" && value != ""
}

func checkName(name string) error {
	if !nameRe.MatchString(name) {
		return errors.New("name must match ^[a-z0-9][a-z0-9-]{0,62}$")
	}
	if name == localName {
		return errors.New(`name "local" is reserved for this host`)
	}
	return nil
}

// parseURL returns the normalised URL and a comparison key (host lowercased)
// for duplicate detection.
func parseURL(raw string) (normal, key string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", errors.New("URL does not parse")
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return "", "", errors.New("URL must be http or https")
	case u.Opaque != "" || u.Host == "" || u.Hostname() == "":
		return "", "", errors.New("URL must be absolute")
	case u.User != nil:
		return "", "", errors.New("URL must not carry userinfo")
	case u.RawQuery != "" || u.ForceQuery:
		return "", "", errors.New("URL must not carry a query")
	case u.Fragment != "" || strings.Contains(raw, "#"):
		return "", "", errors.New("URL must not carry a fragment")
	}
	if u.Scheme == "http" && !plainHTTPOK(u.Hostname()) {
		return "", "", errors.New("http is allowed only for loopback, localhost or a tailnet address (100.64.0.0/10, fd7a:115c:a1e0::/48, *.ts.net); use https")
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	return u.Scheme + "://" + u.Host + path, u.Scheme + "://" + strings.ToLower(u.Host) + path, nil
}

// plainHTTPOK reports whether an http URL's host names this machine or the
// tailnet. It only gates the URL text: a *.ts.net name can resolve through
// public DNS and a 100.x address can route off the tailnet, so plainHopOK on
// every dialled connection is what keeps the bearer token off a cleartext hop.
// A bare MagicDNS short name is refused: telling it is a tailnet name would
// mean resolving it.
func plainHTTPOK(host string) bool {
	h := strings.ToLower(host)
	if h == "localhost" {
		return true
	}
	if strings.HasSuffix(h, ".ts.net") && len(h) > len(".ts.net") {
		return true
	}
	addr, err := netip.ParseAddr(h)
	if err != nil {
		return false
	}
	addr = addr.Unmap().WithZone("")
	return addr.IsLoopback() || inTailnet(addr)
}

// plainHopOK reports whether a plain-http connection stays on this machine or
// goes over the tailnet, where WireGuard encrypts the hop: the remote end is
// loopback, or both ends hold tailnet addresses and, for IPv4, the local
// address is held by a Tailscale interface (tailscale* on Linux, utun* on
// macOS), because 100.64.0.0/10 is also RFC 6598 carrier-grade NAT space. A
// lookup error refuses. fd7a:115c:a1e0::/48 is Tailscale-specific, so IPv6
// needs no interface check. Residual: on macOS another VPN's utun interface
// holding a 100.64/10 address still passes; https avoids it.
func plainHopOK(local, remote net.Addr) bool {
	r, ok := tcpAddr(remote)
	if !ok {
		return false
	}
	if r.IsLoopback() {
		return true
	}
	l, ok := tcpAddr(local)
	if !ok || !inTailnet(r) || !inTailnet(l) {
		return false
	}
	return !l.Is4() || onTailscaleInterface(l)
}

// onTailscaleInterface fails closed: a lookup error refuses the hop.
func onTailscaleInterface(addr netip.Addr) bool {
	name, err := interfaceOf(addr)
	return err == nil && (strings.HasPrefix(name, "tailscale") || strings.HasPrefix(name, "utun"))
}

// interfaceOf names the interface holding addr on this host, "" when none
// does; a var so tests can stand in a host's interfaces.
var interfaceOf = hostInterfaceOf

func hostInterfaceOf(addr netip.Addr) (string, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, ifc := range ifs {
		addrs, err := ifc.Addrs()
		if err != nil {
			return "", err
		}
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip, ok := netip.AddrFromSlice(n.IP); ok && ip.Unmap() == addr {
				return ifc.Name, nil
			}
		}
	}
	return "", nil
}

func tcpAddr(a net.Addr) (netip.Addr, bool) {
	tcp, ok := a.(*net.TCPAddr)
	if !ok || tcp == nil {
		return netip.Addr{}, false
	}
	addr, ok := netip.AddrFromSlice(tcp.IP)
	return addr.Unmap(), ok
}

func inTailnet(addr netip.Addr) bool {
	return tailnetV4.Contains(addr) || tailnetV6.Contains(addr)
}
