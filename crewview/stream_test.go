package crewview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	testBackoffMin = 20 * time.Millisecond
	testBackoffMax = 80 * time.Millisecond
	testFresh      = 200 * time.Millisecond
	testStale      = 150 * time.Millisecond
)

const (
	remoteLine = `{"schema_version":1,"view":"overview","generated_at":1,"attention":[],"workers":[{"crew":"c1","branch":"feat/r","host":"halo","origin":"remote_worker","state":"working"}],"errors":[]}`
	localLine  = `{"schema_version":1,"view":"overview","generated_at":1,"attention":[],"workers":[{"crew":"c1","branch":"feat/l","host":"","origin":"local","state":"working"}],"errors":[]}`
	schema2    = `{"schema_version":2,"view":"overview"}`
)

// TestHelperProcess is the fake `crew dash` the Stream tests launch; it does
// nothing unless GO_WANT_HELPER_PROCESS=1.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	run := 1
	if path := os.Getenv("HELPER_COUNTER"); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(3)
		}
		fmt.Fprintln(f, time.Now().UnixNano())
		_ = f.Close()
		b, _ := os.ReadFile(path)
		run = strings.Count(string(b), "\n")
	}
	linger := func() {
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	golden := func() string {
		b, err := os.ReadFile(os.Getenv("HELPER_GOLDEN"))
		if err != nil {
			os.Exit(3)
		}
		return strings.TrimSpace(string(b))
	}
	switch os.Getenv("HELPER_MODE") {
	case "golden":
		fmt.Println(golden())
		linger()
	case "line":
		fmt.Println(os.Getenv("HELPER_LINE"))
		linger()
	case "old":
		fmt.Fprintln(os.Stderr, `crew dash: unknown argument "--view"`)
		fmt.Fprintln(os.Stderr, "usage: crew dash [--json] [--watch]")
		os.Exit(2)
	case "once-then-fail":
		if run == 1 {
			fmt.Println(golden())
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(1)
	case "slow":
		d, _ := time.ParseDuration(os.Getenv("HELPER_DELAY"))
		time.Sleep(d)
		fmt.Println(os.Getenv("HELPER_LINE"))
		linger()
	case "huge":
		fmt.Printf("{\"x\":\"%s\"}\n", strings.Repeat("a", 5<<20))
		fmt.Println(golden())
		linger()
	case "grandchild":
		c := exec.Command("sleep", "60")
		if err := c.Start(); err != nil {
			os.Exit(3)
		}
		b, _ := json.Marshal(Overview{SchemaVersion: 1, View: ViewOverview, Errors: []string{strconv.Itoa(c.Process.Pid)}})
		fmt.Println(string(b))
		linger()
	case "env":
		var set []string
		for _, k := range []string{"CREW_BIN", "CREW_ID", "CREW_WORKER_ID", "TMUX_PANE", "DISPATCH_SPEC", "KEEP"} {
			if _, ok := os.LookupEnv(k); ok {
				set = append(set, k)
			}
		}
		b, _ := json.Marshal(Overview{SchemaVersion: 1, View: ViewOverview, Errors: set})
		fmt.Println(string(b))
		linger()
	}
	os.Exit(4)
}

type helper struct {
	mode, line, delay string
	counter           string
}

// testStream builds a Stream that launches TestHelperProcess with short
// durations. The base env carries the five keys Stream must strip.
func testStream(t *testing.T, h helper) *Stream {
	t.Helper()
	golden, err := filepath.Abs("testdata/overview.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	s := NewStream("crew-unused", t.TempDir())
	s.env = append(os.Environ(), "CREW_BIN=x", "CREW_ID=x", "CREW_WORKER_ID=x", "TMUX_PANE=%1", "DISPATCH_SPEC=x", "KEEP=1")
	s.command = func(_ context.Context, _ string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
		cmd.Env = []string{
			"GO_WANT_HELPER_PROCESS=1",
			"HELPER_MODE=" + h.mode,
			"HELPER_LINE=" + h.line,
			"HELPER_DELAY=" + h.delay,
			"HELPER_COUNTER=" + h.counter,
			"HELPER_GOLDEN=" + golden,
		}
		return cmd
	}
	s.backoffMin, s.backoffMax, s.fresh, s.stale = testBackoffMin, testBackoffMax, testFresh, testStale
	return s
}

func startStream(t *testing.T, h helper) *Stream {
	t.Helper()
	s := testStream(t, h)
	s.Start(context.Background())
	t.Cleanup(s.Stop)
	return s
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// newCounter creates the file the helper appends its start timestamps to.
func newCounter(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "count")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// starts reads the helper's start timestamps from its counter file.
func starts(t *testing.T, path string) []time.Time {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []time.Time
	for _, f := range strings.Fields(string(b)) {
		n, err := strconv.ParseInt(f, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, time.Unix(0, n))
	}
	return out
}

func TestStreamGolden(t *testing.T) {
	s := startStream(t, helper{mode: "golden"})
	waitFor(t, "golden view", 5*time.Second, func() bool {
		_, _, ok := s.Latest()
		return ok
	})
	ov, st, _ := s.Latest()
	if st.State != StateOK {
		t.Errorf("state = %q, want ok", st.State)
	}
	if len(ov.Workers) != 13 || len(ov.Attention) != 12 {
		t.Errorf("got %d workers, %d attention", len(ov.Workers), len(ov.Attention))
	}
}

func TestStreamTooOldCrew(t *testing.T) {
	counter := newCounter(t)
	s := testStream(t, helper{mode: "old", counter: counter})
	s.backoffMax = 200 * time.Millisecond
	s.Start(context.Background())
	t.Cleanup(s.Stop)

	waitFor(t, "unsupported", 5*time.Second, func() bool {
		_, st, _ := s.Latest()
		return st.State == StateUnsupported
	})
	_, st, ok := s.Latest()
	if ok || !strings.Contains(st.Notice, "--view") {
		t.Errorf("ok = %v, notice = %q", ok, st.Notice)
	}
	waitFor(t, "three starts", 5*time.Second, func() bool { return len(starts(t, counter)) >= 3 })
	ts := starts(t, counter)
	for i := 1; i < len(ts); i++ {
		if gap := ts[i].Sub(ts[i-1]); gap < s.backoffMax {
			t.Errorf("restart %d after %v, want >= %v", i, gap, s.backoffMax)
		}
	}
}

func TestStreamUnsupportedSchema(t *testing.T) {
	s := startStream(t, helper{mode: "line", line: schema2})
	waitFor(t, "unsupported", 5*time.Second, func() bool {
		_, st, _ := s.Latest()
		return st.State == StateUnsupported
	})
	ov, st, ok := s.Latest()
	want := (&UnsupportedError{Version: 2, View: ViewOverview}).Error()
	if ok || ov.SchemaVersion != 0 || st.Notice != want {
		t.Errorf("ok = %v, schema = %d, notice = %q, want %q", ok, ov.SchemaVersion, st.Notice, want)
	}
}

func TestStreamStaleAfterFailure(t *testing.T) {
	counter := newCounter(t)
	s := startStream(t, helper{mode: "once-then-fail", counter: counter})
	waitFor(t, "first view", 5*time.Second, func() bool {
		_, _, ok := s.Latest()
		return ok
	})
	s.mu.Lock()
	last := s.lastLine
	s.mu.Unlock()

	var dropped time.Time
	waitFor(t, "view to go stale", 5*time.Second, func() bool {
		_, _, ok := s.Latest()
		dropped = time.Now()
		return !ok
	})
	if dropped.Sub(last) < s.stale {
		t.Errorf("view dropped %v after the last line, want >= %v", dropped.Sub(last), s.stale)
	}
	waitFor(t, "a failing restart", 5*time.Second, func() bool { return len(starts(t, counter)) >= 2 })
	waitFor(t, "boom notice", 5*time.Second, func() bool {
		_, st, _ := s.Latest()
		return st.State == StateUnavailable && strings.Contains(st.Notice, "boom")
	})
	if _, _, ok := s.Latest(); ok {
		t.Error("stale view came back")
	}
}

func TestStreamFreshnessRestartsRemote(t *testing.T) {
	counter := newCounter(t)
	s := startStream(t, helper{mode: "line", line: remoteLine, counter: counter})
	waitFor(t, "remote view", 5*time.Second, func() bool {
		_, _, ok := s.Latest()
		return ok
	})
	// The view outlives stale here (fresh > stale), so it must stay shown
	// through the planned restart until the new process writes.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, st, ok := s.Latest(); !ok || st.State != StateOK {
			t.Fatalf("ok = %v, state = %q during a freshness restart", ok, st.State)
		}
		if ts := starts(t, counter); len(ts) >= 2 {
			s.mu.Lock()
			restarted := s.lastLine.After(ts[1])
			s.mu.Unlock()
			if restarted {
				if gap := ts[1].Sub(ts[0]); gap < s.fresh {
					t.Errorf("restarted after %v, want >= %v", gap, s.fresh)
				}
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for a freshness restart")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestStreamFreshnessLeavesLocal(t *testing.T) {
	counter := newCounter(t)
	s := startStream(t, helper{mode: "line", line: localLine, counter: counter})
	waitFor(t, "local view", 5*time.Second, func() bool {
		_, _, ok := s.Latest()
		return ok
	})
	time.Sleep(3 * s.fresh)
	if n := len(starts(t, counter)); n != 1 {
		t.Errorf("local-only view: %d starts, want 1", n)
	}
}

func TestStreamSlowFirstEmit(t *testing.T) {
	counter := newCounter(t)
	s := startStream(t, helper{mode: "slow", line: remoteLine, delay: (2 * testFresh).String(), counter: counter})
	waitFor(t, "slow view", 5*time.Second, func() bool {
		_, _, ok := s.Latest()
		return ok
	})
	if n := len(starts(t, counter)); n != 1 {
		t.Fatalf("the first line came after %d starts, want the slow first process", n)
	}
	// The restarted process is slow too; the previous process's last line
	// must not count against it.
	waitFor(t, "a freshness restart", 5*time.Second, func() bool { return len(starts(t, counter)) >= 2 })
	ts := starts(t, counter)
	if len(ts) < 2 {
		t.Fatalf("%d starts", len(ts))
	}
	second := ts[1]
	waitFor(t, "the restarted process's line", 5*time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.lastLine.After(second)
	})
}

func TestStreamSkipsOverCapLine(t *testing.T) {
	s := startStream(t, helper{mode: "huge"})
	waitFor(t, "view after huge line", 10*time.Second, func() bool {
		_, _, ok := s.Latest()
		return ok
	})
	ov, _, _ := s.Latest()
	if len(ov.Workers) != 13 {
		t.Errorf("got %d workers, want the golden 13", len(ov.Workers))
	}
	s.mu.Lock()
	skipped := s.skipped
	s.mu.Unlock()
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
}

func TestStreamStopKillsGroup(t *testing.T) {
	for _, viaCtx := range []bool{false, true} {
		t.Run(fmt.Sprintf("ctx=%v", viaCtx), func(t *testing.T) {
			s := testStream(t, helper{mode: "grandchild"})
			ctx, cancel := context.WithCancel(context.Background())
			s.Start(ctx)
			t.Cleanup(s.Stop)
			t.Cleanup(cancel)
			waitFor(t, "grandchild pid", 5*time.Second, func() bool {
				ov, _, ok := s.Latest()
				return ok && len(ov.Errors) == 1
			})
			ov, _, _ := s.Latest()
			pid, err := strconv.Atoi(ov.Errors[0])
			if err != nil {
				t.Fatal(err)
			}
			if viaCtx {
				cancel()
			} else {
				s.Stop()
			}
			waitFor(t, "grandchild to die", 2*time.Second, func() bool {
				return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
			})
		})
	}
}

func TestStreamStripsEnv(t *testing.T) {
	s := startStream(t, helper{mode: "env"})
	waitFor(t, "env view", 5*time.Second, func() bool {
		_, _, ok := s.Latest()
		return ok
	})
	ov, _, _ := s.Latest()
	if !slices.Equal(ov.Errors, []string{"KEEP"}) {
		t.Errorf("child env had %v, want only KEEP", ov.Errors)
	}
}

func TestManager(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := NewManager(ctx, "crew-unused")
	var made []*Stream
	m.newStream = func(root string) *Stream {
		s := testStream(t, helper{mode: "golden"})
		s.root = root
		made = append(made, s)
		return s
	}
	t.Cleanup(func() {
		cancel()
		for _, s := range made {
			<-s.done
		}
	})

	rootA := t.TempDir()
	m.Ensure("a", rootA)
	m.Ensure("a", t.TempDir())
	m.Ensure("b", t.TempDir())
	if len(made) != 2 {
		t.Fatalf("made %d streams, want 2", len(made))
	}
	if got, ok := m.Root("a"); !ok || got != rootA {
		t.Errorf("Root(a) = %q, %v; want the first root", got, ok)
	}
	if _, ok := m.Root("nope"); ok {
		t.Error("Root(nope) found a bus")
	}
	if ov, st, ok := m.Latest("nope"); ok || st != (Status{}) || ov.SchemaVersion != 0 {
		t.Errorf("unknown bus: ok = %v, status = %+v", ok, st)
	}
	waitFor(t, "bus a view", 5*time.Second, func() bool {
		_, _, ok := m.Latest("a")
		return ok
	})

	m.Retain(map[string]bool{"b": true})
	select {
	case <-made[0].done:
	case <-time.After(5 * time.Second):
		t.Fatal("bus a's stream still running after Retain")
	}
	if _, st, ok := m.Latest("a"); ok || st != (Status{}) {
		t.Errorf("retained-out bus still known: ok = %v, status = %+v", ok, st)
	}
	if _, ok := m.Root("a"); ok {
		t.Error("retained-out bus still has a root")
	}
	select {
	case <-made[1].done:
		t.Fatal("bus b's stream stopped")
	default:
	}
}

func TestManagerCloseKillsEveryStream(t *testing.T) {
	m := NewManager(context.Background(), "crew-unused")
	m.newStream = func(root string) *Stream {
		return testStream(t, helper{mode: "grandchild"})
	}
	t.Cleanup(m.Close)
	m.Ensure("a", t.TempDir())
	m.Ensure("b", t.TempDir())

	var pids []int
	for _, bus := range []string{"a", "b"} {
		waitFor(t, "grandchild pid of "+bus, 5*time.Second, func() bool {
			ov, _, ok := m.Latest(bus)
			return ok && len(ov.Errors) == 1
		})
		ov, _, _ := m.Latest(bus)
		pid, err := strconv.Atoi(ov.Errors[0])
		if err != nil {
			t.Fatal(err)
		}
		pids = append(pids, pid)
	}

	m.Close()
	for _, pid := range pids {
		waitFor(t, "grandchild to die", 2*time.Second, func() bool {
			return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
		})
	}
	if _, ok := m.Root("a"); ok {
		t.Error("bus a still known after Close")
	}
}
