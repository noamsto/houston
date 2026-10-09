package hub

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/noamsto/houston/hook"
	"github.com/noamsto/houston/internal/loadfixture"
)

var fixtureNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// transcriptState is everything a session derives from its transcript.
type transcriptState struct {
	View   SessionView
	Trail  []TrailChip
	Asks   string
	BG     bgTracker
	Offset int64
}

func stateOf(s *Session) transcriptState {
	return transcriptState{View: s.view, Trail: s.trail, Asks: s.asks, BG: s.bg, Offset: s.transcriptOffset}
}

// fastState loads path as a fresh hub does on its first read.
func fastState(t *testing.T, path string) transcriptState {
	t.Helper()
	h := New(t.TempDir(), silentLog())
	h.sessions["s"] = &Session{transcriptPath: path}
	h.refreshTranscript("s")
	return stateOf(h.sessions["s"])
}

// replayState applies every event from byte 0, as the first read did before
// readInitial.
func replayState(t *testing.T, path string) transcriptState {
	t.Helper()
	evs, end, err := ReadTranscriptFrom(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	sess := &Session{transcriptPath: path, transcriptOffset: end}
	for _, ev := range evs {
		applyTranscriptEvent(sess, ev)
	}
	sess.syncTranscriptView(time.Now())
	return stateOf(sess)
}

// tailOffset is where readInitial's tail starts: 0 for a whole-file read.
func tailOffset(t *testing.T, path string) int64 {
	t.Helper()
	_, tail, _, err := readInitial(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) == 0 {
		return 0
	}
	return tail[0].Offset
}

func requireReplayEqual(t *testing.T, path string) {
	t.Helper()
	fast, full := fastState(t, path), replayState(t, path)
	if !reflect.DeepEqual(fast, full) {
		t.Fatalf("fast path differs from full replay:\nfast: %+v\nfull: %+v", fast, full)
	}
}

func TestReadInitialEqualsFullReplay(t *testing.T) {
	rnd := rand.New(rand.NewSource(246)) //nolint:gosec // deterministic fixture
	for _, size := range []int64{50_000, 300_000, 1_000_000, 3_000_000} {
		for _, outstanding := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d-outstanding=%t", size, outstanding), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "t.jsonl")
				if _, err := loadfixture.WriteTranscript(path, size, outstanding, rnd, fixtureNow); err != nil {
					t.Fatal(err)
				}
				if size >= 1_000_000 && tailOffset(t, path) == 0 {
					t.Fatalf("%d bytes read whole; want a tail", size)
				}
				requireReplayEqual(t, path)
			})
		}
	}
}

