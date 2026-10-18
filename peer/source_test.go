package peer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noamsto/houston/runs"
)

const testToken = "synthetic-token-0123456789abcdef"

// sseServer serves a scripted runs stream. fn gets the 1-based connection
// number; stop closes when the test ends, so handlers that hold return.
type sseServer struct {
	*httptest.Server
	hits atomic.Int32
	stop chan struct{}

	mu    sync.Mutex
	auths []string
	query []string
}

func newSSEServer(t *testing.T, fn func(n int, w http.ResponseWriter, r *http.Request, s *sseServer)) *sseServer {
	t.Helper()
	s := &sseServer{stop: make(chan struct{})}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(s.hits.Add(1))
		s.mu.Lock()
		s.auths = append(s.auths, r.Header.Get("Authorization"))
		s.query = append(s.query, r.URL.RawQuery)
		s.mu.Unlock()
		fn(n, w, r, s)
	}))
	t.Cleanup(s.Close)
	t.Cleanup(func() { close(s.stop) })
	return s
}

func (s *sseServer) open(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
}

func (s *sseServer) frame(w http.ResponseWriter, event, data string) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	w.(http.Flusher).Flush()
}

func (s *sseServer) hold(r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-s.stop:
	}
}

func tokenFile(t *testing.T, tok string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "b.token")
	if err := os.WriteFile(p, []byte(tok), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func fastOpts() Options {
	return Options{MinBackoff: 5 * time.Millisecond, MaxBackoff: 20 * time.Millisecond, Silence: 5 * time.Second}
}

// start runs src until the test ends and returns its output.
func start(t *testing.T, src *Source) <-chan runs.Delta {
	t.Helper()
	out := make(chan runs.Delta, 1024)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- src.Run(ctx, out) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Run returned %v, want context.Canceled", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	return out
}

func recv(t *testing.T, out <-chan runs.Delta) runs.Delta {
	t.Helper()
	select {
	case d := <-out:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("no delta")
		return runs.Delta{}
	}
}

func recvN(t *testing.T, out <-chan runs.Delta, n int) map[string]runs.Delta {
	t.Helper()
	got := map[string]runs.Delta{}
	for range n {
		d := recv(t, out)
		got[d.Key] = d
	}
	return got
}

func noDelta(t *testing.T, out <-chan runs.Delta) {
	t.Helper()
	select {
	case d := <-out:
		t.Fatalf("unexpected delta %+v", d)
	default:
	}
}

func waitHealth(t *testing.T, src *Source, state, class string) Health {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h := src.Health()
		if h.State == state && h.Error == class {
			return h
		}
		if time.Now().After(deadline) {
			t.Fatalf("health = %+v, want %s/%q", h, state, class)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func newSource(url, tokenPath string, opts Options) *Source {
	return New(Config{Name: "b", URL: url, TokenFile: tokenPath}, opts)
}

func TestSourceSanitisesAndFollowsUpdates(t *testing.T) {
	snap := `[{"id":"pane-1","agent":"claude","state":"running","tmux":{"session":"s","window":1,"pane_id":"%3"},` +
		`"caps":{"terminal":true,"reply":true,"kill":true,"chat":true},"stale":true,"draft_key":"dk","attention":"stuck","attention_note":"n"},` +
		`{"id":"sess-2","agent":"pi","state":"idle"}]`
	srv := newSSEServer(t, func(n int, w http.ResponseWriter, r *http.Request, s *sseServer) {
		s.open(w)
		s.frame(w, "snapshot", snap)
		s.frame(w, "ping", "")
		s.frame(w, "update", `{"id":"pane-1","agent":"claude","state":"blocked","caps":{"terminal":true}}`)
		s.frame(w, "update", `{"id":"sess-2","removed":true}`)
		s.frame(w, "update", `{"id":"sess-9","removed":true}`)
		s.hold(r)
	})
	src := newSource(srv.URL, tokenFile(t, testToken+"\n"), fastOpts())
	if src.client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("transport has a Proxy func; peer requests must never go through HTTP_PROXY")
	}
	out := start(t, src)

	first := recvN(t, out, 2)
	for _, id := range []string{"pane-1", "sess-2"} {
		d, ok := first[runs.PeerKey("b", id)]
		if !ok {
			t.Fatalf("no delta for %s in %v", id, first)
		}
		r := d.Run
		if d.Source != runs.PeerLayer || d.Gone || r.Host != "b" || r.ID != "" || r.Tmux != nil || r.Caps != (runs.Caps{}) || r.Stale || r.DraftKey != "" {
			t.Errorf("%s: delta %+v not sanitised", id, d)
		}
	}
	if r := first[runs.PeerKey("b", "pane-1")].Run; r.State != runs.StateRunning || r.Attention != "stuck" || r.AttentionNote != "n" || r.Agent != "claude" {
		t.Errorf("pane-1 lost peer fields: %+v", r)
	}

	upd := recv(t, out)
	if upd.Key != runs.PeerKey("b", "pane-1") || upd.Run.State != runs.StateBlocked || upd.Run.Caps != (runs.Caps{}) {
		t.Errorf("update delta = %+v", upd)
	}
	gone := recv(t, out)
	if gone.Key != runs.PeerKey("b", "sess-2") || !gone.Gone {
		t.Errorf("removal delta = %+v, want Gone for sess-2", gone)
	}

	h := waitHealth(t, src, StateConnected, "")
	if h.Name != "b" || h.LastSeen == 0 {
		t.Errorf("health = %+v", h)
	}
	time.Sleep(20 * time.Millisecond)
	noDelta(t, out) // the unknown id's removal is ignored

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.auths[0] != "Bearer "+testToken {
		t.Errorf("Authorization = %q", srv.auths[0])
	}
	if srv.query[0] != "scope=local" {
		t.Errorf("query = %q, want scope=local", srv.query[0])
	}
}

func TestSourceNeverFollowsRedirect(t *testing.T) {
	target := newSSEServer(t, func(n int, w http.ResponseWriter, r *http.Request, s *sseServer) {
		s.open(w)
		s.frame(w, "snapshot", "[]")
		s.hold(r)
	})
	srv := newSSEServer(t, func(n int, w http.ResponseWriter, r *http.Request, s *sseServer) {
		http.Redirect(w, r, target.URL+"/api/runs/stream?scope=local", http.StatusFound)
	})
	src := newSource(srv.URL, tokenFile(t, testToken), fastOpts())
	start(t, src)
	waitHealth(t, src, StateUnreachable, ClassRedirect)
	for srv.hits.Load() < 3 {
		time.Sleep(2 * time.Millisecond)
	}
	if n := target.hits.Load(); n != 0 {
		t.Fatalf("redirect target received %d requests", n)
	}
}

func TestSourceStaleOnDropThenSnapshotReplaces(t *testing.T) {
	end1 := make(chan struct{})
	release2 := make(chan struct{})
	srv := newSSEServer(t, func(n int, w http.ResponseWriter, r *http.Request, s *sseServer) {
		switch n {
		case 1:
			s.open(w)
			s.frame(w, "snapshot", `[{"id":"pane-1","agent":"claude","state":"running"},{"id":"pane-2","agent":"claude","state":"idle"}]`)
			<-end1
		case 2:
			<-release2
			s.open(w)
			s.frame(w, "snapshot", `[{"id":"pane-2","agent":"claude","state":"running"}]`)
			s.hold(r)
		default:
			s.hold(r)
		}
	})
	src := newSource(srv.URL, tokenFile(t, testToken), fastOpts())
	out := start(t, src)

	recvN(t, out, 2)
	waitHealth(t, src, StateConnected, "")

	close(end1)
	stale := recvN(t, out, 2)
	for _, id := range []string{"pane-1", "pane-2"} {
		d := stale[runs.PeerKey("b", id)]
		if d.Gone || !d.Run.Stale || d.Run.Host != "b" {
			t.Errorf("%s after drop: %+v, want listed and stale", id, d)
		}
	}
	waitHealth(t, src, StateUnreachable, ClassClosed)

	close(release2)
	back := recvN(t, out, 2)
	if d := back[runs.PeerKey("b", "pane-1")]; !d.Gone {
		t.Errorf("pane-1 after reconnect: %+v, want Gone", d)
	}
	if d := back[runs.PeerKey("b", "pane-2")]; d.Gone || d.Run.Stale || d.Run.State != runs.StateRunning {
		t.Errorf("pane-2 after reconnect: %+v, want fresh and running", d)
	}
	waitHealth(t, src, StateConnected, "")
}

func TestSourceSilenceReconnects(t *testing.T) {
	srv := newSSEServer(t, func(n int, w http.ResponseWriter, r *http.Request, s *sseServer) {
		if n == 1 {
			s.open(w)
			s.frame(w, "snapshot", `[{"id":"pane-1","agent":"claude","state":"running"}]`)
		}
		s.hold(r)
	})
	opts := fastOpts()
	opts.Silence = 100 * time.Millisecond
	src := newSource(srv.URL, tokenFile(t, testToken), opts)
	out := start(t, src)

	if d := recv(t, out); d.Run.Stale {
		t.Fatalf("first delta stale: %+v", d)
	}
	if d := recv(t, out); !d.Run.Stale || d.Gone {
		t.Fatalf("after silence: %+v, want stale", d)
	}
	waitHealth(t, src, StateUnreachable, ClassSilent)
	for srv.hits.Load() < 2 {
		time.Sleep(2 * time.Millisecond)
	}
}

func TestSourceNullSnapshotEmptiesSet(t *testing.T) {
	srv := newSSEServer(t, func(n int, w http.ResponseWriter, r *http.Request, s *sseServer) {
		s.open(w)
		if n == 1 {
			s.frame(w, "snapshot", `[{"id":"pane-1","agent":"claude"},{"id":"pane-2","agent":"claude"}]`)
			return
		}
		s.frame(w, "snapshot", "null")
		s.hold(r)
	})
	src := newSource(srv.URL, tokenFile(t, testToken), fastOpts())
	out := start(t, src)

	recvN(t, out, 2)
	recvN(t, out, 2) // stale re-emits after the first stream ends
	gone := recvN(t, out, 2)
	for _, id := range []string{"pane-1", "pane-2"} {
		if !gone[runs.PeerKey("b", id)].Gone {
			t.Errorf("%s not Gone after a null snapshot: %v", id, gone)
		}
	}
	waitHealth(t, src, StateConnected, "")
}

func TestSourceParsesLargeSnapshot(t *testing.T) {
	const n = 200
	list := make([]runs.Run, n)
	for i := range list {
		list[i] = runs.Run{ID: fmt.Sprintf("pane-%d", i), Agent: "claude", Activity: runs.Activity{Task: strings.Repeat("x", 400)}}
	}
	b, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) <= 64<<10 {
		t.Fatalf("snapshot only %d bytes; the test needs more than 64 KiB", len(b))
	}
	srv := newSSEServer(t, func(_ int, w http.ResponseWriter, r *http.Request, s *sseServer) {
		s.open(w)
		s.frame(w, "snapshot", string(b))
		s.hold(r)
	})
	src := newSource(srv.URL, tokenFile(t, testToken), fastOpts())
	out := start(t, src)
	if got := recvN(t, out, n); len(got) != n {
		t.Fatalf("got %d distinct runs, want %d", len(got), n)
	}
	waitHealth(t, src, StateConnected, "")
}

func TestSourceDropsForeignHostAndBadID(t *testing.T) {
	srv := newSSEServer(t, func(_ int, w http.ResponseWriter, r *http.Request, s *sseServer) {
		s.open(w)
		s.frame(w, "snapshot", `[{"id":"pane-1","host":"c","agent":"claude"},{"id":"a/b","agent":"claude"},{"id":"","agent":"claude"},{"id":"pane-2","agent":"claude"}]`)
		s.frame(w, "update", `{"id":"peer-c.pane-1","host":"c","agent":"claude"}`)
		s.frame(w, "update", `{"id":"pane-2","agent":"pi"}`)
		s.hold(r)
	})
	src := newSource(srv.URL, tokenFile(t, testToken), fastOpts())
	out := start(t, src)
	if d := recv(t, out); d.Key != runs.PeerKey("b", "pane-2") {
		t.Fatalf("first delta = %+v, want pane-2 only", d)
	}
	if d := recv(t, out); d.Key != runs.PeerKey("b", "pane-2") || d.Run.Agent != "pi" {
		t.Fatalf("second delta = %+v, want pane-2's update", d)
	}
}

func TestSourceRefusesSelf(t *testing.T) {
	srv := newSSEServer(t, func(_ int, w http.ResponseWriter, r *http.Request, s *sseServer) {
		w.Header().Set(InstanceHeader, "inst-a")
		s.open(w)
		s.frame(w, "snapshot", `[{"id":"pane-1","agent":"claude"}]`)
		s.hold(r)
	})
	opts := fastOpts()
	opts.Self = "inst-a"
	src := newSource(srv.URL, tokenFile(t, testToken), opts)
	out := start(t, src)
	waitHealth(t, src, StateUnreachable, ClassSelf)
	noDelta(t, out)
}

func TestSourceTokenProblemsNeverDial(t *testing.T) {
	cases := map[string]string{
		"missing":     "",
		"empty":       " \n",
		"inner space": "abc def",
		"control":     "abc\x01def",
		"non-ascii":   "abcé",
		"too large":   strings.Repeat("a", maxToken+1),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			srv := newSSEServer(t, func(_ int, w http.ResponseWriter, r *http.Request, s *sseServer) {
				s.open(w)
				s.hold(r)
			})
			path := filepath.Join(t.TempDir(), "absent.token")
			if name != "missing" {
				path = tokenFile(t, content)
			}
			src := newSource(srv.URL, path, fastOpts())
			start(t, src)
			waitHealth(t, src, StateUnreachable, ClassToken)
			time.Sleep(30 * time.Millisecond) // several retries
			if n := srv.hits.Load(); n != 0 {
				t.Fatalf("server received %d requests with a bad token file", n)
			}
		})
	}
}

