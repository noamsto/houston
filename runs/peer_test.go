package runs

import (
	"encoding/json"
	"testing"
)

func TestPeerKeyAndID(t *testing.T) {
	key := PeerKey("b", "pane-3")
	if key != "peer/b/pane-3" {
		t.Fatalf("PeerKey = %q", key)
	}
	if got := idFor(key); got != "peer-b.pane-3" {
		t.Errorf("idFor(%q) = %q, want peer-b.pane-3", key, got)
	}
	if host, ok := peerHost(key); !ok || host != "b" {
		t.Errorf("peerHost(%q) = %q, %v", key, host, ok)
	}
	for _, local := range []string{"%1", "claude/abc", "crew/x/y"} {
		if _, ok := peerHost(local); ok {
			t.Errorf("peerHost(%q) ok, want not a peer key", local)
		}
	}
}

func TestPeerLayerComposition(t *testing.T) {
	cases := []struct {
		name     string
		layer    Run
		wantAtt  Attention
		wantNote string
	}{
		{"done passes through", Run{State: StateReview, Attention: AttentionDone}, AttentionDone, ""},
		{"stuck keeps its note", Run{State: StateRunning, Attention: AttentionStuck, AttentionNote: "quiet"}, AttentionStuck, "quiet"},
		{"needs-you on a non-blocked state is dropped", Run{State: StateRunning, Attention: AttentionNeedsYou, AttentionNote: "x"}, AttentionNone, ""},
		{"blocked is needs-you", Run{State: StateBlocked, AttentionNote: "x"}, AttentionNeedsYou, ""},
		{"none stays none", Run{State: StateRunning}, AttentionNone, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := NewRegistry(DefaultOrder)
			key := PeerKey("b", "pane-3")
			layer := c.layer
			layer.Agent, layer.Host = "claude", "b"
			r.Apply(Delta{Source: PeerLayer, Key: key, Run: layer})

			snap := r.Snapshot()
			if len(snap) != 1 {
				t.Fatalf("%d runs, want 1", len(snap))
			}
			got := snap[0]
			if got.ID != "peer-b.pane-3" || got.Host != "b" {
				t.Errorf("id %q host %q, want peer-b.pane-3 / b", got.ID, got.Host)
			}
			if got.Caps != (Caps{}) {
				t.Errorf("caps = %+v, want zero: a peer run is read-only here", got.Caps)
			}
			if got.Attention != c.wantAtt || got.AttentionNote != c.wantNote {
				t.Errorf("attention = %q %q, want %q %q", got.Attention, got.AttentionNote, c.wantAtt, c.wantNote)
			}
		})
	}
}

func TestPeerLayerCapsComposition(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	sub := r.Subscribe()
	defer r.Unsubscribe(sub)

	key := PeerKey("b", "pane-2")
	r.Apply(Delta{Source: PeerLayer, Key: key, Run: Run{Agent: "claude", Host: "b", Caps: Caps{Terminal: true, Chat: true}}})
	select {
	case <-sub:
	default:
		t.Fatal("no broadcast")
	}
	snap := r.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("%d runs, want 1", len(snap))
	}
	if want := (Caps{Terminal: true, Chat: true}); snap[0].Caps != want {
		t.Errorf("caps = %+v, want %+v", snap[0].Caps, want)
	}

	r.Apply(Delta{Source: PeerLayer, Key: key, Run: Run{Agent: "claude", Host: "b", Stale: true}})
	select {
	case got := <-sub:
		if !got.Stale {
			t.Errorf("re-emitted run stale = false, want true")
		}
	default:
		t.Fatal("stale peer run was not re-emitted")
	}
	snap = r.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("%d runs, want 1", len(snap))
	}
	if snap[0].Caps != (Caps{}) {
		t.Errorf("stale caps = %+v, want zero", snap[0].Caps)
	}
}

func TestGoneBroadcastsHostOnlyForPeerKeys(t *testing.T) {
	r := NewRegistry(DefaultOrder)
	sub := r.Subscribe()
	defer r.Unsubscribe(sub)

	next := func() Run {
		t.Helper()
		select {
		case run := <-sub:
			return run
		default:
			t.Fatal("no broadcast")
			return Run{}
		}
	}

	peerKey := PeerKey("b", "pane-3")
	r.Apply(Delta{Source: PeerLayer, Key: peerKey, Run: Run{Agent: "claude", Host: "b"}})
	next()
	r.Apply(Delta{Source: PeerLayer, Key: peerKey, Gone: true})
	got := next()
	if !got.Removed || got.ID != "peer-b.pane-3" || got.Host != "b" {
		t.Errorf("peer removal = %+v, want id peer-b.pane-3, host b, removed", got)
	}

	r.Apply(Delta{Source: "hooks", Key: "%1", Run: Run{Agent: "claude"}})
	next()
	r.Apply(Delta{Source: "hooks", Key: "%1", Gone: true})
	got = next()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(Run{ID: "pane-1", Removed: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("local removal = %s, want %s", gotJSON, wantJSON)
	}
}