// filler appends a loadfixture transcript of about size bytes with no
// outstanding background tasks.
func filler(t *testing.T, size int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "filler.jsonl")
	if _, err := loadfixture.WriteTranscript(path, size, false, rand.New(rand.NewSource(1)), fixtureNow); err != nil { //nolint:gosec // deterministic fixture
		t.Fatal(err)
	}
	b, err := os.ReadFile(path) //nolint:gosec // test temp file
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestHubRestartListsBackgroundStartedBeforeTheTail(t *testing.T) {
	persistentMonitor := `{"command":"tail -f log","description":"watch log","persistent":true}`
	monitorTurn := bgStartLine("tu_mon", "Monitor", persistentMonitor, "2026-01-01T00:00:01Z") +
		bgResultLine("tu_mon", "Monitor started (task bmon1, persistent)", false)
	firstTurn := bgStartLine("tu_sh", "Bash", shellInput, "2026-01-01T00:00:00Z") +
		bgResultLine("tu_sh", fmt.Sprintf(shellResult, "bsh1"), false) +
		monitorTurn
	// Both shell lines outgrow foldBackground's 64 KiB read buffer.
	pad := strings.Repeat("x", 200_000)
	longTurn := bgStartLine("tu_sh", "Bash", fmt.Sprintf(`{"command":"sleep 300","description":%q,"run_in_background":true}`, pad), "2026-01-01T00:00:00Z") +
		bgResultLine("tu_sh", fmt.Sprintf(shellResult, "bsh1")+" "+pad, false) +
		monitorTurn
	shellDone := bgNotificationUserLine(notificationText([]string{"bsh1"}, "tu_sh", "completed"))

	for _, tc := range []struct {
		name string
		head string
		want []string
	}{
		{"both outstanding", firstTurn, []string{"bmon1", "bsh1"}},
		{"shell finished before the tail", firstTurn + shellDone, []string{"bmon1"}},
		{"lines over the read buffer", longTurn, []string{"bmon1", "bsh1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "t.jsonl")
			if err := os.WriteFile(path, []byte(tc.head+filler(t, 6*initialTail)), 0o600); err != nil {
				t.Fatal(err)
			}
			if off := tailOffset(t, path); off < 4*initialTail {
				t.Fatalf("tail starts at %d; want the first turn well before it", off)
			}
			writeState(t, dir, hook.SessionState{SessionID: "bg", State: hook.StateWaiting, TranscriptPath: path, UpdatedAt: time.Now().Unix()})

			h := NewWithOptions(dir, Options{ClaudeProjectsDir: "-"}, silentLog())
			startHub(t, h)
			v := findSession(h, "bg")
			if v == nil {
				t.Fatal("session not loaded")
			}
			var ids []string
			for _, b := range v.Background {
				ids = append(ids, b.ID)
			}
			slices.Sort(ids)
			if !slices.Equal(ids, tc.want) {
				t.Fatalf("background = %v, want %v", ids, tc.want)
			}
		})
	}
}

// longRecord is an assistant text line longer than initialTail, so no line
// starts in the last initialTail bytes of a file it ends.
func longRecord() string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":"2026-01-01T00:02:00Z","message":{"role":"assistant","content":[{"type":"text","text":%q}],"usage":{"input_tokens":7,"output_tokens":3}}}`, strings.Repeat("y", 2*initialTail)+" Shall I go on?") + "\n"
}

// A line start lineStartFrom reports must stay one when the record being
// written past it at the Stat lands: readInitial reopens the file to read the
// tail, so a start inside that record would drop it.
func TestLineStartSurvivesTheLastRecordLanding(t *testing.T) {
	head := filler(t, 6*initialTail)
	record := longRecord()
	cut := len(record) - 100
	path := writeJSONL(t, head+record[:cut])

	f, err := os.Open(path) //nolint:gosec // test temp file
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	start, found, err := lineStartFrom(f, fi.Size()-initialTail, fi.Size())
	if err != nil {
		t.Fatal(err)
	}
	appendLines(t, path, record[cut:]+filler(t, 2*initialTail))
	if !found {
		return
	}
	tail, _, err := ReadTranscriptFrom(path, start)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(tail, func(ev TranscriptEvent) bool { return ev.Offset == int64(len(head)) }) {
		t.Fatalf("lineStartFrom reported %d as a line start; the tail read from it skips the record at %d", start, len(head))
	}
}

func TestReadInitialLongLastRecordEqualsFullReplay(t *testing.T) {
	for _, tc := range []struct{ name, trim string }{
		{"newline-terminated", ""},
		{"newline not yet written", "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeJSONL(t, filler(t, 6*initialTail)+strings.TrimSuffix(longRecord(), tc.trim))
			if tailOffset(t, path) == 0 {
				t.Fatal("read whole; want a tail")
			}
			requireReplayEqual(t, path)
		})
	}
}

