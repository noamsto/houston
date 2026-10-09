// Package loadfixture writes a synthetic houston state dir: hook state files
// pointing at large Claude Code transcripts, shaped like a busy host (hundreds
// of sessions, median transcript ~0.8 MB, a long tail past 10 MB). Every byte
// of text is generated; nothing comes from a real transcript.
package loadfixture

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Options sizes a fixture. Zero values take the defaults in Write.
type Options struct {
	Sessions int     // default 250
	Scale    float64 // multiplies every transcript size; default 1
	Seed     int64
	Now      time.Time // default time.Now()
}

// Session describes one generated session.
type Session struct {
	ID             string
	TranscriptPath string
	State          string
	Size           int64
	// Outstanding are the background task ids started early in the
	// transcript and never finished: a restart must still report them.
	Outstanding []string
}

// MarkerFile is written into a fixture's state dir.
const MarkerFile = ".loadfixture"

// Size distribution measured on a busy host: median 0.8 MB, p90 2.7 MB, max 19 MB.
const (
	medianBytes = 800_000
	sigma       = 0.94
	maxBytes    = 19_000_000
)

// Write generates opts.Sessions sessions: state files under
// <stateDir>/claude/<id>.json, transcripts under <projectsDir>/<project>/<id>.jsonl.
// <stateDir>/.loadfixture names the absolute projects dir: tools that write
// into a state dir refuse any dir without it.
func Write(stateDir, projectsDir string, opts Options) ([]Session, error) {
	if opts.Sessions == 0 {
		opts.Sessions = 250
	}
	if opts.Scale == 0 {
		opts.Scale = 1
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	rnd := rand.New(rand.NewSource(opts.Seed)) //nolint:gosec // deterministic fixture, not security
	claudeDir := filepath.Join(stateDir, "claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil { //nolint:gosec // test fixture dir
		return nil, err
	}
	absProjects, err := filepath.Abs(projectsDir)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(stateDir, MarkerFile), []byte(absProjects+"\n"), 0o644); err != nil { //nolint:gosec // test fixture
		return nil, err
	}
	out := make([]Session, 0, opts.Sessions)
	for i := range opts.Sessions {
		size := int64(math.Min(maxBytes, medianBytes*math.Exp(sigma*rnd.NormFloat64())) * opts.Scale)
		if i == 0 {
			size = int64(maxBytes * opts.Scale) // always include the long tail
		}
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		project := fmt.Sprintf("-home-user-git-project%d", i%12)
		path := filepath.Join(projectsDir, project, id+".jsonl")
		outstanding, err := WriteTranscript(path, size, i%5 == 0, rnd, opts.Now)
		if err != nil {
			return nil, err
		}
		state := stateFor(i)
		updated := opts.Now.Add(-time.Duration(i) * time.Minute).Unix()
		st := map[string]any{
			"version":         1,
			"session_id":      id,
			"transcript_path": path,
			"cwd":             "/home/user/git/project" + fmt.Sprint(i%12),
			"git_branch":      fmt.Sprintf("feat/%d-synthetic", i),
			"tmux_session":    "synthetic",
			"tmux_window":     fmt.Sprint(i),
			"tmux_pane":       fmt.Sprintf("%%%d", 90000+i),
			"tmux_server":     "1",
			"state":           state,
			"turn":            i%40 + 1,
			"since":           updated,
			"updated_at":      updated,
			"agent":           "claude",
		}
		b, err := json.Marshal(st)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(claudeDir, id+".json"), b, 0o644); err != nil { //nolint:gosec // test fixture
			return nil, err
		}
		fi, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		out = append(out, Session{ID: id, TranscriptPath: path, State: state, Size: fi.Size(), Outstanding: outstanding})
	}
	return out, nil
}

// stateFor spreads states like the measured host: about a third ended, half
// waiting, the rest mid-turn.
func stateFor(i int) string {
	switch r := i % 100; {
	case r < 35:
		return "ended"
	case r < 86:
		return "waiting"
	case r < 95:
		return "thinking"
	case r < 99:
		return "tool-running"
	default:
		return "waiting:permission"
	}
}

var vocab = strings.Fields(`alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima
mike november oscar papa quebec romeo sierra tango uniform victor whiskey xray yankee zulu
func return error nil struct string int test build go vet lint package import
the a of to in is it for on with as by at from this that be are was`)

func words(rnd *rand.Rand, n int) string {
	var b strings.Builder
	for i := range n {
		if i > 0 {
			if i%14 == 0 {
				b.WriteByte('\n')
			} else {
				b.WriteByte(' ')
			}
		}
		b.WriteString(vocab[rnd.Intn(len(vocab))])
	}
	return b.String()
}

type writer struct {
	w      *bufio.Writer
	n      int64
	uuid   int
	ts     time.Time
	sid    string
	errOut error
}

func (w *writer) rec(m map[string]any) {
	if w.errOut != nil {
		return
	}
	w.uuid++
	m["uuid"] = fmt.Sprintf("u%d", w.uuid)
	m["parentUuid"] = fmt.Sprintf("u%d", w.uuid-1)
	m["sessionId"] = w.sid
	m["timestamp"] = w.ts.UTC().Format("2006-01-02T15:04:05.000Z")
	m["cwd"] = "/home/user/git/project"
	m["gitBranch"] = "feat/synthetic"
	m["version"] = "2.1.0"
	m["isSidechain"] = false
	m["userType"] = "external"
	w.ts = w.ts.Add(2 * time.Second)
	b, err := json.Marshal(m)
	if err != nil {
		w.errOut = err
		return
	}
	b = append(b, '\n')
	n, err := w.w.Write(b)
	w.n += int64(n)
	if err != nil {
		w.errOut = err
	}
}

