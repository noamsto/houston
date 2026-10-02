package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/noamsto/houston/opencode"
)

func fakeOpenCode(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/global/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"healthy":true,"version":"t"}`))
	})
	mux.HandleFunc("/project/current", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/session/status", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/session/s1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"s1","title":"t"}`))
	})
	mux.HandleFunc("/session/s1/message", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/session/s1/todo", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/session/s1/abort", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`true`))
	})
	mux.HandleFunc("/session/s1/prompt_async", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOpenCodeSessionOnlyReachesDiscoveredServers(t *testing.T) {
	var decoyHits atomic.Int32
	decoy := fakeOpenCode(t, &decoyHits)
	fake := fakeOpenCode(t, nil)

	disc := opencode.NewDiscovery(opencode.WithStaticURL(fake.URL))
	disc.Scan(context.Background())
	if len(disc.GetServers()) != 1 {
		t.Fatalf("fake server not discovered")
	}

	allowed := []string{replyOrigin}
	s := &Server{
		auth:        &authGate{token: replyToken, enabled: true, allowedOrigins: allowed},
		hosts:       deriveHosts(nil, allowed),
		ocDiscovery: disc,
		ocManager:   opencode.NewManager(disc),
	}

	fakeURL, err := url.Parse(fake.URL)
	if err != nil {
		t.Fatal(err)
	}
	decoyURL, err := url.Parse(decoy.URL)
	if err != nil {
		t.Fatal(err)
	}
	fakePort := fakeURL.Port()
	otherPort := "1"
	if fakePort == "1" {
		otherPort = "2"
	}

	do := func(method, server, action string) int {
		p := "/api/opencode/session/" + url.PathEscape(server) + "/s1"
		var body string
		if action != "" {
			p += "/" + action
		}
		if action == "send" {
			body = "input=hi"
		}
		req := replyRequest(method, p, body)
		if body != "" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		return doReply(t, s, req).Code
	}

	accepted := []struct{ name, method, action string }{
		{"details", http.MethodGet, ""},
		{"abort", http.MethodPost, "abort"},
		{"send", http.MethodPost, "send"},
	}
	for _, tc := range accepted {
		t.Run("discovered "+tc.name, func(t *testing.T) {
			if code := do(tc.method, fake.URL, tc.action); code != http.StatusOK {
				t.Fatalf("status %d, want 200", code)
			}
		})
	}

	refused := map[string]string{
		"decoy":              decoy.URL,
		"different port":     "http://" + fakeURL.Hostname() + ":" + otherPort,
		"userinfo":           "http://user@" + fakeURL.Host,
		"userinfo swap":      "http://" + fakeURL.Host + "@" + decoyURL.Host,
		"path":               fake.URL + "/%2e%2e/x",
		"file":               "file:///etc/passwd",
		"gopher":             "gopher://" + decoyURL.Host,
		"decoy with path":    decoy.URL + "/x",
		"decoy fragment @":   "http://" + decoyURL.Host + "#@" + fakeURL.Host,
		"discovered no host": strings.Replace(fake.URL, fakeURL.Host, "", 1),
	}
	for name, server := range refused {
		for _, tc := range accepted {
			t.Run("refused "+name+" "+tc.name, func(t *testing.T) {
				if code := do(tc.method, server, tc.action); code != http.StatusNotFound {
					t.Fatalf("status %d, want 404", code)
				}
			})
		}
	}
	if n := decoyHits.Load(); n != 0 {
		t.Fatalf("decoy received %d requests, want 0", n)
	}
}
