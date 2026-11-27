package crewfeed

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"
)

// feedRing is how many of its newest entries each crew keeps in memory; an
// older page is rebuilt by rescanning the file.
const feedRing = 500

const (
	defaultPageLimit = 50
	maxPageLimit     = 100
)

var (
	// ErrNoBus is returned for a bus Advance has never seen.
	ErrNoBus = errors.New("crewfeed: unknown bus")
	// ErrEpoch is returned when the log was reset while an older page was
	// being rescanned: the cursor no longer names this log.
	ErrEpoch = errors.New("crewfeed: epoch changed")
)

// Row is one bus row as the crew CLI's events view serves it: Offset is the
// byte offset of its line in the log (the stable part of the event id) and
// Line the row's JSON.
type Row struct {
	Offset int64
	Line   []byte
}

// Batch is rows in log order. Reset says the log was replaced (rotated or
// truncated) and Rows now start from its top: the store starts over under a
// new epoch.
type Batch struct {
	Rows  []Row
	Reset bool
}

// RescanFunc returns bus's whole log, as a batch, for an older page than the
// ring holds.
type RescanFunc func(ctx context.Context, bus string) (Batch, error)

// Store folds each crew bus's rows into a per-crew ring of entries. Apply is
// the only writer; Epoch, Page and Since are safe from any goroutine. Entries
// handed out share their PR pointer with the ring, so callers must not write
// through it.
type Store struct {
	// nonce names this store: an epoch from an earlier houston process can
	// never match one of this process's.
	nonce string

	mu     sync.Mutex
	tails  map[string]*tail
	views  map[string]*view
	rescan RescanFunc
}

// tail is Apply's own position in one bus: the fold state no reader touches.
type tail struct {
	gen  uint64
	fold Fold
}

// snapshot is what a rescan needs of a published view.
type snapshot struct {
	busEpoch string
	consumed int64
}

type view struct {
	snapshot
	rings map[string]*ring
}

type item struct {
	off int64
	e   Entry
}

// ring holds a crew's newest entries in file order. evicted is the offset of
// the newest entry pushed out, -1 while none has been.
type ring struct {
	items   []item
	evicted int64
}

func (r *ring) push(it item) {
	if len(r.items) < feedRing {
		r.items = append(r.items, it)
		return
	}
	r.evicted = r.items[0].off
	copy(r.items, r.items[1:])
	r.items[len(r.items)-1] = it
}

func NewStore() *Store {
	var n [8]byte
	_, _ = rand.Read(n[:])
	return &Store{nonce: hex.EncodeToString(n[:]), tails: map[string]*tail{}, views: map[string]*view{}}
}

// SetRescan installs how an older page is rebuilt; without it a page holds
// only what the ring still has.
func (s *Store) SetRescan(f RescanFunc) {
	s.mu.Lock()
	s.rescan = f
	s.mu.Unlock()
}

// feedEpoch names a bus log's entries. gen restarts at 0 with houston, so the
// epoch also carries the store's nonce.
func feedEpoch(nonce, bus string, gen uint64) string {
	sum := sha256.Sum256([]byte(nonce + "\x00" + bus + "\x00" + strconv.FormatUint(gen, 10)))
	return hex.EncodeToString(sum[:])[:12]
}

// crewEpoch names one crew's entries within a bus epoch, so a cursor minted
// for one crew is never accepted for another crew on the same bus.
func crewEpoch(busEpoch, crew string) string {
	sum := sha256.Sum256([]byte(busEpoch + "\x00" + crew))
	return hex.EncodeToString(sum[:])[:12]
}

func entryID(epoch string, off int64) string {
	return epoch + "." + strconv.FormatInt(off, 10)
}

type pending struct {
	crew string
	it   item
}

// Apply folds b, the rows appended to bus since the last call. A reset batch,
// and the first batch for a bus, starts a new epoch (the first with no
// entries needs none: an empty batch records the bus so its epoch is known
// before it has a row).
func (s *Store) Apply(bus string, b Batch) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t := s.tails[bus]
	_, known := s.views[bus]
	if t == nil || b.Reset || !known {
		next := &tail{}
		if t != nil {
			next.gen = t.gen + 1
		}
		t = next
		s.tails[bus] = t
		s.views[bus] = &view{
			snapshot: snapshot{busEpoch: feedEpoch(s.nonce, bus, t.gen)},
			rings:    map[string]*ring{},
		}
	}
	v := s.views[bus]

	epochs := map[string]string{}
	var got []pending
	for _, r := range b.Rows {
		v.consumed = max(v.consumed, r.Offset+1)
		crew, e, ok := t.fold.Line(r.Line, r.Offset)
		if !ok {
			continue
		}
		epoch, seen := epochs[crew]
		if !seen {
			epoch = crewEpoch(v.busEpoch, crew)
			epochs[crew] = epoch
		}
		e.ID = entryID(epoch, r.Offset)
		got = append(got, pending{crew, item{r.Offset, e}})
	}
	pushAll(v, got)
}

