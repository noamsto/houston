package crewfeed

import (
	"context"
	"errors"
	"testing"
)

func rowsOf(lines ...string) []Row {
	rows := []Row{}
	var off int64
	for _, l := range lines {
		rows = append(rows, Row{Offset: off, Line: []byte(l)})
		off += int64(len(l)) + 1
	}
	return rows
}

func q(text string) string {
	return question("c1", "feat/a", text)[:len(question("c1", "feat/a", text))-1]
}

// Apply takes rows as the events view serves them: entries id by the row's
// offset, rows after the first batch append, and a reset starts a new epoch
// whose entries replace the old ones.
func TestApplyBatchesAndReset(t *testing.T) {
	s := NewStore()
	first := rowsOf(q("one"), q("two"))
	s.Apply("bus", Batch{Rows: first})
	epoch1 := mustEpoch(t, s, "bus", "c1")

	next := Row{Offset: first[1].Offset + int64(len(first[1].Line)) + 1, Line: []byte(q("three"))}
	s.Apply("bus", Batch{Rows: []Row{next}})
	p, err := s.Page("bus", "c1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(p.Entries); len(got) != 3 {
		t.Fatalf("entries = %q, want three", got)
	}
	if p.Epoch != epoch1 || offOf(t, p.Entries[2].ID) != next.Offset {
		t.Errorf("epoch %q (want %q), last id %q (want offset %d)", p.Epoch, epoch1, p.Entries[2].ID, next.Offset)
	}

	s.Apply("bus", Batch{Reset: true, Rows: rowsOf(q("fresh"))})
	epoch2 := mustEpoch(t, s, "bus", "c1")
	if epoch2 == epoch1 {
		t.Error("a reset kept the epoch")
	}
	p, err = s.Page("bus", "c1", 0, 0)
	if err != nil || len(p.Entries) != 1 || p.Entries[0].Text == "" || p.Epoch != epoch2 {
		t.Errorf("after reset: %+v, %v; want the one fresh entry under the new epoch", p, err)
	}
}

// An empty first batch records the bus so its epoch is known before any row.
func TestApplyEmptyFirstBatchRecordsTheBus(t *testing.T) {
	s := NewStore()
	if _, ok := s.Epoch("bus", "c1"); ok {
		t.Fatal("epoch known before any batch")
	}
	s.Apply("bus", Batch{})
	if _, ok := s.Epoch("bus", "c1"); !ok {
		t.Error("an empty batch did not record the bus")
	}
}

// Two stores (two houston processes) never share an epoch for one bus.
func TestEpochsDifferAcrossStores(t *testing.T) {
	a, b := NewStore(), NewStore()
	a.Apply("bus", Batch{})
	b.Apply("bus", Batch{})
	if mustEpoch(t, a, "bus", "c1") == mustEpoch(t, b, "bus", "c1") {
		t.Error("two stores minted the same epoch")
	}
}

// An older page past the ring comes from the rescan; a failing rescan is the
// page's error, and no rescan serves what the ring holds.
func TestPageRescanSeam(t *testing.T) {
	var lines []string
	for i := range feedRing + 20 {
		lines = append(lines, q("m"+string(rune('a'+i%26))))
	}
	rows := rowsOf(lines...)

	s := NewStore()
	s.Apply("bus", Batch{Rows: rows})
	s.SetRescan(func(context.Context, string) (Batch, error) { return Batch{Rows: rows}, nil })
	if all := pageAll(t, s, "bus", "c1", 100); len(all) != len(rows) {
		t.Errorf("paged %d entries with a rescan, want %d", len(all), len(rows))
	}

	boom := errors.New("boom")
	s.SetRescan(func(context.Context, string) (Batch, error) { return Batch{}, boom })
	if _, err := s.Page("bus", "c1", rows[10].Offset, 5); !errors.Is(err, boom) {
		t.Errorf("Page err = %v, want boom", err)
	}

	s.SetRescan(nil)
	p, err := s.Page("bus", "c1", rows[10].Offset, 5)
	if err != nil || !p.More {
		t.Errorf("no rescan: %+v, %v; want a page flagged more", p, err)
	}
}
