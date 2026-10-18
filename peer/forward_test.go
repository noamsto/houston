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
	"strconv"
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
	resp, err := src.Do(context.Background(), http.MethodPost, "pane-1", "chat", q, h, []byte(`{"a":1}`), Fast)
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
	resp, err := src.Do(context.Background(), http.MethodGet, "pane-1", "prompt", nil, nil, nil, Fast)
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
	// Long and Detached use the slow client, which must keep the same refusals.
	for _, mode := range []Mode{Fast, Long, Detached} {
		t.Run(strconv.Itoa(int(mode)), func(t *testing.T) {
			target := newRecorder(t, nil)
			rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL+"/x", http.StatusFound)
			})
			src := newSource(rec.URL, tokenFile(t, testToken), fastOpts())
			resp, err := src.Do(context.Background(), http.MethodGet, "pane-1", "prompt", nil, nil, nil, mode)
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
		})
	}
}

func TestSlowClientUsesNoProxyAndRefusesRedirects(t *testing.T) {
	src := newSource("http://127.0.0.1:1", tokenFile(t, testToken), fastOpts())
	tr, ok := src.slow.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("slow transport is %T, want *http.Transport", src.slow.Transport)
	}
	if tr.Proxy != nil {
		t.Error("slow transport has a proxy func")
	}
	if src.slow.CheckRedirect == nil {
		t.Error("slow client follows redirects")
	}
}

func TestForwardRefusesInvalidIDsWithoutRequest(t *testing.T) {
	rec := newRecorder(t, nil)
	src := newSource(rec.URL, tokenFile(t, testToken), fastOpts())
	for _, id := range []string{"", "../x", "a/b", "a b", "a?b", "a%2fb", strings.Repeat("a", maxID+1)} {
		if resp, err := src.Do(context.Background(), http.MethodGet, id, "chat", nil, nil, nil, Fast); err == nil {
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
	if resp, err := src.Do(context.Background(), http.MethodGet, "pane-1", "chat", nil, nil, nil, Fast); err == nil {
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
	resp, err := src.DoAPI(context.Background(), http.MethodPost, "dispatch/dispatcher", nil, h, []byte(`{"a":1}`), Fast)
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
		resp, err := src.DoAPI(context.Background(), http.MethodGet, p, nil, nil, nil, Fast)
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

func TestLongHasNoHeaderTimeout(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	})
	opts := fastOpts()
	opts.HeaderTimeout = 50 * time.Millisecond
	src := newSource(rec.URL, tokenFile(t, testToken), opts)

	for _, mode := range []Mode{Long, Detached} {
		resp, err := src.DoAPI(context.Background(), http.MethodGet, "repos", nil, nil, nil, mode)
		if err != nil {
			t.Fatalf("mode %d: %v", mode, err)
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || string(b) != "late" {
			t.Errorf("mode %d body = %q, %v", mode, b, err)
		}
	}
}

func TestFastHeaderTimeoutIsNotUnknown(t *testing.T) {
	rec := newRecorder(t, stall)
	opts := fastOpts()
	opts.HeaderTimeout = 50 * time.Millisecond
	src := newSource(rec.URL, tokenFile(t, testToken), opts)

	resp, err := src.Do(context.Background(), http.MethodPost, "pane-1", "input", nil, nil, []byte(`{}`), Fast)
	if err == nil {
		resp.Body.Close()
		t.Fatal("Fast waited past the header timeout")
	}
	if errors.Is(err, ErrOutcomeUnknown) {
		t.Errorf("err = %v, want no unknown outcome from Fast", err)
	}
	if n := rec.hits.Load(); n != 1 {
		t.Errorf("peer received %d requests, want 1", n)
	}
}

// stall holds the request until the client goes away.
func stall(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }

// TestDetachedDialTimeoutIsNotUnknown: nothing was written, so nothing can
// have happened. The error still matches context.DeadlineExceeded, which is
// why that test cannot tell an unknown outcome apart.
func TestDetachedDialTimeoutIsNotUnknown(t *testing.T) {
	rec := newRecorder(t, nil)
	opts := fastOpts()
	opts.DialTimeout = time.Nanosecond
	src := newSource(rec.URL, tokenFile(t, testToken), opts)

	resp, err := src.DoAPI(context.Background(), http.MethodPost, "dispatch", nil, nil, []byte(`{}`), Detached)
	if err == nil {
		resp.Body.Close()
		t.Fatal("dial succeeded")
	}
	if errors.Is(err, ErrOutcomeUnknown) {
		t.Errorf("err = %v, want no unknown outcome before the write", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want a dial timeout", err)
	}
	if n := rec.hits.Load(); n != 0 {
		t.Errorf("peer received %d requests", n)
	}
}

func TestCeilingAfterTheWrite(t *testing.T) {
	rec := newRecorder(t, stall)
	opts := fastOpts()
	opts.LongCeiling = 50 * time.Millisecond
	src := newSource(rec.URL, tokenFile(t, testToken), opts)

	for _, tc := range []struct {
		mode    Mode
		unknown bool
	}{{Long, false}, {Detached, true}} {
		resp, err := src.DoAPI(context.Background(), http.MethodPost, "dispatch", nil, nil, []byte(`{}`), tc.mode)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("mode %d outlived the ceiling", tc.mode)
		}
		if got := errors.Is(err, ErrOutcomeUnknown); got != tc.unknown {
			t.Errorf("mode %d err = %v, unknown %v, want %v", tc.mode, err, got, tc.unknown)
		}
	}
}

// hangUp drops the connection without an answer.
func hangUp(w http.ResponseWriter, _ *http.Request) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		conn.Close()
	}
}

func TestDetachedCloseAfterTheWriteIsUnknown(t *testing.T) {
	rec := newRecorder(t, hangUp)
	src := newSource(rec.URL, tokenFile(t, testToken), fastOpts())

	for _, tc := range []struct {
		mode    Mode
		unknown bool
	}{{Long, false}, {Detached, true}} {
		resp, err := src.DoAPI(context.Background(), http.MethodPost, "dispatch", nil, nil, []byte(`{}`), tc.mode)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("mode %d got an answer", tc.mode)
		}
		if got := errors.Is(err, ErrOutcomeUnknown); got != tc.unknown {
			t.Errorf("mode %d err = %v, unknown %v, want %v", tc.mode, err, got, tc.unknown)
		}
	}
}

