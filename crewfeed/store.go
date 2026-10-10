package crewfeed

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"syscall"
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
	// ErrEpoch is returned when the file was reset while an older page was
	// being rescanned: the cursor no longer names this file.
	ErrEpoch = errors.New("crewfeed: epoch changed")
)

// Store tails each crew bus's events.jsonl into a per-crew ring of entries.
// Advance is the only writer; Epoch, Page and Since are safe from any
// goroutine. Entries handed out share their PR pointer with the ring, so
// callers must not write through it.
type Store struct {
	// adv serializes Advance and guards tails, the parse state no reader
	// touches, so the file I/O runs without mu, and ioErrs, the last I/O
	// error warned about per bus.
	adv    sync.Mutex
	tails  map[string]*tail
	ioErrs map[string]string

	// mu guards views, the published state readers see.
	mu    sync.Mutex
	views map[string]*view
}

// tail is Advance's own position in one bus file.
type tail struct {
	head string // hash of the first complete line, "" while there is none
	off  int64  // consumed through the last '\n'
	gen  uint64
	fold Fold
}

// snapshot is what a rescan needs of a published view.
type snapshot struct {
	path     string
	head     string
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
	return &Store{tails: map[string]*tail{}, ioErrs: map[string]string{}, views: map[string]*view{}}
}

// feedEpoch names a bus file's entries. gen restarts at 0 with houston, so the
// epoch also names the file (path and first line): after a restart, an epoch a
// client already holds can only come back for the same file. The raw path
// never reaches the wire.
func feedEpoch(bus, head string, gen uint64) string {
	sum := sha256.Sum256([]byte(bus + "\x00" + head + "\x00" + strconv.FormatUint(gen, 10)))
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

// Advance folds the lines appended to <bus>/events.jsonl since the last call.
// A shrunk file or a changed first line starts over from byte 0 under a new
// generation. A missing file records the bus with no entries, so its epoch is
// known before the file exists. Call it from one goroutine; concurrent calls
// are serialized.
func (s *Store) Advance(bus string) {
	s.adv.Lock()
	defer s.adv.Unlock()

	path := filepath.Join(bus, "events.jsonl")
	t := s.tails[bus]
	f, size, head, err := openBus(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.warnIO(bus, "open", err)
		// Not evidence the file is gone: keep what was published.
		if t == nil {
			s.record(bus, path, &tail{}, nil)
		}
		return
	}
	if f != nil {
		defer func() { _ = f.Close() }()
	}

	reset := t == nil || head != t.head || size < t.off
	if reset {
		next := &tail{head: head}
		if t != nil {
			next.gen = t.gen + 1
		}
		t = next
		s.tails[bus] = t
	}

	var got []pending
	var readErr error
	if f != nil && size > t.off {
		busEpoch := feedEpoch(bus, t.head, t.gen)
		epochs := map[string]string{}
		// The offset scanLines returns is exact even on a read error, and the
		// fold has seen exactly the lines before it, so a failed read resumes
		// there on the next tick.
		t.off, readErr = scanLines(io.NewSectionReader(f, t.off, size-t.off), t.off, math.MaxInt64, func(line []byte, off int64) {
			if crew, e, ok := t.fold.Line(line, off); ok {
				epoch, seen := epochs[crew]
				if !seen {
					epoch = crewEpoch(busEpoch, crew)
					epochs[crew] = epoch
				}
				e.ID = entryID(epoch, off)
				got = append(got, pending{crew, item{off, e}})
			}
		})
	}
	if readErr != nil {
		s.warnIO(bus, "read", readErr)
	} else {
		delete(s.ioErrs, bus)
	}
	if reset {
		s.record(bus, path, t, got)
		return
	}
	s.publish(bus, t, got)
}

// warnIO logs an I/O error on bus only when it differs from the last one
// logged for it, since Advance meets the same error on every tick.
func (s *Store) warnIO(bus, op string, err error) {
	msg := op + ": " + err.Error()
	if s.ioErrs[bus] == msg {
		return
	}
	s.ioErrs[bus] = msg
	slog.Warn("crew feed: cannot read the bus file", "bus", bus, "op", op, "err", err)
}

// openBus opens a bus file and reads its size and first-line hash. A missing
// file is (nil, 0, "", os.ErrNotExist).
func openBus(path string) (*os.File, int64, string, error) {
	// O_NONBLOCK keeps a FIFO from blocking the open while Advance holds its lock.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // the bus dir is derived from git's common dir, never from a request
	if err != nil {
		return nil, 0, "", err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, "", err
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, 0, "", fmt.Errorf("%s: not a regular file (%s)", path, fi.Mode().Type())
	}
	head, err := headHash(io.NewSectionReader(f, 0, fi.Size()))
	if err != nil {
		_ = f.Close()
		return nil, 0, "", err
	}
	return f, fi.Size(), head, nil
}

// record replaces bus's published view with t's position and entries.
func (s *Store) record(bus, path string, t *tail, got []pending) {
	v := &view{
		snapshot: snapshot{path: path, head: t.head, busEpoch: feedEpoch(bus, t.head, t.gen), consumed: t.off},
		rings:    map[string]*ring{},
	}
	pushAll(v, got)
	s.mu.Lock()
	s.views[bus] = v
	s.mu.Unlock()
}

func (s *Store) publish(bus string, t *tail, got []pending) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.views[bus]
	if !ok {
		return
	}
	v.consumed = t.off
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
	snap := v.snapshot
	s.mu.Unlock()

	entries, more, err := rescan(snap, crew, before, limit)
	if err != nil {
		return Page{}, err
	}
	if cur, _ := s.Epoch(bus, crew); cur != p.Epoch {
		return Page{}, ErrEpoch
	}
	p.Entries, p.More = entries, more
	return p, nil
}

// rescan folds v's file from byte 0 with a fresh Fold and returns crew's
// newest limit entries starting below before. A file that no longer matches v
// (another first line, or shorter than v consumed) is ErrEpoch.
func rescan(v snapshot, crew string, before int64, limit int) ([]Entry, bool, error) {
	f, size, head, err := openBus(v.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, ErrEpoch
	}
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	if head != v.head || size < v.consumed {
		return nil, false, ErrEpoch
	}

	epoch := crewEpoch(v.busEpoch, crew)
	var fold Fold
	got := []Entry{}
	total := 0
	end := min(before, v.consumed)
	read, err := scanLines(io.NewSectionReader(f, 0, v.consumed), 0, end, func(line []byte, off int64) {
		c, e, ok := fold.Line(line, off)
		if !ok || c != crew {
			return
		}
		e.ID = entryID(epoch, off)
		total++
		got = append(got, e)
		if len(got) > limit {
			got = got[1:]
		}
	})
	if err != nil {
		return nil, false, err
	}
	if read < end {
		return nil, false, ErrEpoch
	}
	return got, total > limit, nil
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
