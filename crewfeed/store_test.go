package crewfeed

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// question is one bus line yielding a question entry for crew.
func question(crew, branch, text string) string {
	return fmt.Sprintf(`{"ts":1,"crew_id":%q,"from":"worker:%s#s1-1","to":"dispatcher:%s","kind":"msg","body":%q}`+"\n", crew, branch, crew, text)
}

func busFile(bus string) string { return filepath.Join(bus, "events.jsonl") }

func writeBusFile(t *testing.T, bus, data string) {
	t.Helper()
	if err := os.WriteFile(busFile(bus), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendBusFile(t *testing.T, bus, data string) {
	t.Helper()
	f, err := os.OpenFile(busFile(bus), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(data); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// foldFile is the reference: one pass over the whole file, skipping lines
// over maxLine, with ids in epoch.
func foldFile(t *testing.T, bus, epoch string) map[string][]Entry {
	t.Helper()
	data, err := os.ReadFile(busFile(bus))
	if err != nil {
		t.Fatal(err)
	}
	var f Fold
	out := map[string][]Entry{}
	var off int64
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return out
		}
		if i <= maxLine {
			if crew, e, ok := f.Line(data[:i], off); ok {
				e.ID = epoch + "." + strconv.FormatInt(off, 10)
				out[crew] = append(out[crew], e)
			}
		}
		off += int64(i + 1)
		data = data[i+1:]
	}
}

// foldCrew is foldFile's entries for one crew, which must have some.
func foldCrew(t *testing.T, bus, epoch, crew string) []Entry {
	t.Helper()
	es, ok := foldFile(t, bus, epoch)[crew]
	if !ok {
		t.Fatalf("no entries for crew %q", crew)
	}
	return es
}

// idOff is the offset an id names, -1 for a malformed one.
func idOff(id string) int64 {
	_, o, _ := strings.Cut(id, ".")
	n, err := strconv.ParseInt(o, 10, 64)
	if err != nil {
		return -1
	}
	return n
}

func offOf(t *testing.T, id string) int64 {
	t.Helper()
	n := idOff(id)
	if n < 0 {
		t.Fatalf("malformed id %q", id)
	}
	return n
}

// pageAll walks crew's feed from newest to oldest and returns it oldest first.
func pageAll(t *testing.T, s *Store, bus, crew string, limit int) []Entry {
	t.Helper()
	var all []Entry
	var before int64
	for range 100 {
		p, err := s.Page(bus, crew, before, limit)
		if err != nil {
			t.Fatalf("Page(before %d): %v", before, err)
		}
		all = append(append([]Entry{}, p.Entries...), all...)
		if !p.More {
			return all
		}
		if len(p.Entries) == 0 {
			t.Fatalf("Page(before %d) has more but no entries", before)
		}
		before = offOf(t, p.Entries[0].ID)
	}
	t.Fatal("paging did not end")
	return nil
}

func texts(es []Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Text
	}
	return out
}

func mustEpoch(t *testing.T, s *Store, bus string) string {
	t.Helper()
	ep, ok := s.Epoch(bus)
	if !ok {
		t.Fatalf("no epoch for %s", bus)
	}
	return ep
}

// An Advance parses only the bytes appended since the last one: rewriting an
// already-read line in place changes nothing.
func TestAdvanceReadsOnlyAppendedBytes(t *testing.T) {
	bus := t.TempDir()
	a, b := question("c1", "feat/a", "first"), question("c1", "feat/a", "bbbb")
	writeBusFile(t, bus, a+b)
	s := NewStore()
	s.Advance(bus)

	writeBusFile(t, bus, a+question("c1", "feat/a", "zzzz")+question("c1", "feat/a", "third"))
	s.Advance(bus)

	p, err := s.Page(bus, "c1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := texts(p.Entries), []string{"first", "bbbb", "third"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("texts %q, want %q", got, want)
	}
	if got, want := offOf(t, p.Entries[2].ID), int64(len(a)+len(b)); got != want {
		t.Errorf("third entry offset %d, want %d", got, want)
	}
}

func TestAdvanceTornLineWaits(t *testing.T) {
	bus := t.TempDir()
	a, c := question("c1", "feat/a", "first"), question("c1", "feat/a", "second")
	writeBusFile(t, bus, a+c[:10])
	s := NewStore()
	s.Advance(bus)
	ep := mustEpoch(t, s, bus)
	if p, _ := s.Page(bus, "c1", 0, 0); len(p.Entries) != 1 {
		t.Fatalf("torn line yielded an entry: %+v", p.Entries)
	}

	appendBusFile(t, bus, c[10:])
	s.Advance(bus)
	p, _ := s.Page(bus, "c1", 0, 0)
	if got, want := texts(p.Entries), []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("texts %q, want %q", got, want)
	}
	if want := ep + "." + strconv.Itoa(len(a)); p.Entries[1].ID != want {
		t.Errorf("completed line id %q, want %q", p.Entries[1].ID, want)
	}
	if p.Epoch != ep {
		t.Errorf("epoch moved from %q to %q on an append", ep, p.Epoch)
	}
}

