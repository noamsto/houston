package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/noamsto/houston/hook"
	"github.com/noamsto/houston/hub"
	"github.com/noamsto/houston/mode"
	"github.com/noamsto/houston/runs"
)

func TestRunsSnapshotReturnsComposedRuns(t *testing.T) {
	reg := runs.NewRegistry(runs.DefaultOrder)
	reg.Apply(runs.Delta{Source: "tmux", Key: "%1", Run: runs.Run{Branch: "main"}})
	reg.Apply(runs.Delta{Source: "hooks", Key: "%1", Run: runs.Run{Agent: "claude", State: runs.StateRunning}})

	s := &Server{runs: reg}
	rec := httptest.NewRecorder()
	s.handleRunsSnapshot(rec, httptest.NewRequest("GET", "/api/runs", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	var got []runs.Run
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v — body %s", err, rec.Body.String())
	}
	if len(got) != 1 || got[0].State != runs.StateRunning || got[0].Branch != "main" {
		t.Fatalf("got %+v, want one composed run", got)
	}
}

// TestRunsSnapshotShowsAPiAgentFromAHookEnvelope wires the real hook →
// hub → HookSource → Registry pipeline (server.go's New does the same) over a
// pi session_start envelope, and checks the resulting /api/runs body carries
// the engine hookyard reported.
func TestRunsSnapshotShowsAPiAgentFromAHookEnvelope(t *testing.T) {
	// No tmux to talk to: the pane key path is not under test here.
	t.Setenv("TMUX_PANE", "")
	t.Setenv("TMUX", "")

	stateDir := t.TempDir()
	payload := []byte(`{
		"engine": "pi",
		"canonical_event": "session_start",
		"native_event": "session_start",
		"session_id": "pi-s1",
		"cwd": "/w",
		"native": {
			"cwd": "/w",
			"hook_event_name": "session_start",
			"session_id": "pi-s1",
			"session_file": "/s/pi.jsonl",
			"reason": "startup"
		}
	}`)
	if err := hook.Dispatch("", stateDir, bytes.NewReader(payload)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := hub.NewWithOptions(stateDir, hub.Options{ClaudeProjectsDir: "-"}, discard)

	ctx, cancel := context.WithCancel(context.Background())
	hubDone := make(chan struct{})
	go func() {
		defer close(hubDone)
		_ = h.Run(ctx)
	}()

	reg := runs.NewRegistry(runs.DefaultOrder)
	deltas := make(chan runs.Delta, 16)
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		for d := range deltas {
			reg.Apply(d)
		}
	}()

	src := runs.NewHookSource(h, nil)
	srcDone := make(chan struct{})
	go func() {
		defer close(srcDone)
		_ = src.Run(ctx, deltas)
	}()

	t.Cleanup(func() {
		cancel()
		waitClosed := func(what string, ch <-chan struct{}) {
			select {
			case <-ch:
			case <-time.After(5 * time.Second):
				t.Errorf("%s did not stop after cancel", what)
			}
		}
		waitClosed("hub", hubDone)
		waitClosed("hook source", srcDone)
		close(deltas)
		waitClosed("delta pump", pumpDone)
	})

	s := &Server{runs: reg}
	deadline := time.Now().Add(5 * time.Second)
	var body []byte
	for time.Now().Before(deadline) {
		rec := httptest.NewRecorder()
		s.handleRunsSnapshot(rec, httptest.NewRequest("GET", "/api/runs", nil))
		body = rec.Body.Bytes()

		var got []runs.Run
		if err := json.Unmarshal(body, &got); err == nil && len(got) == 1 {
			if got[0].Agent != "pi" {
				t.Fatalf("Agent = %q, want pi; body=%s", got[0].Agent, body)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no run appeared within deadline; last body=%s", body)
}

func TestRunsSnapshotWithoutRegistryIs503(t *testing.T) {
	s := &Server{} // registry never started
	rec := httptest.NewRecorder()
	s.handleRunsSnapshot(rec, httptest.NewRequest("GET", "/api/runs", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
}

func TestRunsStreamDeliversARemoval(t *testing.T) {
	reg := runs.NewRegistry(runs.DefaultOrder)
	reg.Apply(runs.Delta{Source: "hooks", Key: "%1", Run: runs.Run{Agent: "claude", State: runs.StateRunning}})

	s := &Server{runs: reg}
	rec := newSyncRecorder()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/runs/stream", nil).WithContext(ctx)

	done := make(chan struct{})
	go func() {
		s.handleRunsStream(rec, req)
		close(done)
	}()

	waitForBody := func(substr string, timeout time.Duration) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if strings.Contains(rec.body(), substr) {
				return true
			}
			time.Sleep(time.Millisecond)
		}
		return false
	}

	if !waitForBody("event: snapshot", 5*time.Second) {
		t.Fatalf("snapshot event never sent\n%s", rec.body())
	}

	// The run's only agent-bearing layer leaves — the registry must report it
	// as removed, not just fall silent.
	reg.Apply(runs.Delta{Source: "hooks", Key: "%1", Gone: true})

	if !waitForBody(`"removed":true`, 5*time.Second) {
		t.Fatalf("removal never reached the client\n%s", rec.body())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Errorf("handler did not return after cancel")
	}
}

func TestRunsStreamPingsAsANamedEvent(t *testing.T) {
	s := &Server{runs: runs.NewRegistry(runs.DefaultOrder), runsPing: 20 * time.Millisecond}
	rec := newSyncRecorder()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/runs/stream", nil).WithContext(ctx)

	done := make(chan struct{})
	go func() {
		s.handleRunsStream(rec, req)
		close(done)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(rec.body(), "event: ping\ndata: \n\n") {
		if time.Now().After(deadline) {
			t.Fatalf("no named ping event within 3s\n%s", rec.body())
		}
		time.Sleep(time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Errorf("handler did not return after cancel")
	}
}

func TestRunsRoutesAreBehindTheAuthGate(t *testing.T) {
	// A new route registered outside apiMux would reopen the hole closed in #4.
	dir := t.TempDir()
	s := newFullServer(t, Config{StatusDir: dir, AuthEnabled: true, Mode: mode.Dispatcher})

	req := httptest.NewRequest("GET", "http://127.0.0.1/api/runs", nil)
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401 — /api/runs must require a token", rec.Code)
	}
}

const (
	scopeLocalID = "pane-1"
	scopePeerID  = "peer-b.pane-1"
)

func localScopeDelta(branch string) runs.Delta {
	return runs.Delta{Source: "tmux", Key: "%1", Run: runs.Run{
		Agent: "claude", State: runs.StateIdle, Branch: branch,
	}}
}

func peerScopeDelta(branch string) runs.Delta {
	return runs.Delta{Source: runs.PeerLayer, Key: runs.PeerKey("b", "pane-1"), Run: runs.Run{
		Agent: "claude", Host: "b", State: runs.StateIdle, Branch: branch,
	}}
}

func scopeRegistry() *runs.Registry {
	reg := runs.NewRegistry(runs.DefaultOrder)
	reg.Apply(localScopeDelta("local-branch"))
	reg.Apply(peerScopeDelta("peer-branch"))
	return reg
}

func snapshotIDs(t *testing.T, s *Server, target string) []string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleRunsSnapshot(rec, httptest.NewRequest("GET", target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d", target, rec.Code)
	}
	var got []runs.Run
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ids := make([]string, 0, len(got))
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	return ids
}

func startRunsStream(t *testing.T, s *Server, target string) *syncRecorder {
	t.Helper()
	rec := newSyncRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleRunsStream(rec, httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx))
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return rec
}

func waitBody(t *testing.T, rec *syncRecorder, substr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(rec.body(), substr) {
		if time.Now().After(deadline) {
			t.Fatalf("%q never arrived\n%s", substr, rec.body())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestNewWithoutPeersStartsNoPeerSources(t *testing.T) {
	s := newFullServer(t, Config{StatusDir: t.TempDir(), Mode: mode.Dispatcher})
	if len(s.peers) != 0 {
		t.Fatalf("%d peer sources, want 0", len(s.peers))
	}
	if len(s.instance) != 32 {
		t.Fatalf("instance %q, want 32 hex characters", s.instance)
	}
}

func TestRunsWithoutPeersAreByteIdenticalWithAndWithoutScope(t *testing.T) {
	reg := runs.NewRegistry(runs.DefaultOrder)
	reg.Apply(localScopeDelta("local-branch"))
	s := &Server{runs: reg, instance: "inst"}

	var want bytes.Buffer
	if err := json.NewEncoder(&want).Encode(reg.Snapshot()); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/api/runs", "/api/runs?scope=local"} {
		rec := httptest.NewRecorder()
		s.handleRunsSnapshot(rec, httptest.NewRequest("GET", target, nil))
		if rec.Body.String() != want.String() {
			t.Fatalf("%s body %q, want %q", target, rec.Body.String(), want.String())
		}
	}

	snap, err := json.Marshal(reg.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	frame := "event: snapshot\ndata: " + string(snap) + "\n\n"
	rec := startRunsStream(t, s, "/api/runs/stream")
	waitBody(t, rec, "\n\n")
	if got := rec.body(); got != frame {
		t.Fatalf("first frame %q, want %q", got, frame)
	}
	if got := rec.Header().Get("X-Houston-Instance"); got != "inst" {
		t.Fatalf("instance header %q, want inst", got)
	}
}

func TestRunsSnapshotScope(t *testing.T) {
	s := &Server{runs: scopeRegistry()}

	if got := snapshotIDs(t, s, "/api/runs"); len(got) != 2 {
		t.Fatalf("no scope: %v, want both runs", got)
	}
	got := snapshotIDs(t, s, "/api/runs?scope=local")
	if len(got) != 1 || got[0] != scopeLocalID {
		t.Fatalf("scope=local: %v, want only %s", got, scopeLocalID)
	}
	empty := &Server{runs: runs.NewRegistry(runs.DefaultOrder)}
	rec := httptest.NewRecorder()
	empty.handleRunsSnapshot(rec, httptest.NewRequest("GET", "/api/runs?scope=local", nil))
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty local scope body %q, want []", rec.Body.String())
	}
}

func TestRunsScopeUnknownIs400(t *testing.T) {
	s := &Server{runs: scopeRegistry()}
	for _, target := range []string{"/api/runs?scope=bogus", "/api/runs?scope=", "/api/runs?scope=local&scope=local"} {
		rec := httptest.NewRecorder()
		s.handleRunsSnapshot(rec, httptest.NewRequest("GET", target, nil))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown scope") {
			t.Fatalf("snapshot %s: %d %q, want 400 unknown scope", target, rec.Code, rec.Body.String())
		}
		rec = httptest.NewRecorder()
		s.handleRunsStream(rec, httptest.NewRequest("GET", strings.Replace(target, "/api/runs", "/api/runs/stream", 1), nil))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown scope") {
			t.Fatalf("stream %s: %d %q, want 400 unknown scope", target, rec.Code, rec.Body.String())
		}
	}
}

func TestRunsStreamLocalScopeSkipsPeerRuns(t *testing.T) {
	reg := scopeRegistry()
	s := &Server{runs: reg}
	rec := startRunsStream(t, s, "/api/runs/stream?scope=local")
	waitBody(t, rec, "\n\n")
	if body := rec.body(); !strings.Contains(body, "local-branch") || strings.Contains(body, scopePeerID) {
		t.Fatalf("initial snapshot should hold only the local run\n%s", body)
	}

	reg.Apply(peerScopeDelta("peer-update"))
	reg.Apply(runs.Delta{Source: runs.PeerLayer, Key: runs.PeerKey("b", "pane-1"), Gone: true})
	reg.Apply(localScopeDelta("local-update"))
	waitBody(t, rec, "local-update")
	// The subscriber channel is ordered, so the peer events were already
	// handled by the time the local one is written.
	body := rec.body()
	for _, leak := range []string{"peer-update", scopePeerID, `"removed":true`} {
		if strings.Contains(body, leak) {
			t.Fatalf("local scope leaked %q\n%s", leak, body)
		}
	}
}

func TestRunsStreamWithoutScopeDeliversPeerUpdates(t *testing.T) {
	reg := scopeRegistry()
	s := &Server{runs: reg}
	rec := startRunsStream(t, s, "/api/runs/stream")
	waitBody(t, rec, "\n\n")

	reg.Apply(peerScopeDelta("peer-update"))
	waitBody(t, rec, "peer-update")
	reg.Apply(runs.Delta{Source: runs.PeerLayer, Key: runs.PeerKey("b", "pane-1"), Gone: true})
	waitBody(t, rec, `"removed":true`)
}

func TestRunAddressedRoutesDoNotResolvePeerRuns(t *testing.T) {
	s := newReplyServer(t, nil, localScopeDelta("local-branch"), peerScopeDelta("peer-branch"))

	if _, ok := s.findRun(scopeLocalID); !ok {
		t.Fatal("local run not found")
	}
	if _, ok := s.findRun(scopePeerID); ok {
		t.Fatal("peer run resolved by findRun")
	}

	base := "/api/runs/" + scopePeerID
	for _, rt := range []struct{ method, path, body string }{
		{"POST", base + "/reply", replyBody(t, "hi")},
		{"GET", base + "/chat", ""},
		{"GET", base + "/terminal", ""},
	} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, replyRequest(rt.method, rt.path, rt.body))
		wantRefusal(t, rec, http.StatusNotFound, "no such run")
	}
}
