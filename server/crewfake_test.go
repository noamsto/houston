package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/noamsto/houston/crewfeed"
)

// fakeCrewCLI is the test double for the crew CLI's `init` and `crews` verbs:
// it keeps the crews of a made-up state dir, so no test writes under a real
// (or fixture) crew dir.
type fakeCrewCLI struct {
	mu      sync.Mutex
	crews   map[string]*fakeCrew
	calls   [][]string
	initErr error
	initPID int // dispatcher pid a crew starts with once init made it
	listErr error
}

type fakeCrew struct {
	pid  int
	home bool
}

func newFakeCrewCLI() *fakeCrewCLI { return &fakeCrewCLI{crews: map[string]*fakeCrew{}} }

var (
	crewFakesMu sync.Mutex
	crewFakes   = map[*Server]*fakeCrewCLI{}
)

// crewOf is the fake behind s.crewCLI, installed by newDispatchServer.
func crewOf(t *testing.T, s *Server) *fakeCrewCLI {
	t.Helper()
	crewFakesMu.Lock()
	defer crewFakesMu.Unlock()
	f := crewFakes[s]
	if f == nil {
		t.Fatal("server has no fake crew CLI")
	}
	return f
}

func installFakeCrew(s *Server) *fakeCrewCLI {
	f := newFakeCrewCLI()
	s.crewCLI = f.run
	crewFakesMu.Lock()
	crewFakes[s] = f
	crewFakesMu.Unlock()
	return f
}

// add records crew id as listed by `crew crews` (home: also by `--home`) with
// dispatcher pid (0: none).
func (f *fakeCrewCLI) add(id string, pid int, home bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.crews[id] = &fakeCrew{pid: pid, home: home}
}

func (f *fakeCrewCLI) setPID(id string, pid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.crews[id]; c != nil {
		c.pid = pid
	}
}

func (f *fakeCrewCLI) pidOf(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.crews[id]; c != nil {
		return c.pid
	}
	return 0
}

func (f *fakeCrewCLI) has(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.crews[id]
	return ok
}

func (f *fakeCrewCLI) called(verb string) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, c := range f.calls {
		if c[0] == verb {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeCrewCLI) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(args) == 0 {
		return nil, fmt.Errorf("fakeCrewCLI: no crew args")
	}
	f.calls = append(f.calls, append([]string(nil), args...))
	switch args[0] {
	case "init":
		if f.initErr != nil {
			return nil, f.initErr
		}
		if len(args) < 2 {
			return nil, fmt.Errorf("fakeCrewCLI: crew init needs an id")
		}
		id := args[1]
		created := f.crews[id] == nil
		if created {
			f.crews[id] = &fakeCrew{home: true, pid: f.initPID}
		}
		return fmt.Appendf(nil, `{"id":%q,"dir":"/state/crews/%s","log":"/state/crews/%s/events.jsonl","created":%v}`+"\n", id, id, id, created), nil
	case "crews":
		if f.listErr != nil {
			return nil, f.listErr
		}
		home := len(args) > 1 && args[1] == "--home"
		ids := make([]string, 0, len(f.crews))
		for id := range f.crews {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		var b strings.Builder
		b.WriteString("crew_id\tlast_event_s\tfirst_event_s\tworkers\tpid\talive\n")
		for _, id := range ids {
			c := f.crews[id]
			if c == nil || home && !c.home {
				continue
			}
			pid, alive := "—", "—"
			if c.pid > 0 {
				pid, alive = fmt.Sprint(c.pid), "yes"
			}
			fmt.Fprintf(&b, "%s\t1\t1\t0\t%s\t%s\n", id, pid, alive)
		}
		return []byte(b.String()), nil
	}
	return nil, fmt.Errorf("fakeCrewCLI: unexpected crew args %v", args)
}

// advanceFeed is the test double for the crew CLI's events view feeding the
// activity feed: it applies the complete lines appended to <bus>/events.jsonl
// since the last call as rows with their start offsets, and a reset when the
// file shrank, so the feed tests keep writing plain fixtures.
func advanceFeed(s *crewfeed.Store, bus string) {
	feedPosMu.Lock()
	defer feedPosMu.Unlock()
	key := feedPosKey{s, bus}
	pos := feedPositions[key]
	off := pos.off
	data, err := os.ReadFile(filepath.Join(bus, "events.jsonl"))
	if err != nil {
		if !os.IsNotExist(err) {
			return
		}
		data = []byte{}
	}
	head := ""
	if first, _, ok := bytes.Cut(data, []byte{'\n'}); ok {
		sum := sha256.Sum256(first)
		head = hex.EncodeToString(sum[:])
	}
	var b crewfeed.Batch
	if int64(len(data)) < off || (pos.head != "" && head != pos.head) {
		b.Reset, off = true, 0
	}
	for {
		i := bytes.IndexByte(data[off:], '\n')
		if i < 0 {
			break
		}
		b.Rows = append(b.Rows, crewfeed.Row{Offset: off, Line: bytes.Clone(data[off : off+int64(i)])})
		off += int64(i + 1)
	}
	feedPositions[key] = feedPos{off: off, head: head}
	s.Apply(bus, b)
}

type feedPos struct {
	off  int64
	head string
}

type feedPosKey struct {
	s   *crewfeed.Store
	bus string
}

var (
	feedPosMu     sync.Mutex
	feedPositions = map[feedPosKey]feedPos{}
)