func (w *writer) assistant(msgID string, blocks ...map[string]any) {
	w.rec(map[string]any{
		"type":      "assistant",
		"requestId": "req_" + msgID,
		"message": map[string]any{
			"id": msgID, "role": "assistant", "model": "claude-synthetic", "type": "message",
			"content": blocks,
			"usage":   map[string]any{"input_tokens": 1200 + w.uuid, "output_tokens": 300, "cache_read_input_tokens": 50000, "cache_creation_input_tokens": 900},
		},
	})
}

func (w *writer) toolResult(id, content string, extra any) {
	w.rec(map[string]any{
		"type":          "user",
		"message":       map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": id, "content": content, "is_error": false}}},
		"toolUseResult": extra,
	})
}

func (w *writer) notification(taskID, toolUseID string) {
	w.rec(map[string]any{
		"type":    "user",
		"origin":  map[string]any{"kind": "task-notification"},
		"message": map[string]any{"role": "user", "content": fmt.Sprintf("<task-notification>\n<task-id>%s</task-id>\n<tool-use-id>%s</tool-use-id>\n<status>completed</status>\n<summary>done</summary>\n</task-notification>", taskID, toolUseID)},
	})
}

// WriteTranscript writes a synthetic transcript of about size bytes. With
// outstanding set, a background shell and a persistent monitor started in the
// first turn stay unfinished; their task ids are returned.
func WriteTranscript(path string, size int64, outstanding bool, rnd *rand.Rand, now time.Time) ([]string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // test fixture dir
		return nil, err
	}
	f, err := os.Create(path) //nolint:gosec // fixture path chosen by the caller
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	sid := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	turns := 1 + size/6000
	w := &writer{w: bufio.NewWriterSize(f, 1<<16), sid: sid, ts: now.Add(-time.Duration(turns) * 10 * time.Second)}

	var open []string
	if outstanding {
		w.assistant("msg_bg", map[string]any{"type": "tool_use", "id": "toolu_bg_shell", "name": "Bash", "input": map[string]any{"command": "go test ./... -count=50", "description": "soak tests", "run_in_background": true}})
		w.toolResult("toolu_bg_shell", "Command running in background with ID: bgshell1. Output is being written to: /tmp/x", map[string]any{"backgroundTaskId": "bgshell1"})
		w.assistant("msg_mon", map[string]any{"type": "tool_use", "id": "toolu_bg_mon", "name": "Monitor", "input": map[string]any{"command": "tail -f log", "description": "watch log", "persistent": true}})
		w.toolResult("toolu_bg_mon", "Monitor started (task bgmon1, persistent)", map[string]any{"taskId": "bgmon1"})
		open = []string{"bgshell1", "bgmon1"}
	}

	for t := 0; w.n < size && w.errOut == nil; t++ {
		w.rec(map[string]any{"type": "user", "origin": map[string]any{"kind": "human"}, "message": map[string]any{"role": "user", "content": words(rnd, 20)}})
		msg := fmt.Sprintf("msg_%d", t)
		w.assistant(msg, map[string]any{"type": "thinking", "thinking": words(rnd, 60), "signature": strings.Repeat("s", 400)})
		w.assistant(msg, map[string]any{"type": "text", "text": words(rnd, 40)})
		tid := fmt.Sprintf("toolu_%d", t)
		switch t % 3 {
		case 0:
			w.assistant(msg, map[string]any{"type": "tool_use", "id": tid, "name": "Bash", "input": map[string]any{"command": "go test ./" + vocab[t%len(vocab)], "description": "run tests"}})
			out := words(rnd, 150+rnd.Intn(300))
			w.toolResult(tid, out, map[string]any{"stdout": out, "stderr": "", "interrupted": false})
		case 1:
			w.assistant(msg, map[string]any{"type": "tool_use", "id": tid, "name": "Read", "input": map[string]any{"file_path": "/home/user/git/project/file.go"}})
			out := words(rnd, 200+rnd.Intn(300))
			w.toolResult(tid, out, map[string]any{"type": "text", "file": map[string]any{"filePath": "/home/user/git/project/file.go", "content": out}})
		default:
			w.assistant(msg, map[string]any{"type": "tool_use", "id": tid, "name": "Edit", "input": map[string]any{"file_path": "/home/user/git/project/file.go", "old_string": words(rnd, 30), "new_string": words(rnd, 30)}})
			w.toolResult(tid, "The file has been updated.", map[string]any{"filePath": "/home/user/git/project/file.go", "structuredPatch": []any{}})
		}
		if t%40 == 39 {
			bid := fmt.Sprintf("bg%d", t)
			btu := "toolu_" + bid
			w.assistant(msg, map[string]any{"type": "tool_use", "id": btu, "name": "Bash", "input": map[string]any{"command": "sleep 5", "run_in_background": true}})
			w.toolResult(btu, "Command running in background with ID: "+bid+".", map[string]any{"backgroundTaskId": bid})
			w.notification(bid, btu)
		}
		w.assistant(msg, map[string]any{"type": "text", "text": words(rnd, 30)})
	}
	if w.errOut != nil {
		return nil, w.errOut
	}
	if err := w.w.Flush(); err != nil {
		return nil, err
	}
	return open, f.Close()
}
