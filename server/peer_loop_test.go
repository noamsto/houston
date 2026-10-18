package server

import (
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/noamsto/houston/peer"
	"github.com/noamsto/houston/runs"
)

// newFederationServer is newAuthTestServer with one local run keyed paneKey,
// its own instance id and a short runs-stream ping.
func newFederationServer(token, instance, paneKey string) *Server {
	s := newAuthTestServer(token)
	s.instance = instance
	s.runsPing = 20 * time.Millisecond
	s.runs = runs.NewRegistry(runs.DefaultOrder)
	s.runs.Apply(federationDelta(paneKey, "initial"))
	return s
}

func federationDelta(paneKey, branch string) runs.Delta {
	return runs.Delta{Source: "tmux", Key: paneKey, Run: runs.Run{
		Agent: "claude", State: runs.StateIdle, Branch: branch,
	}}
}

func listedIDs(reg *runs.Registry) []string {
	var ids []string
	for _, r := range reg.Snapshot() {
		ids = append(ids, r.ID)
	}
	slices.Sort(ids)
	return ids
}

func waitIDs(t *testing.T, name string, reg *runs.Registry, want ...string) {
	t.Helper()
	slices.Sort(want)
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Equal(listedIDs(reg), want) {
		if time.Now().After(deadline) {
			t.Fatalf("%s lists %v, want %v", name, listedIDs(reg), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitBranch(t *testing.T, name string, reg *runs.Registry, id, branch string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, r := range reg.Snapshot() {
			if r.ID == id && r.Branch == branch {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never saw %s on branch %q", name, id, branch)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func peerOptions(self string) peer.Options {
	return peer.Options{Self: self, MinBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond}
}

func TestTwoHoustonsMirrorEachOthersLocalRunsWithoutEcho(t *testing.T) {
	a := newFederationServer("token-a", "instance-a", "%1")
	b := newFederationServer("token-b", "instance-b", "%2")
	srvA := httptest.NewServer(a.Handler())
	t.Cleanup(srvA.Close)
	srvB := httptest.NewServer(b.Handler())
	t.Cleanup(srvB.Close)

	startSource(t, peer.New(
		peer.Config{Name: "b", URL: srvB.URL, TokenFile: writeTokenFile(t, "token-b")},
		peerOptions(a.instance),
	), a.runs)
	startSource(t, peer.New(
		peer.Config{Name: "a", URL: srvA.URL, TokenFile: writeTokenFile(t, "token-a")},
		peerOptions(b.instance),
	), b.runs)

	waitIDs(t, "A", a.runs, "pane-1", "peer-b.pane-2")
	waitIDs(t, "B", b.runs, "pane-2", "peer-a.pane-1")

	a.runs.Apply(federationDelta("%1", "a-update-1"))
	a.runs.Apply(federationDelta("%1", "a-update-2"))
	b.runs.Apply(federationDelta("%2", "b-update-1"))
	b.runs.Apply(federationDelta("%2", "b-update-2"))
	waitBranch(t, "B", b.runs, "peer-a.pane-1", "a-update-2")
	waitBranch(t, "A", a.runs, "peer-b.pane-2", "b-update-2")

	// A resync or an echo would show up within a few ping intervals.
	time.Sleep(10 * a.runsPing)
	waitIDs(t, "A", a.runs, "pane-1", "peer-b.pane-2")
	waitIDs(t, "B", b.runs, "pane-2", "peer-a.pane-1")
	for _, id := range append(listedIDs(a.runs), listedIDs(b.runs)...) {
		if strings.Count(id, "peer-") > 1 {
			t.Fatalf("id %q carries a second peer segment", id)
		}
	}
}

func TestSelfPeerIsRecognisedAndMirrorsNothing(t *testing.T) {
	s := newFederationServer("token-s", "instance-s", "%1")
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	src := peer.New(
		peer.Config{Name: "me", URL: srv.URL, TokenFile: writeTokenFile(t, "token-s")},
		peerOptions(s.instance),
	)
	s.peers = []*peer.Source{src}
	startSource(t, src, s.runs)

	deadline := time.Now().Add(5 * time.Second)
	for src.Health().Error != peer.ClassSelf {
		if time.Now().After(deadline) {
			t.Fatalf("health %+v, want unreachable/%s", src.Health(), peer.ClassSelf)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if h := src.Health(); h.State != peer.StateUnreachable {
		t.Fatalf("health %+v, want unreachable", h)
	}
	time.Sleep(5 * s.runsPing)
	waitIDs(t, "self", s.runs, "pane-1")
}