func TestAdvanceResets(t *testing.T) {
	a, b := question("c1", "feat/a", "first"), question("c1", "feat/a", "second")
	cases := []struct {
		name  string
		after string
		want  []string
	}{
		{"shrink", a, []string{"first"}},
		{"first line changed", question("c1", "feat/a", "FIRST") + b + question("c1", "feat/a", "third"), []string{"FIRST", "second", "third"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bus := t.TempDir()
			writeBusFile(t, bus, a+b)
			s := NewStore()
			s.Advance(bus)
			old := mustEpoch(t, s, bus)

			writeBusFile(t, bus, tc.after)
			s.Advance(bus)
			ep := mustEpoch(t, s, bus)
			if ep == old {
				t.Fatal("epoch unchanged after a reset")
			}
			p, _ := s.Page(bus, "c1", 0, 0)
			if got := texts(p.Entries); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("texts %q, want %q", got, tc.want)
			}
			if want := foldCrew(t, bus, ep, "c1"); !reflect.DeepEqual(p.Entries, want) {
				t.Errorf("entries %+v, want a fresh fold %+v", p.Entries, want)
			}
			if _, ok := s.Since(bus, "c1", old, 0); ok {
				t.Error("Since accepted the old epoch")
			}
		})
	}
}

func TestAdvanceMissingFileThenAppears(t *testing.T) {
	bus := t.TempDir()
	s := NewStore()
	s.Advance(bus)
	ep := mustEpoch(t, s, bus)
	p, err := s.Page(bus, "c1", 0, 0)
	if err != nil || p.Epoch != ep || p.Entries == nil || len(p.Entries) != 0 || p.More {
		t.Fatalf("Page = %+v, %v; want an empty page in epoch %q", p, err, ep)
	}
	if es, ok := s.Since(bus, "c1", ep, 0); !ok || len(es) != 0 {
		t.Fatalf("Since = %v, %v; want empty and servable", es, ok)
	}

	writeBusFile(t, bus, question("c1", "feat/a", "hello"))
	s.Advance(bus)
	ep2 := mustEpoch(t, s, bus)
	p, _ = s.Page(bus, "c1", 0, 0)
	if got := texts(p.Entries); !reflect.DeepEqual(got, []string{"hello"}) || p.Epoch != ep2 {
		t.Fatalf("page after the file appeared = %+v", p)
	}
	if es, ok := s.Since(bus, "c1", ep2, -1); !ok || len(es) != 1 {
		t.Errorf("Since = %v, %v; want the one entry", es, ok)
	}
}

func TestUnknownBus(t *testing.T) {
	s := NewStore()
	if _, err := s.Page("/nope", "c1", 0, 0); !errors.Is(err, ErrNoBus) {
		t.Errorf("Page err = %v, want ErrNoBus", err)
	}
	if _, ok := s.Epoch("/nope"); ok {
		t.Error("Epoch known for an unseen bus")
	}
	if _, ok := s.Since("/nope", "c1", "x", 0); ok {
		t.Error("Since ok for an unseen bus")
	}
}

// ringBus writes n entries for c1, interleaved with entries for c2 (every
// third line), in two appends so the ring evicts across Advance calls.
func ringBus(t *testing.T, n int) (*Store, string) {
	t.Helper()
	bus := t.TempDir()
	var first, second strings.Builder
	for i := range n {
		w := &first
		if i >= n/2 {
			w = &second
		}
		w.WriteString(question("c1", "feat/a", "c1 #"+strconv.Itoa(i)))
		if i%3 == 0 {
			w.WriteString(question("c2", "feat/b", "c2 #"+strconv.Itoa(i)))
		}
	}
	writeBusFile(t, bus, first.String())
	s := NewStore()
	s.Advance(bus)
	appendBusFile(t, bus, second.String())
	s.Advance(bus)
	return s, bus
}

