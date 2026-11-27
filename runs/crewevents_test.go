package runs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/noamsto/houston/crewfeed"
	"github.com/noamsto/houston/crewview"
)

// advance reads only what a bus gained since the last tick, through the
// cursor, into both the feed and the identity fold; a replaced log starts over.
func TestEventTailsFollowTheCursor(t *testing.T) {
	bus := t.TempDir()
	writeEvents(t, bus, dispatchRow(1000, "feat/a"))
	var calls [][]string
	run := fileCrew(map[string]string{"/wt": bus})
	e := newEventTails(func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		calls = append(calls, args)
		return run(ctx, dir, args...)
	})
	feed := crewfeed.NewStore()
	e.advance(context.Background(), bus, "/wt", feed)
	if id := e.identity(bus)["feat/a"]; id.crew != "c1" {
		t.Fatalf("identity = %+v, want feat/a of c1", id)
	}
	if got := e.crewsOn(bus); !got["c1"] {
		t.Errorf("crewsOn = %v, want c1", got)
	}

	f, err := os.OpenFile(filepath.Join(bus, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(dispatchRow(2000, "feat/b"))
	_ = f.Close()
	e.advance(context.Background(), bus, "/wt", feed)
	if len(e.identity(bus)) != 2 {
		t.Errorf("identity = %v, want both branches", e.identity(bus))
	}
	if len(calls) != 2 || calls[0][4] != "--limit" || calls[1][4] != "--since" {
		t.Errorf("calls = %v, want a limited first read and a cursor read after it", calls)
	}
	p, err := feed.Page(bus, "c1", 0, 0)
	if err != nil || len(p.Entries) != 2 {
		t.Errorf("feed page = %+v, %v; want both dispatches", p, err)
	}
	epoch, _ := feed.Epoch(bus, "c1")

	// A shorter log is a different file: the cursor is past its end.
	writeEvents(t, bus, dispatchRow(3000, "feat/z"))
	e.advance(context.Background(), bus, "/wt", feed)
	if ids := e.identity(bus); len(ids) != 1 || ids["feat/z"].crew != "c1" {
		t.Errorf("identity after the reset = %v, want only feat/z", ids)
	}
	if now, _ := feed.Epoch(bus, "c1"); now == epoch {
		t.Error("the feed kept its epoch across a reset")
	}
}

// A failing events read keeps what was folded and records a first-seen bus in
// the feed, so its epoch exists.
func TestEventTailsKeepStateWhenTheReadFails(t *testing.T) {
	bus := t.TempDir()
	writeEvents(t, bus, dispatchRow(1000, "feat/a"))
	fail := false
	run := fileCrew(map[string]string{"/wt": bus})
	e := newEventTails(func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		if fail {
			return nil, errors.New("crew too old")
		}
		return run(ctx, dir, args...)
	})
	feed := crewfeed.NewStore()
	e.advance(context.Background(), bus, "/wt", feed)
	fail = true
	e.advance(context.Background(), bus, "/wt", feed)
	if _, ok := e.identity(bus)["feat/a"]; !ok {
		t.Error("identity lost after a failed read")
	}

	other := newEventTails(func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("crew too old") })
	other.advance(context.Background(), "/b", "/wt", feed)
	if _, ok := feed.Epoch("/b", "c1"); !ok {
		t.Error("a bus whose first read failed has no epoch")
	}
}

// rescan reads the whole log for an older page; an unknown bus has no root.
func TestEventTailsRescan(t *testing.T) {
	bus := t.TempDir()
	writeEvents(t, bus, dispatchRow(1000, "feat/a")+dispatchRow(2000, "feat/b"))
	e := newEventTails(fileCrew(map[string]string{"/wt": bus}))
	if _, err := e.rescan(context.Background(), bus); !errors.Is(err, crewfeed.ErrNoBus) {
		t.Errorf("rescan before any tick = %v, want ErrNoBus", err)
	}
	e.advance(context.Background(), bus, "/wt", nil)
	b, err := e.rescan(context.Background(), bus)
	if err != nil || len(b.Rows) != 2 || b.Rows[0].Offset != 0 {
		t.Errorf("rescan = %+v, %v; want both rows from offset 0", b, err)
	}
}

// Crew reports a dispatcher on its worker rows only, so one with no worker
// listed has no crew layer yet: the pane stays with the tmux and hook layers.
func TestJoinDispatchersNeedsAWorkerRow(t *testing.T) {
	f := defaultJoinFixture()
	f.ov = crewview.Overview{}
	if got := joinDispatchers(f.ov, f.wins, f.panes, nil, f.probe); len(got) != 0 {
		t.Errorf("join = %v, want none for an overview with no worker rows", got)
	}
}
