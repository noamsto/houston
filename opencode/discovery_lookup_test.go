package opencode

import "testing"

func TestDiscoveryLookup(t *testing.T) {
	const known = "http://127.0.0.1:4096"
	d := NewDiscovery()
	d.addServer(known, &Server{URL: known})
	d.addServer("http://localhost", &Server{URL: "http://localhost"})

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"exact", known, known},
		{"trailing slash", known + "/", known},
		{"uppercase", "HTTP://127.0.0.1:4096", known},
		{"default port", "http://localhost:80", "http://localhost"},
		{"different host", "http://127.0.0.2:4096", ""},
		{"different port", "http://127.0.0.1:4097", ""},
		{"different scheme", "https://127.0.0.1:4096", ""},
		{"userinfo", "http://user@127.0.0.1:4096", ""},
		{"userinfo host swap", "http://127.0.0.1:4096@evil.example", ""},
		{"fragment host swap", "http://evil.example#@127.0.0.1:4096", ""},
		{"path", known + "/x", ""},
		{"encoded dot dot", known + "/%2e%2e", ""},
		{"query", known + "?x", ""},
		{"file", "file:///etc/passwd", ""},
		{"ftp", "ftp://127.0.0.1:4096", ""},
		{"gopher", "gopher://127.0.0.1:4096", ""},
		{"no scheme", "127.0.0.1:4096", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, ok := d.Lookup(tc.raw)
			if tc.want == "" {
				if ok || srv != nil {
					t.Fatalf("Lookup(%q) = %v, %v; want refusal", tc.raw, srv, ok)
				}
				return
			}
			if !ok || srv.URL != tc.want {
				t.Fatalf("Lookup(%q) = %v, %v; want %s", tc.raw, srv, ok, tc.want)
			}
		})
	}
}
