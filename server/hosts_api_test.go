package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noamsto/houston/peer"
	"github.com/noamsto/houston/runs"
)

const hostsTestPeerToken = "peer-secret-token"

type hostsBody struct {
	Hosts []peer.Health `json:"hosts"`
}

func getHosts(t *testing.T, s *Server, token string) (*httptest.ResponseRecorder, hostsBody) {
	t.Helper()
	req := httptest.NewRequest("GET", "http://127.0.0.1:9090/api/hosts", nil)
	req.Host = "127.0.0.1:9090"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var body hostsBody
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v — %s", err, rec.Body.String())
		}
	}
	return rec, body
}

func writeTokenFile(t *testing.T, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "peer.token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// startSource runs src until test cleanup, applying its deltas to reg.
func startSource(t *testing.T, src *peer.Source, reg *runs.Registry) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	deltas := make(chan runs.Delta)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for d := range deltas {
			reg.Apply(d)
		}
	}()
	go func() {
		defer wg.Done()
		defer close(deltas)
		_ = src.Run(ctx, deltas)
	}()
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
}

func waitHealth(t *testing.T, s *Server, want func(peer.Health) bool) peer.Health {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, body := getHosts(t, s, "secret")
		if len(body.Hosts) == 2 && want(body.Hosts[1]) {
			return body.Hosts[1]
		}
		if time.Now().After(deadline) {
			t.Fatalf("peer health never matched; last %+v", body.Hosts)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHostsRouteIsBehindTheAuthGate(t *testing.T) {
	s := newAuthTestServer("secret")
	if rec, _ := getHosts(t, s, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
}

func TestHostsListsLocalOnlyWithoutPeers(t *testing.T) {
	s := newAuthTestServer("secret")
	rec, body := getHosts(t, s, "secret")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if len(body.Hosts) != 1 || body.Hosts[0] != (peer.Health{Name: "local", Local: true, State: peer.StateConnected}) {
		t.Fatalf("hosts %+v, want only the connected local host", body.Hosts)
	}
}

func TestHostsReportsPeerHealthTransitions(t *testing.T) {
	release := make(chan struct{})
	peerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: snapshot\ndata: []\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(peerSrv.Close)
	t.Cleanup(func() { close(release) })

	src := peer.New(
		peer.Config{Name: "b", URL: peerSrv.URL, TokenFile: writeTokenFile(t, hostsTestPeerToken)},
		peer.Options{MinBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond},
	)
	s := newAuthTestServer("secret")
	s.peers = []*peer.Source{src}

	rec, body := getHosts(t, s, "secret")
	if rec.Code != http.StatusOK || len(body.Hosts) != 2 {
		t.Fatalf("status %d hosts %+v, want 200 and two hosts", rec.Code, body.Hosts)
	}
	if !body.Hosts[0].Local || body.Hosts[0].Name != "local" {
		t.Fatalf("first host %+v, want local", body.Hosts[0])
	}
	if got := body.Hosts[1]; got.Name != "b" || got.Local || got.State != peer.StateConnecting {
		t.Fatalf("peer before Run %+v, want b connecting", got)
	}

	startSource(t, src, s.runs)
	got := waitHealth(t, s, func(h peer.Health) bool { return h.State == peer.StateConnected })
	if got.LastSeen <= 0 || got.Error != "" {
		t.Fatalf("connected peer %+v, want last_seen and no error", got)
	}

	peerSrv.CloseClientConnections()
	peerSrv.Close()
	got = waitHealth(t, s, func(h peer.Health) bool { return h.State == peer.StateUnreachable })
	if got.Error == "" {
		t.Fatalf("unreachable peer %+v, want an error class", got)
	}

	rec, _ = getHosts(t, s, "secret")
	if strings.Contains(rec.Body.String(), hostsTestPeerToken) {
		t.Fatalf("hosts body leaks the peer token: %s", rec.Body.String())
	}
}