func TestDetachedTruncatedBodyIsUnknown(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\npartial")
		conn.Close()
	})
	src := newSource(rec.URL, tokenFile(t, testToken), fastOpts())

	for _, tc := range []struct {
		mode    Mode
		unknown bool
	}{{Long, false}, {Detached, true}} {
		resp, err := src.DoAPI(context.Background(), http.MethodPost, "dispatch", nil, nil, []byte(`{}`), tc.mode)
		if err != nil {
			t.Fatalf("mode %d: %v", tc.mode, err)
		}
		_, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err == nil {
			t.Fatalf("mode %d read a truncated body whole", tc.mode)
		}
		if got := errors.Is(err, ErrOutcomeUnknown); got != tc.unknown {
			t.Errorf("mode %d err = %v, unknown %v, want %v", tc.mode, err, got, tc.unknown)
		}
	}
}

func TestSlowModesKeepTheHopCheck(t *testing.T) {
	rec := newRecorder(t, nil)
	src := newSource(rec.URL, tokenFile(t, testToken), fastOpts())
	src.hopOK = func(_, _ net.Addr) bool { return false }
	for _, mode := range []Mode{Long, Detached} {
		if resp, err := src.DoAPI(context.Background(), http.MethodGet, "repos", nil, nil, nil, mode); err == nil {
			resp.Body.Close()
			t.Errorf("mode %d: DoAPI succeeded over a refused hop", mode)
		} else if !errors.Is(err, errInsecureHop) || errors.Is(err, ErrOutcomeUnknown) {
			t.Errorf("mode %d: DoAPI err = %v, want errInsecureHop", mode, err)
		}
		if resp, err := src.Do(context.Background(), http.MethodGet, "pane-1", "chat", nil, nil, nil, mode); err == nil {
			resp.Body.Close()
			t.Errorf("mode %d: Do succeeded over a refused hop", mode)
		} else if !errors.Is(err, errInsecureHop) || errors.Is(err, ErrOutcomeUnknown) {
			t.Errorf("mode %d: Do err = %v, want errInsecureHop", mode, err)
		}
	}
	if n := rec.hits.Load(); n != 0 {
		t.Fatalf("peer received %d requests over a refused hop", n)
	}
}
