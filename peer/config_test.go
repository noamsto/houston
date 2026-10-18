package peer

import (
	"errors"
	"net"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFlags(t *testing.T) {
	cases := []struct {
		name       string
		peers      []string
		tokenFiles []string
		want       []Config // nil with wantErr
		wantErr    string
	}{
		{
			name:  "https any host, trailing slash trimmed, default token path",
			peers: []string{"halo=https://halo.example.com/houston/"},
			want:  []Config{{Name: "halo", URL: "https://halo.example.com/houston", TokenFile: "/st/peers/halo.token"}},
		},
		{
			name:  "http loopback, localhost, tailnet ip and MagicDNS fqdn",
			peers: []string{"a=http://127.0.0.1:9090", "b=http://localhost:9090", "c=http://100.101.102.103:9090", "d=http://halo.tail1234.ts.net:9090", "e=http://[fd7a:115c:a1e0::1]:9090", "f=http://[::1]:9090"},
			want: []Config{
				{Name: "a", URL: "http://127.0.0.1:9090", TokenFile: "/st/peers/a.token"},
				{Name: "b", URL: "http://localhost:9090", TokenFile: "/st/peers/b.token"},
				{Name: "c", URL: "http://100.101.102.103:9090", TokenFile: "/st/peers/c.token"},
				{Name: "d", URL: "http://halo.tail1234.ts.net:9090", TokenFile: "/st/peers/d.token"},
				{Name: "e", URL: "http://[fd7a:115c:a1e0::1]:9090", TokenFile: "/st/peers/e.token"},
				{Name: "f", URL: "http://[::1]:9090", TokenFile: "/st/peers/f.token"},
			},
		},
		{
			name:       "token file override",
			peers:      []string{"b=https://b.example.com"},
			tokenFiles: []string{"b=/secrets/b"},
			want:       []Config{{Name: "b", URL: "https://b.example.com", TokenFile: "/secrets/b"}},
		},
		{name: "http short name refused", peers: []string{"halo=http://halo:9090"}, wantErr: "http is allowed only"},
		{name: "http public ip refused", peers: []string{"x=http://8.8.8.8"}, wantErr: "http is allowed only"},
		{name: "http outside cgnat refused", peers: []string{"x=http://100.128.0.1"}, wantErr: "http is allowed only"},
		{name: "bare ts.net refused", peers: []string{"x=http://ts.net"}, wantErr: "http is allowed only"},
		{name: "local reserved", peers: []string{"local=https://a.example.com"}, wantErr: "reserved"},
		{name: "uppercase", peers: []string{"Halo=https://a.example.com"}, wantErr: "must match"},
		{name: "leading dash", peers: []string{"-a=https://a.example.com"}, wantErr: "must match"},
		{name: "dot in name", peers: []string{"a.b=https://a.example.com"}, wantErr: "must match"},
		{name: "duplicate name", peers: []string{"a=https://a.example.com", "a=https://b.example.com"}, wantErr: "declared twice"},
		{name: "duplicate url", peers: []string{"a=https://A.example.com/", "b=https://a.example.com"}, wantErr: "same URL"},
		{name: "userinfo", peers: []string{"a=https://u:secretpw@a.example.com"}, wantErr: "userinfo"},
		{name: "query", peers: []string{"a=https://a.example.com/?x=1"}, wantErr: "query"},
		{name: "empty query", peers: []string{"a=https://a.example.com/?"}, wantErr: "query"},
		{name: "fragment", peers: []string{"a=https://a.example.com/#x"}, wantErr: "fragment"},
		{name: "relative", peers: []string{"a=/api"}, wantErr: "http or https"},
		{name: "no host", peers: []string{"a=https:///x"}, wantErr: "absolute"},
		{name: "other scheme", peers: []string{"a=ftp://a.example.com"}, wantErr: "http or https"},
		{name: "malformed peer", peers: []string{"https://a.example.com"}, wantErr: "NAME=URL"},
		{name: "empty url", peers: []string{"a="}, wantErr: "NAME=URL"},
		{name: "unknown token-file name", peers: []string{"a=https://a.example.com"}, tokenFiles: []string{"b=/x"}, wantErr: "no -peer of that name"},
		{name: "duplicate token-file", peers: []string{"a=https://a.example.com"}, tokenFiles: []string{"a=/x", "a=/y"}, wantErr: "given twice"},
		{name: "malformed token-file", peers: []string{"a=https://a.example.com"}, tokenFiles: []string{"a"}, wantErr: "NAME=PATH"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseFlags(tc.peers, tc.tokenFiles, "/st")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				if strings.Contains(err.Error(), "secretpw") {
					t.Fatalf("error echoes the URL's password: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d configs, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				want := tc.want[i]
				want.TokenFile = filepath.FromSlash(want.TokenFile)
				if got[i] != want {
					t.Errorf("config %d = %+v, want %+v", i, got[i], want)
				}
			}
		})
	}
}

func TestParseFlagsNone(t *testing.T) {
	got, err := ParseFlags(nil, nil, "/st")
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want none", got, err)
	}
}

