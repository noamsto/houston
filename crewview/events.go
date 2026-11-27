package crewview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ViewEvents is the view name of the cursor-resumable bus tail.
const ViewEvents = "events"

const (
	// execStdoutCap bounds one crew call's stdout: an events page of the
	// largest limit houston asks for fits well inside it.
	execStdoutCap = 64 << 20
	execTimeout   = 20 * time.Second
)

// Event is one bus row of the events view. Row is the line as written;
// houston folds it itself, ID is stable across readers of one log.
type Event struct {
	ID     string          `json:"id"`
	Host   string          `json:"host"`
	TS     int64           `json:"ts"`
	Kind   string          `json:"kind"`
	From   string          `json:"from"`
	To     string          `json:"to"`
	Crew   string          `json:"crew"`
	Branch string          `json:"branch"`
	Text   string          `json:"text"`
	Row    json.RawMessage `json:"row"`
}

// EventsPage is one document of `crew dash --json --view events`. Pass Cursor
// back as since to read only the rows after this page.
type EventsPage struct {
	SchemaVersion int      `json:"schema_version"`
	View          string   `json:"view"`
	GeneratedAt   int64    `json:"generated_at"`
	Host          string   `json:"host"`
	Cursor        string   `json:"cursor"`
	Reset         bool     `json:"reset"`
	More          bool     `json:"more"`
	Events        []Event  `json:"events"`
	Errors        []string `json:"errors"`
}

// DecodeEvents parses one events document, checking view and schema version
// first so a newer producer is reported as unsupported rather than half-read.
func DecodeEvents(raw []byte) (EventsPage, error) {
	var probe struct {
		SchemaVersion int    `json:"schema_version"`
		View          string `json:"view"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return EventsPage{}, fmt.Errorf("crewview: %w", err)
	}
	if probe.View != ViewEvents || probe.SchemaVersion < SchemaMin || probe.SchemaVersion > SchemaMax {
		return EventsPage{}, &UnsupportedError{Version: probe.SchemaVersion, View: probe.View}
	}
	var p EventsPage
	if err := json.Unmarshal(raw, &p); err != nil {
		return EventsPage{}, fmt.Errorf("crewview: %w", err)
	}
	return p, nil
}

// EventOffset is the byte offset an event id starts at ("<offset>-<hash>").
func EventOffset(id string) (int64, bool) {
	off, _, ok := strings.Cut(id, "-")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(off, 10, 64)
	return n, err == nil && n >= 0
}

// Runner runs one crew command in dir and returns its stdout. A non-zero exit
// is an error carrying crew's stderr.
type Runner func(ctx context.Context, dir string, args ...string) ([]byte, error)

// ExecRunner runs the crew binary at crewPath with no shell, in houston's
// process environment minus the crew identity strippedEnv names.
func ExecRunner(crewPath string) Runner {
	return func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, execTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, crewPath, args...) //nolint:gosec // crewPath is resolved by houston at startup; args are houston's own constants and validated ids
		cmd.Dir = dir
		cmd.Env = withoutStripped(os.Environ())
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
		cmd.WaitDelay = 2 * time.Second
		out := &cappedBuffer{max: execStdoutCap}
		var stderr bytes.Buffer
		cmd.Stdout = out
		cmd.Stderr = &limitedWriter{w: &stderr, n: 4 << 10}
		if err := cmd.Run(); err != nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return nil, fmt.Errorf("crew %s: %w: %s", firstArg(args), err, msg)
			}
			return nil, fmt.Errorf("crew %s: %w", firstArg(args), err)
		}
		if out.over {
			return nil, fmt.Errorf("crew %s: output larger than %d bytes", firstArg(args), execStdoutCap)
		}
		return out.Bytes(), nil
	}
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func withoutStripped(vars []string) []string {
	out := make([]string, 0, len(vars))
	for _, kv := range vars {
		k, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(strippedEnv, k) {
			out = append(out, kv)
		}
	}
	return out
}

type cappedBuffer struct {
	bytes.Buffer
	max  int
	over bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.Len()+len(p) > c.max {
		c.over = true
		return len(p), nil
	}
	return c.Buffer.Write(p)
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		m := min(len(p), l.n)
		_, _ = l.w.Write(p[:m])
		l.n -= m
	}
	return len(p), nil
}

// FetchEvents reads one page of the events view of the bus crew resolves in
// root: the tail (limit rows) with no since, the rows after since otherwise.
func FetchEvents(ctx context.Context, run Runner, root, since string, limit int) (EventsPage, error) {
	args := []string{"dash", "--json", "--view", ViewEvents}
	if since != "" {
		args = append(args, "--since", since)
	}
	if limit > 0 {
		args = append(args, "--limit", strconv.Itoa(limit))
	}
	out, err := run(ctx, root, args...)
	if err != nil {
		return EventsPage{}, err
	}
	return DecodeEvents(bytes.TrimSpace(out))
}

// InitResult is what `crew init` prints.
type InitResult struct {
	ID      string `json:"id"`
	Dir     string `json:"dir"`
	Log     string `json:"log"`
	Created bool   `json:"created"`
}

// ErrCrewExists reports that `crew init` found the crew already there.
var ErrCrewExists = errors.New("crewview: crew already exists")

// InitCrew creates crew id's directory and log through `crew init`, so houston
// never makes a directory under the crew state itself. An id that already
// exists is ErrCrewExists: the caller minted it and expects it to be new.
func InitCrew(ctx context.Context, run Runner, root, id string) (InitResult, error) {
	out, err := run(ctx, root, "init", id)
	if err != nil {
		return InitResult{}, err
	}
	var res InitResult
	if err := json.Unmarshal(bytes.TrimSpace(out), &res); err != nil {
		return InitResult{}, fmt.Errorf("crewview: crew init: %w", err)
	}
	if res.ID != id {
		return InitResult{}, fmt.Errorf("crewview: crew init answered crew %q for %q", res.ID, id)
	}
	if !res.Created {
		return res, ErrCrewExists
	}
	return res, nil
}

// CrewRow is one row of `crew crews`. PID is 0 when crew records none.
type CrewRow struct {
	ID      string
	Workers int
	PID     int
	Alive   bool
}

// ListCrews runs `crew crews` (home: `--home`) in root and parses its table:
// a header line, then tab-separated crew_id, last, first, workers, pid, alive,
// with "—" for an unknown pid.
func ListCrews(ctx context.Context, run Runner, root string, home bool) ([]CrewRow, error) {
	args := []string{"crews"}
	if home {
		args = append(args, "--home")
	}
	out, err := run(ctx, root, args...)
	if err != nil {
		return nil, err
	}
	rows := []CrewRow{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 6 || f[0] == "" || f[0] == "crew_id" {
			continue
		}
		r := CrewRow{ID: f[0], Alive: f[5] == "yes"}
		r.Workers, _ = strconv.Atoi(f[3])
		r.PID, _ = strconv.Atoi(f[4])
		rows = append(rows, r)
	}
	return rows, nil
}
