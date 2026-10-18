package peer

import (
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
