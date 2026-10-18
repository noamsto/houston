package peer

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// recorder is a peer that records the last request it saw and answers with fn.
type recorder struct {
	*httptest.Server
	hits atomic.Int32
	req  atomic.Pointer[http.Request]
	body atomic.Pointer[string]
}

func newRecorder(t *testing.T, fn http.HandlerFunc) *recorder {
	t.Helper()
	rec := &recorder{}
	rec.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		s := string(b)
		rec.body.Store(&s)
		rec.req.Store(r)
		if fn != nil {
			fn(w, r)
		}
	}))
	t.Cleanup(rec.Close)
	return rec
}

// dialTerminal drops the handshake response; the failure cases here assert
// only on err and on what the peer saw.
func dialTerminal(src *Source, id string) (*websocket.Conn, error) {
	conn, resp, err := src.DialTerminal(context.Background(), id)
	if resp != nil {
		resp.Body.Close()
	}
	return conn, err
}

func TestDoSendsBearerPathQueryAndOnlyGivenHeader(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	src := newSource(rec.URL, tokenFile(t, testToken+"\n"), fastOpts())

	q := url.Values{"limit": {"5"}, "before": {"e.9"}}
	h := http.Header{"Content-Type": {"application/json"}}
	resp, err := src.Do(context.Background(), http.MethodPost, "pane-1", "chat", q, h, []byte(`{"a":1}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	r := rec.req.Load()
	if r.Method != http.MethodPost || r.URL.Path != "/api/runs/pane-1/chat" || r.URL.RawQuery != q.Encode() {
		t.Errorf("request = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
		t.Errorf("Authorization = %q", got)
	}
	if got := r.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := *rec.body.Load(); got != `{"a":1}` {
		t.Errorf("body = %q", got)
	}
	for k := range r.Header {
		switch k {
		case "Authorization", "Content-Type", "Content-Length", "User-Agent", "Accept-Encoding":
		default:
			t.Errorf("unexpected header %q reached the peer", k)
		}
	}
}

func TestDoGetHasNoQueryOrBody(t *testing.T) {
	rec := newRecorder(t, nil)
	src := newSource(rec.URL, tokenFile(t, testToken), fastOpts())
	resp, err := src.Do(context.Background(), http.MethodGet, "pane-1", "prompt", nil, nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	r := rec.req.Load()
	if r.URL.RawQuery != "" || r.URL.Path != "/api/runs/pane-1/prompt" || *rec.body.Load() != "" {
		t.Errorf("request = %s?%s body %q", r.URL.Path, r.URL.RawQuery, *rec.body.Load())
	}
}

func TestDoReturnsRedirectWithoutFollowing(t *testing.T) {
	target := newRecorder(t, nil)
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/x", http.StatusFound)
	})
	src := newSource(rec.URL, tokenFile(t, testToken), fastOpts())
	resp, err := src.Do(context.Background(), http.MethodGet, "pane-1", "prompt", nil, nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want 302 returned as-is", resp.StatusCode)
	}
	if n := target.hits.Load(); n != 0 {
		t.Errorf("redirect target received %d requests", n)
	}
}

func TestForwardRefusesInvalidIDsWithoutRequest(t *testing.T) {
	rec := newRecorder(t, nil)
	src := newSource(rec.URL, tokenFile(t, testToken), fastOpts())
	for _, id := range []string{"", "../x", "a/b", "a b", "a?b", "a%2fb", strings.Repeat("a", maxID+1)} {
		if resp, err := src.Do(context.Background(), http.MethodGet, id, "chat", nil, nil, nil, 0); err == nil {
			resp.Body.Close()
			t.Errorf("Do(%q) succeeded", id)
		}
		if conn, err := dialTerminal(src, id); err == nil {
			conn.Close()
			t.Errorf("DialTerminal(%q) succeeded", id)
		}
	}
	if n := rec.hits.Load(); n != 0 {
		t.Fatalf("peer received %d requests for invalid ids", n)
	}
}

func TestForwardUnreadableTokenSendsNothing(t *testing.T) {
	rec := newRecorder(t, nil)
	path := tokenFile(t, testToken)
	src := newSource(rec.URL, path, fastOpts())
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if resp, err := src.Do(context.Background(), http.MethodGet, "pane-1", "chat", nil, nil, nil, 0); err == nil {
		resp.Body.Close()
		t.Error("Do succeeded without a token")
	} else if strings.Contains(err.Error(), testToken) {
		t.Errorf("error leaks the token: %v", err)
	}
	if conn, err := dialTerminal(src, "pane-1"); err == nil {
		conn.Close()
		t.Error("DialTerminal succeeded without a token")
	}
	if n := rec.hits.Load(); n != 0 {
		t.Fatalf("peer received %d requests without a token", n)
	}
}

func TestDialTerminalCarriesBearerAndNoOrigin(t *testing.T) {
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = c.WriteMessage(websocket.TextMessage, []byte("hi"))
		c.Close()
	})
	src := newSource(rec.URL, tokenFile(t, testToken), fastOpts())
	conn, resp, err := src.DialTerminal(context.Background(), "pane-1")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Errorf("handshake status = %d", resp.StatusCode)
	}
	if _, msg, err := conn.ReadMessage(); err != nil || string(msg) != "hi" {
		t.Errorf("read = %q, %v", msg, err)
	}
	r := rec.req.Load()
	if r.URL.Path != "/api/runs/pane-1/terminal" {
		t.Errorf("path = %q", r.URL.Path)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
		t.Errorf("Authorization = %q", got)
	}
	if got := r.Header.Get("Origin"); got != "" {
		t.Errorf("Origin = %q, want none", got)
	}
}

func TestDialTerminalRefusesInsecureHop(t *testing.T) {
	rec := newRecorder(t, nil)
	src := newSource(rec.URL, tokenFile(t, testToken), fastOpts())
	src.hopOK = func(_, _ net.Addr) bool { return false }
	if conn, err := dialTerminal(src, "pane-1"); err == nil {
		conn.Close()
		t.Fatal("DialTerminal succeeded over a refused hop")
	}
	if n := rec.hits.Load(); n != 0 {
		t.Fatalf("peer received %d requests over a refused hop", n)
	}
}

func TestDoAPISendsBearerAndPath(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	src := newSource(rec.URL, tokenFile(t, testToken), fastOpts())
	h := http.Header{"Content-Type": {"application/json"}}
	resp, err := src.DoAPI(context.Background(), http.MethodPost, "dispatch/dispatcher", nil, h, []byte(`{"a":1}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	r := rec.req.Load()
	if r.Method != http.MethodPost || r.URL.Path != "/api/dispatch/dispatcher" {
		t.Errorf("request = %s %s", r.Method, r.URL.Path)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
		t.Errorf("Authorization = %q", got)
	}
	if got := r.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := *rec.body.Load(); got != `{"a":1}` {
		t.Errorf("body = %q", got)
	}
	for _, k := range []string{"Cookie", "Origin"} {
		if got := r.Header.Get(k); got != "" {
			t.Errorf("%s = %q reached the peer", k, got)
		}
	}
}

func TestDoAPIRefusesOtherPaths(t *testing.T) {
	rec := newRecorder(t, nil)
	src := newSource(rec.URL, tokenFile(t, testToken), fastOpts())
	for _, p := range []string{"runs", "dispatch/x", "../runs", "repos/../runs", "", "dispatch/", "runs/pane-1/input"} {
		resp, err := src.DoAPI(context.Background(), http.MethodGet, p, nil, nil, nil, 0)
		if err == nil {
			resp.Body.Close()
		}
		if !errors.Is(err, errBadPath) {
			t.Errorf("DoAPI(%q) err = %v, want errBadPath", p, err)
		}
	}
	if n := rec.hits.Load(); n != 0 {
		t.Fatalf("peer received %d requests for refused paths", n)
	}
}

func TestDoWaitBoundsTheRequest(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	})
	src := newSource(rec.URL, tokenFile(t, testToken), fastOpts())

	resp, err := src.DoAPI(context.Background(), http.MethodGet, "repos", nil, nil, nil, 100*time.Millisecond)
	if err == nil {
		resp.Body.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("short wait err = %v, want deadline exceeded", err)
	}

	resp, err = src.DoAPI(context.Background(), http.MethodGet, "repos", nil, nil, nil, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if b, err := io.ReadAll(resp.Body); err != nil || string(b) != "late" {
		t.Errorf("body = %q, %v", b, err)
	}
}

func TestDoWaitKeepsTheHopCheck(t *testing.T) {
	rec := newRecorder(t, nil)
	src := newSource(rec.URL, tokenFile(t, testToken), fastOpts())
	src.hopOK = func(_, _ net.Addr) bool { return false }
	if resp, err := src.DoAPI(context.Background(), http.MethodGet, "repos", nil, nil, nil, time.Second); err == nil {
		resp.Body.Close()
		t.Error("DoAPI succeeded over a refused hop")
	} else if !errors.Is(err, errInsecureHop) {
		t.Errorf("DoAPI err = %v, want errInsecureHop", err)
	}
	if resp, err := src.Do(context.Background(), http.MethodGet, "pane-1", "chat", nil, nil, nil, time.Second); err == nil {
		resp.Body.Close()
		t.Error("Do succeeded over a refused hop")
	} else if !errors.Is(err, errInsecureHop) {
		t.Errorf("Do err = %v, want errInsecureHop", err)
	}
	if n := rec.hits.Load(); n != 0 {
		t.Fatalf("peer received %d requests over a refused hop", n)
	}
}