// Paging past the ring rescans the file and lands on the ids a one-pass fold
// gives; each crew sees only its own entries.
func TestPageRescansPastTheRing(t *testing.T) {
	s, bus := ringBus(t, feedRing+150)
	ep := mustEpoch(t, s, bus)
	want := foldFile(t, bus, ep)
	if len(want["c1"]) != feedRing+150 {
		t.Fatalf("fixture has %d c1 entries", len(want["c1"]))
	}

	for _, limit := range []int{100, 7} {
		if got := pageAll(t, s, bus, "c1", limit); !reflect.DeepEqual(got, want["c1"]) {
			t.Fatalf("limit %d: paged %d entries, want the one-pass fold's %d", limit, len(got), len(want["c1"]))
		}
	}
	if got := pageAll(t, s, bus, "c2", 100); !reflect.DeepEqual(got, want["c2"]) {
		t.Fatalf("c2 paged %d entries, want %d", len(got), len(want["c2"]))
	}

	// The newest page comes from the ring and still reports older entries.
	p, _ := s.Page(bus, "c1", 0, 0)
	if len(p.Entries) != defaultPageLimit || !p.More {
		t.Errorf("newest page: %d entries, more %v; want %d and more", len(p.Entries), p.More, defaultPageLimit)
	}
	// The oldest page comes from a rescan and has nothing older.
	p, err := s.Page(bus, "c1", offOf(t, want["c1"][3].ID), 100)
	if err != nil || len(p.Entries) != 3 || p.More {
		t.Errorf("oldest page = %d entries, more %v, %v; want 3, no more", len(p.Entries), p.More, err)
	}
	// A rescan page that does not reach the start says so.
	p, _ = s.Page(bus, "c1", offOf(t, want["c1"][20].ID), 10)
	if !reflect.DeepEqual(p.Entries, want["c1"][10:20]) || !p.More {
		t.Errorf("rescan page = %q more %v, want entries 10..19 and more", texts(p.Entries), p.More)
	}
}

func TestPageFromAnUnevictedRing(t *testing.T) {
	s, bus := ringBus(t, 60)
	want := foldCrew(t, bus, mustEpoch(t, s, bus), "c1")
	p, _ := s.Page(bus, "c1", 0, 0)
	if !reflect.DeepEqual(p.Entries, want[10:]) || !p.More {
		t.Fatalf("newest page = %d entries more %v, want the newest 50 and more", len(p.Entries), p.More)
	}
	p, _ = s.Page(bus, "c1", offOf(t, p.Entries[0].ID), 0)
	if !reflect.DeepEqual(p.Entries, want[:10]) || p.More {
		t.Fatalf("older page = %d entries more %v, want the oldest 10 and no more", len(p.Entries), p.More)
	}
	for limit, n := range map[int]int{-1: defaultPageLimit, 1000: 60} {
		if p, _ := s.Page(bus, "c1", 0, limit); len(p.Entries) != min(n, maxPageLimit) {
			t.Errorf("limit %d: %d entries", limit, len(p.Entries))
		}
	}
	if p, _ := s.Page(bus, "nobody", 0, 0); p.Entries == nil || len(p.Entries) != 0 || p.More {
		t.Errorf("unknown crew page = %+v, want empty", p)
	}
}