// stubInterfaces stands in the host's interfaces: names maps an address to
// the interface holding it; err fails every lookup.
func stubInterfaces(t *testing.T, names map[netip.Addr]string, err error) {
	t.Helper()
	orig := interfaceOf
	interfaceOf = func(a netip.Addr) (string, error) { return names[a], err }
	t.Cleanup(func() { interfaceOf = orig })
}

func TestPlainHopOK(t *testing.T) {
	stubInterfaces(t, map[netip.Addr]string{netip.MustParseAddr("100.101.1.1"): "tailscale0"}, nil)
	tcp := func(s string) net.Addr { return &net.TCPAddr{IP: net.ParseIP(s), Port: 1} }
	cases := []struct {
		name          string
		local, remote net.Addr
		want          bool
	}{
		{"loopback v4", tcp("127.0.0.1"), tcp("127.0.0.1"), true},
		{"loopback v6", tcp("::1"), tcp("::1"), true},
		{"loopback, LAN local", tcp("192.168.1.2"), tcp("127.0.0.1"), true},
		{"mapped loopback", tcp("::ffff:127.0.0.1"), tcp("::ffff:127.0.0.1"), true},
		{"tailnet v4 both ends", tcp("100.101.1.1"), tcp("100.64.0.7"), true},
		{"tailnet v6 both ends", tcp("fd7a:115c:a1e0::1"), tcp("fd7a:115c:a1e0::2"), true},
		{"mapped tailnet both ends", tcp("::ffff:100.101.1.1"), tcp("::ffff:100.64.0.7"), true},
		{"4-byte tailnet both ends", &net.TCPAddr{IP: net.IPv4(100, 101, 1, 1).To4()}, &net.TCPAddr{IP: net.IPv4(100, 64, 0, 7).To4()}, true},
		{"tailnet remote, LAN local", tcp("192.168.1.2"), tcp("100.64.0.7"), false},
		{"tailnet remote, public local", tcp("203.0.113.5"), tcp("100.64.0.7"), false},
		{"tailnet v6 remote, LAN v6 local", tcp("fe80::1"), tcp("fd7a:115c:a1e0::2"), false},
		{"public remote, tailnet local", tcp("100.101.1.1"), tcp("203.0.113.9"), false},
		{"public remote, public local", tcp("203.0.113.5"), tcp("203.0.113.9"), false},
		{"just outside 100.64/10", tcp("100.101.1.1"), tcp("100.128.0.1"), false},
		{"non-TCP remote", tcp("127.0.0.1"), &net.UnixAddr{Name: "/x", Net: "unix"}, false},
		{"nil addrs", nil, nil, false},
		{"nil local, tailnet remote", nil, tcp("100.64.0.7"), false},
	}
	for _, tc := range cases {
		if got := plainHopOK(tc.local, tc.remote); got != tc.want {
			t.Errorf("%s: plainHopOK = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPlainHopOKInterface(t *testing.T) {
	tcp := func(s string) net.Addr { return &net.TCPAddr{IP: net.ParseIP(s), Port: 1} }
	local := netip.MustParseAddr("100.101.1.1")
	cases := []struct {
		name  string
		names map[netip.Addr]string
		err   error
		want  bool
	}{
		{"tailscale interface", map[netip.Addr]string{local: "tailscale0"}, nil, true},
		{"utun interface", map[netip.Addr]string{local: "utun3"}, nil, true},
		{"uplink interface", map[netip.Addr]string{local: "eth0"}, nil, false},
		{"no interface", nil, nil, false},
		{"lookup error", map[netip.Addr]string{local: "tailscale0"}, errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubInterfaces(t, tc.names, tc.err)
			if got := plainHopOK(tcp("100.101.1.1"), tcp("100.64.0.7")); got != tc.want {
				t.Errorf("plainHopOK = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPlainHopOKNoLookupForLoopbackAndV6(t *testing.T) {
	stubInterfaces(t, nil, errors.New("boom"))
	tcp := func(s string) net.Addr { return &net.TCPAddr{IP: net.ParseIP(s), Port: 1} }
	if !plainHopOK(tcp("192.168.1.2"), tcp("127.0.0.1")) {
		t.Error("loopback remote refused on lookup error")
	}
	if !plainHopOK(tcp("::1"), tcp("::1")) {
		t.Error("loopback v6 refused on lookup error")
	}
	if !plainHopOK(tcp("fd7a:115c:a1e0::1"), tcp("fd7a:115c:a1e0::2")) {
		t.Error("tailnet v6 refused on lookup error")
	}
}