func pushAll(v *view, got []pending) {
	for _, g := range got {
		r := v.rings[g.crew]
		if r == nil {
			r = &ring{evicted: -1}
			v.rings[g.crew] = r
		}
		r.push(g.it)
	}
}

// Epoch returns crew's current epoch on bus; false for a bus Advance has
// never seen.
func (s *Store) Epoch(bus, crew string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.views[bus]
	if !ok {
		return "", false
	}
	return crewEpoch(v.busEpoch, crew), true
}

// Page returns crew's newest limit entries (1..100, 50 when ≤ 0) whose offset
// is below before, oldest to newest; before ≤ 0 means newest overall. more
// reports whether older entries exist. When the ring no longer holds enough
// of them, Page rescans the file prefix without holding the store's lock and
// yields the same ids the live tail did. Page does not check a cursor's epoch:
// the caller compares it with Epoch first, and with the returned Page.Epoch,
// which names the state the page was cut from.
func (s *Store) Page(bus, crew string, before int64, limit int) (Page, error) {
	if limit <= 0 {
		limit = defaultPageLimit
	}
	limit = min(limit, maxPageLimit)
	if before <= 0 {
		before = math.MaxInt64
	}

	s.mu.Lock()
	v, ok := s.views[bus]
	if !ok {
		s.mu.Unlock()
		return Page{}, ErrNoBus
	}
	p := Page{Epoch: crewEpoch(v.busEpoch, crew), Entries: []Entry{}}
	r := v.rings[crew]
	if r == nil {
		s.mu.Unlock()
		return p, nil
	}
	n := sort.Search(len(r.items), func(i int) bool { return r.items[i].off >= before })
	if n >= limit || r.evicted < 0 {
		for _, it := range r.items[max(0, n-limit):n] {
			p.Entries = append(p.Entries, it.e)
		}
		p.More = n > limit || r.evicted >= 0
		s.mu.Unlock()
		return p, nil
	}
	snap, rescan := v.snapshot, s.rescan
	s.mu.Unlock()

	if rescan == nil {
		for _, it := range r.items[:n] {
			p.Entries = append(p.Entries, it.e)
		}
		p.Entries = p.Entries[max(0, len(p.Entries)-limit):]
		p.More = r.evicted >= 0 || n > limit
		return p, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), rescanTimeout)
	defer cancel()
	batch, err := rescan(ctx, bus)
	if err != nil {
		return Page{}, err
	}
	entries, more := rescanPage(snap, batch, crew, before, limit)
	if cur, _ := s.Epoch(bus, crew); cur != p.Epoch {
		return Page{}, ErrEpoch
	}
	if entries == nil {
		entries = []Entry{}
	}
	p.Entries, p.More = entries, more
	return p, nil
}

// rescanTimeout bounds the whole-log read an older page costs.
const rescanTimeout = 30 * time.Second

// rescanPage folds b from its first row with a fresh Fold and returns crew's
// newest limit entries starting below before and within what the store had
// consumed when the page was cut.
func rescanPage(v snapshot, b Batch, crew string, before int64, limit int) ([]Entry, bool) {
	epoch := crewEpoch(v.busEpoch, crew)
	var fold Fold
	got := []Entry{}
	total := 0
	end := min(before, v.consumed)
	for _, r := range b.Rows {
		if r.Offset >= end {
			break
		}
		c, e, ok := fold.Line(r.Line, r.Offset)
		if !ok || c != crew {
			continue
		}
		e.ID = entryID(epoch, r.Offset)
		total++
		got = append(got, e)
		if len(got) > limit {
			got = got[1:]
		}
	}
	return got, total > limit
}

// Since returns crew's entries with offset above after, oldest to newest. ok
// is false when epoch is not crew's current one on bus, or after cannot be
// served from the ring: older than an entry it pushed out, or past what has
// been read. The caller then restarts from a fresh page.
func (s *Store) Since(bus, crew, epoch string, after int64) ([]Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.views[bus]
	if !ok || crewEpoch(v.busEpoch, crew) != epoch || (after > 0 && after >= v.consumed) {
		return nil, false
	}
	r := v.rings[crew]
	if r == nil {
		return []Entry{}, true
	}
	if after < r.evicted {
		return nil, false
	}
	n := sort.Search(len(r.items), func(i int) bool { return r.items[i].off > after })
	out := make([]Entry, 0, len(r.items)-n)
	for _, it := range r.items[n:] {
		out = append(out, it.e)
	}
	return out, true
}