func TestSince(t *testing.T) {
	s, bus := ringBus(t, feedRing+10)
	ep := mustEpoch(t, s, bus)
	all := foldCrew(t, bus, ep, "c1")
	off := func(i int) int64 { return offOf(t, all[i].ID) }
	consumed := func() int64 {
		fi, err := os.Stat(busFile(bus))
		if err != nil {
			t.Fatal(err)
		}
		return fi.Size()
	}()

	cases := []struct {
		name  string
		epoch string
		after int64
		ok    bool
		want  []Entry
	}{
		{"newest", ep, off(len(all) - 1), true, []Entry{}},
		{"middle", ep, off(len(all) - 4), true, all[len(all)-3:]},
		{"the newest evicted entry", ep, off(9), true, all[10:]},
		{"older than the ring", ep, off(8), false, nil},
		{"zero on an evicted ring", ep, 0, false, nil},
		{"past what was read", ep, consumed, false, nil},
		{"foreign epoch", "000000000000", off(len(all) - 1), false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := s.Since(bus, "c1", tc.epoch, tc.after)
			if ok != tc.ok || (ok && !reflect.DeepEqual(got, tc.want)) {
				t.Fatalf("Since = %d entries, %v; want %d, %v", len(got), ok, len(tc.want), tc.ok)
			}
		})
	}

	// A ring that never evicted serves from 0; the line at 0 itself is not
	// after 0.
	s, bus = ringBus(t, 5)
	ep = mustEpoch(t, s, bus)
	all = foldCrew(t, bus, ep, "c1")
	if got, ok := s.Since(bus, "c1", ep, 0); !ok || !reflect.DeepEqual(got, all[1:]) {
		t.Errorf("Since(0) = %q, %v; want every entry after the first", texts(got), ok)
	}
	if got, ok := s.Since(bus, "c1", ep, -1); !ok || !reflect.DeepEqual(got, all) {
		t.Errorf("Since(-1) = %q, %v; want every entry", texts(got), ok)
	}
}

// A line over maxLine is consumed without an entry, the same way when it
// arrives torn across Advance calls and when an older page rescans past it.
func TestOversizedLineSkippedEverywhere(t *testing.T) {
	bus := t.TempDir()
	huge := question("c1", "feat/a", strings.Repeat("x", maxLine))
	writeBusFile(t, bus, question("c1", "feat/a", "before")+huge[:maxLine/2])
	s := NewStore()
	s.Advance(bus)
	var rest strings.Builder
	rest.WriteString(huge[maxLine/2:])
	for i := range feedRing + 20 {
		rest.WriteString(question("c1", "feat/a", "after #"+strconv.Itoa(i)))
	}
	appendBusFile(t, bus, rest.String())
	s.Advance(bus)

	want := foldCrew(t, bus, mustEpoch(t, s, bus), "c1")
	if len(want) != feedRing+21 || want[0].Text != "before" || want[1].Text != "after #0" {
		t.Fatalf("reference fold did not skip the oversized line: %d entries", len(want))
	}
	if got := pageAll(t, s, bus, "c1", 100); !reflect.DeepEqual(got, want) {
		t.Fatalf("paged %d entries, want %d matching the reference", len(got), len(want))
	}
}

// Advance runs alongside Page and Since from several goroutines, through
// appends, evictions, rescans and a reset.
func TestStoreConcurrentReaders(t *testing.T) {
	bus := t.TempDir()
	writeBusFile(t, bus, "")
	s := NewStore()
	s.Advance(bus)

	done := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				ep, _ := s.Epoch(bus)
				p, err := s.Page(bus, "c1", 0, 20)
				if err != nil {
					t.Errorf("newest Page: %v", err)
					return
				}
				for i := 1; i < len(p.Entries); i++ {
					if idOff(p.Entries[i-1].ID) >= idOff(p.Entries[i].ID) {
						t.Errorf("page out of order: %q", p.Entries[i-1].ID)
						return
					}
				}
				if len(p.Entries) > 0 {
					first := idOff(p.Entries[0].ID)
					if _, err := s.Page(bus, "c1", first, 100); err != nil && !errors.Is(err, ErrEpoch) {
						t.Errorf("older Page: %v", err)
						return
					}
					s.Since(bus, "c1", ep, first)
				}
			}
		}()
	}

	for i := range 3 * feedRing {
		if i == 2*feedRing {
			writeBusFile(t, bus, question("c1", "feat/a", "restart"))
		}
		appendBusFile(t, bus, question("c1", "feat/a", "line #"+strconv.Itoa(i)))
		if i%7 == 0 {
			s.Advance(bus)
		}
	}
	s.Advance(bus)
	close(done)
	wg.Wait()

	want := foldCrew(t, bus, mustEpoch(t, s, bus), "c1")
	if got := pageAll(t, s, bus, "c1", 100); !reflect.DeepEqual(got, want) {
		t.Fatalf("after the run: paged %d entries, want %d", len(got), len(want))
	}
}
