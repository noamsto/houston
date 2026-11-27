package runs

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"sync"

	"github.com/noamsto/houston/crewfeed"
	"github.com/noamsto/houston/crewview"
)

const (
	// firstEventsLimit is how many rows the first fetch of a bus asks for: the
	// identity fold needs each branch's dispatch row, so it asks for far more
	// than crew's default page.
	firstEventsLimit = 50000
	// rescanEventsLimit bounds the whole-log read an older feed page costs.
	rescanEventsLimit = 100000
	// maxEventPages caps the follow-up pages one tick reads when a page says
	// more rows remain.
	maxEventPages = 20
	// followEventsLimit is the page size of a read after the first one.
	followEventsLimit = 5000
)

// eventTails reads each bus's log through crew's events view and keeps its
// cursor, so a tick reads only the rows appended since the last one. Every
// page goes to the activity feed and to the identity fold. It is the only
// place houston reads a bus log.
type eventTails struct {
	run crewview.Runner

	mu    sync.Mutex
	roots map[string]string
	tails map[string]*busTail
}

// busTail is one bus's position and fold.
type busTail struct {
	cursor string
	fold   identityFold
	crews  map[string]bool
	warned string
}

func newEventTails(run crewview.Runner) *eventTails {
	return &eventTails{run: run, roots: map[string]string{}, tails: map[string]*busTail{}}
}

// advance reads the rows bus gained since the last call, from the bus crew
// resolves in root, into feed (when not nil) and the identity fold. A failed
// read keeps what was folded; it is logged once per distinct error.
func (e *eventTails) advance(ctx context.Context, bus, root string, feed *crewfeed.Store) {
	e.mu.Lock()
	e.roots[bus] = root
	t, known := e.tails[bus]
	first := !known || t == nil
	if first {
		t = &busTail{crews: map[string]bool{}}
		e.tails[bus] = t
	}
	cursor := t.cursor
	e.mu.Unlock()

	for range maxEventPages {
		limit := followEventsLimit
		if cursor == "" {
			limit = firstEventsLimit
		}
		page, err := crewview.FetchEvents(ctx, e.run, root, cursor, limit)
		if err != nil {
			e.fail(bus, t, err)
			if first && feed != nil {
				feed.Apply(bus, crewfeed.Batch{})
			}
			return
		}
		e.mu.Lock()
		if page.Reset {
			t.fold = identityFold{}
			t.crews = map[string]bool{}
		}
		rows := make([]crewfeed.Row, 0, len(page.Events))
		skipped := 0
		for _, ev := range page.Events {
			off, ok := crewview.EventOffset(ev.ID)
			if !ok {
				skipped++
				continue
			}
			rows = append(rows, crewfeed.Row{Offset: off, Line: ev.Row})
			t.fold.Row(ev.Row)
		}
		if skipped > 0 {
			t.warnLocked(bus, fmt.Sprintf("%d event(s) with an unreadable id skipped", skipped))
		}
		for _, id := range t.fold.ids {
			if id.crew != "" {
				t.crews[id.crew] = true
			}
		}
		t.cursor = page.Cursor
		cursor = page.Cursor
		e.mu.Unlock()
		if feed != nil {
			feed.Apply(bus, crewfeed.Batch{Rows: rows, Reset: page.Reset})
		}
		first = false
		if !page.More {
			e.mu.Lock()
			if skipped == 0 {
				t.warned = ""
			}
			e.mu.Unlock()
			return
		}
	}
}

func (e *eventTails) fail(bus string, t *busTail, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if t != nil {
		t.warnLocked(bus, err.Error())
	}
}

// warnLocked logs msg unless it is the last thing warned about for the bus, so
// a condition that holds every tick is logged once. e.mu is held.
func (t *busTail) warnLocked(bus, msg string) {
	if t.warned == msg {
		return
	}
	t.warned = msg
	slog.Warn("crew source: bus events", "bus", bus, "problem", msg)
}

// identity is the fold of bus's rows so far: who each branch's worker is.
func (e *eventTails) identity(bus string) map[string]crewIdentity {
	e.mu.Lock()
	defer e.mu.Unlock()
	t := e.tails[bus]
	if t == nil {
		return map[string]crewIdentity{}
	}
	return maps.Clone(t.fold.result())
}

// crewsOn lists the crew ids bus's log names.
func (e *eventTails) crewsOn(bus string) map[string]bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[string]bool{}
	if t := e.tails[bus]; t != nil {
		maps.Copy(out, t.crews)
	}
	return out
}

// rescan reads bus's whole log (bounded) for an older feed page.
func (e *eventTails) rescan(ctx context.Context, bus string) (crewfeed.Batch, error) {
	e.mu.Lock()
	root := e.roots[bus]
	e.mu.Unlock()
	if root == "" {
		return crewfeed.Batch{}, crewfeed.ErrNoBus
	}
	page, err := crewview.FetchEvents(ctx, e.run, root, "", rescanEventsLimit)
	if err != nil {
		return crewfeed.Batch{}, err
	}
	rows := make([]crewfeed.Row, 0, len(page.Events))
	for _, ev := range page.Events {
		if off, ok := crewview.EventOffset(ev.ID); ok {
			rows = append(rows, crewfeed.Row{Offset: off, Line: ev.Row})
		}
	}
	return crewfeed.Batch{Rows: rows}, nil
}