func TestReadInitialFewToolCallsEqualsFullReplay(t *testing.T) {
	var b strings.Builder
	for i := range 5 {
		id := fmt.Sprintf("tu_%d", i)
		b.WriteString(bgStartLine(id, "Read", `{"file_path":"main.go"}`, "2026-01-01T00:00:00Z"))
		b.WriteString(bgResultLine(id, "contents", i == 3))
	}
	text := strings.Repeat("lorem ipsum ", 30)
	for i := range 3000 {
		fmt.Fprintf(&b, `{"type":"assistant","timestamp":"2026-01-01T00:01:00Z","message":{"role":"assistant","content":[{"type":"text","text":%q}],"usage":{"input_tokens":%d,"output_tokens":%d}}}`+"\n", text, 100+i, 10+i)
	}
	path := writeJSONL(t, b.String())
	if fi, _ := os.Stat(path); fi.Size() <= 2*initialTail {
		t.Fatalf("transcript is %d bytes; want past two tails", fi.Size())
	}
	requireReplayEqual(t, path)
}

func TestReadInitialPiTranscriptEqualsFullReplay(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"type":"session","version":3,"id":"pi-s1","timestamp":"2025-01-01T00:00:00.000Z","cwd":"/w"}` + "\n")
	text := strings.Repeat("lorem ipsum ", 20)
	for i := range 2000 {
		fmt.Fprintf(&b, `{"type":"message","id":"u%d","timestamp":"2025-01-01T00:00:01.000Z","message":{"role":"user","content":%q}}`+"\n", i, text)
		fmt.Fprintf(&b, `{"type":"message","id":"a%d","timestamp":"2025-01-01T00:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":%q},{"type":"toolCall","id":"call_%d","name":"read","arguments":{"path":"/w/main.go"}}],"usage":{"input":%d,"output":12},"stopReason":"toolUse"}}`+"\n", i, text, i, 100+i)
		fmt.Fprintf(&b, `{"type":"message","id":"r%d","timestamp":"2025-01-01T00:00:03.000Z","message":{"role":"toolResult","toolCallId":"call_%d","toolName":"read","content":[{"type":"text","text":"package main"}],"isError":false}}`+"\n", i, i)
	}
	fmt.Fprintf(&b, `{"type":"message","id":"end","timestamp":"2025-01-01T00:00:04.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Shall I go on?"}],"stopReason":"stop"}}`+"\n")
	path := writeJSONL(t, b.String())
	if fi, _ := os.Stat(path); fi.Size() <= 2*initialTail {
		t.Fatalf("transcript is %d bytes; want past two tails", fi.Size())
	}
	requireReplayEqual(t, path)
}

// BenchmarkInitialTranscriptRead compares a full replay with readInitial over
// a loadfixture host, and times a hub scan of it.
func BenchmarkInitialTranscriptRead(b *testing.B) {
	stateDir, projects := b.TempDir(), b.TempDir()
	sessions, err := loadfixture.Write(stateDir, projects, loadfixture.Options{Sessions: 40, Scale: 0.3, Seed: 246, Now: fixtureNow})
	if err != nil {
		b.Fatal(err)
	}
	b.Run("full", func(b *testing.B) {
		for range b.N {
			for _, s := range sessions {
				sess := &Session{}
				evs, _, err := ReadTranscriptFrom(s.TranscriptPath, 0)
				if err != nil {
					b.Fatal(err)
				}
				for _, ev := range evs {
					applyTranscriptEvent(sess, ev)
				}
			}
		}
	})
	b.Run("fast", func(b *testing.B) {
		for range b.N {
			for _, s := range sessions {
				sess := &Session{}
				bg, evs, _, err := readInitial(s.TranscriptPath)
				if err != nil {
					b.Fatal(err)
				}
				sess.bg = bg
				for _, ev := range evs {
					applyTranscriptEvent(sess, ev)
				}
			}
		}
	})
	b.Run("scan", func(b *testing.B) {
		for range b.N {
			h := NewWithOptions(stateDir, Options{ClaudeProjectsDir: "-"}, silentLog())
			if err := h.scan(filepath.Join(stateDir, "claude")); err != nil {
				b.Fatal(err)
			}
		}
	})
}