func TestReadTokenTrims(t *testing.T) {
	got, err := readToken(tokenFile(t, "  "+testToken+"\r\n"))
	if err != nil || got != testToken {
		t.Fatalf("readToken = %q, %v", got, err)
	}
	if _, err := readToken(tokenFile(t, "x"+strings.Repeat("y", maxToken-1))); err != nil {
		t.Fatalf("a 4 KiB token is allowed: %v", err)
	}
}

func TestSourceFailureClasses(t *testing.T) {
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refusedURL := "http://" + closed.Addr().String()
	closed.Close()

	cases := []struct {
		name    string
		handler http.HandlerFunc
		class   string
	}{
		{"401", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "unauthorized", http.StatusUnauthorized) }, ClassAuth},
		{"403", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "forbidden", http.StatusForbidden) }, ClassAuth},
		{"500", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) }, ClassHTTP},
		{"text/plain", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "event: snapshot\ndata: []\n\n")
		}, ClassProtocol},
		{"bad json", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			fmt.Fprint(w, "event: snapshot\ndata: [{\n\n")
		}, ClassProtocol},
		{"eof before snapshot", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, ": hi\n\n")
		}, ClassClosed},
		{"refused", nil, ClassRefused},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := refusedURL
			if tc.handler != nil {
				srv := httptest.NewServer(tc.handler)
				t.Cleanup(srv.Close)
				url = srv.URL
			}
			src := newSource(url, tokenFile(t, testToken), fastOpts())
			out := start(t, src)
			h := waitHealth(t, src, StateUnreachable, tc.class)
			noDelta(t, out)
			b, err := json.Marshal(h)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), testToken) {
				t.Fatalf("health JSON carries the token: %s", b)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&net.DNSError{Err: "no such host", Name: "x", IsNotFound: true}, ClassDNS},
		{context.DeadlineExceeded, ClassTimeout},
		{fmt.Errorf("read: %w", os.ErrDeadlineExceeded), ClassTimeout},
		{errors.New("something"), ClassOther},
	}
	for _, tc := range cases {
		if got := classify(tc.err); got != tc.want {
			t.Errorf("classify(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestJitterBounds(t *testing.T) {
	for range 1000 {
		if d := jitter(time.Second); d < 800*time.Millisecond || d > 1200*time.Millisecond {
			t.Fatalf("jitter(1s) = %v, want within ±20%%", d)
		}
	}
}

func TestSourcePlainHTTPHopCheck(t *testing.T) {
	cases := []struct {
		name          string
		local, remote string
		ok            bool
	}{
		{"tailnet both ends", "100.101.1.1", "100.64.0.7", true},
		{"tailnet remote, LAN local", "192.168.1.2", "100.64.0.7", false},
		{"public remote", "100.101.1.1", "203.0.113.9", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newSSEServer(t, func(_ int, w http.ResponseWriter, r *http.Request, s *sseServer) {
				s.open(w)
				s.frame(w, "snapshot", `[{"id":"pane-1","agent":"claude"}]`)
				s.hold(r)
			})
			src := newSource(srv.URL, tokenFile(t, testToken), fastOpts())
			// The connection really goes to loopback; the check sees the
			// addresses a route over this host's network would have.
			src.hopOK = func(_, _ net.Addr) bool {
				return plainHopOK(&net.TCPAddr{IP: net.ParseIP(tc.local)}, &net.TCPAddr{IP: net.ParseIP(tc.remote)})
			}
			out := start(t, src)
			if tc.ok {
				recv(t, out)
				waitHealth(t, src, StateConnected, "")
				return
			}
			waitHealth(t, src, StateUnreachable, ClassInsecure)
			time.Sleep(30 * time.Millisecond) // several retries
			if n := srv.hits.Load(); n != 0 {
				t.Fatalf("server received %d requests over a refused hop", n)
			}
			noDelta(t, out)
		})
	}
}

func TestSourceCapsRunsPerPeer(t *testing.T) {
	list := make([]runs.Run, maxPeerRuns+5)
	for i := range list {
		list[i] = runs.Run{ID: fmt.Sprintf("pane-%d", i), Agent: "claude"}
	}
	b, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	srv := newSSEServer(t, func(_ int, w http.ResponseWriter, r *http.Request, s *sseServer) {
		s.open(w)
		s.frame(w, "snapshot", string(b))
		s.frame(w, "update", `{"id":"fresh-1","agent":"claude"}`)
		s.frame(w, "update", `{"id":"fresh-2","agent":"claude"}`)
		s.frame(w, "update", `{"id":"pane-0","agent":"pi"}`)
		s.hold(r)
	})
	src := newSource(srv.URL, tokenFile(t, testToken), fastOpts())
	out := start(t, src)

	got := recvN(t, out, maxPeerRuns)
	if len(got) != maxPeerRuns {
		t.Fatalf("got %d distinct runs, want %d", len(got), maxPeerRuns)
	}
	for i := maxPeerRuns; i < len(list); i++ {
		if _, ok := got[runs.PeerKey("b", fmt.Sprintf("pane-%d", i))]; ok {
			t.Errorf("pane-%d past the cap was emitted", i)
		}
	}
	if d := recv(t, out); d.Key != runs.PeerKey("b", "pane-0") || d.Run.Agent != "pi" {
		t.Fatalf("delta after the cap = %+v, want pane-0's update (new ids dropped)", d)
	}
	time.Sleep(20 * time.Millisecond)
	noDelta(t, out)
}

func TestSourceAcceptsLongIDs(t *testing.T) {
	long := "crew-" + strings.Repeat("A", 595)
	tooLong := strings.Repeat("B", 1025)
	srv := newSSEServer(t, func(_ int, w http.ResponseWriter, r *http.Request, s *sseServer) {
		s.open(w)
		s.frame(w, "snapshot", `[{"id":"`+long+`","agent":"claude"},{"id":"`+tooLong+`","agent":"claude"},{"id":"pane-2","agent":"claude"}]`)
		s.hold(r)
	})
	src := newSource(srv.URL, tokenFile(t, testToken), fastOpts())
	out := start(t, src)
	if d := recv(t, out); d.Key != runs.PeerKey("b", long) {
		t.Fatalf("first delta = %q, want the 600-char id", d.Key)
	}
	if d := recv(t, out); d.Key != runs.PeerKey("b", "pane-2") {
		t.Fatalf("second delta = %q, want pane-2 (1025-char id dropped)", d.Key)
	}
}
