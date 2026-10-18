// Package claudemod ingests the records a read-only Claude Code mod posts over
// a Unix socket and folds them into per-session state. Nothing here is wired
// into the runs registry; it exists to compare the mod's signals with hooks.
package claudemod

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// MaxBody is the largest record body Decode accepts.
const MaxBody = 16 << 10

// ErrTooLarge is returned by Decode for a body over MaxBody.
var ErrTooLarge = errors.New("record too large")

// Task is a background task as the mod reports it.
type Task struct {
	ID     string `json:"id"`
	Type   string `json:"type,omitempty"`
	Status string `json:"status,omitempty"`
}

// Ask is one AskUserQuestion question.
type Ask struct {
	Question string   `json:"question"`
	Multi    bool     `json:"multi,omitempty"`
	Options  []string `json:"options"`
}

// Record is one mod event as posted.
type Record struct {
	V    int    `json:"v"`
	SID  string `json:"sid"`
	Boot string `json:"boot"`
	Seq  int64  `json:"seq"`
	TS   int64  `json:"ts"`
	Ev   string `json:"ev"`

	Turn     string `json:"turn,omitempty"`
	Agent    string `json:"agent,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Trigger  string `json:"trigger,omitempty"`
	Tool     string `json:"tool,omitempty"`
	ID       string `json:"id,omitempty"`
	Decision string `json:"decision,omitempty"`
	Type     string `json:"type,omitempty"`
	Message  string `json:"message,omitempty"`
	Task     string `json:"task,omitempty"`

	BG     bool `json:"bg,omitempty"`
	Err    bool `json:"err,omitempty"`
	UserBG bool `json:"user_bg,omitempty"`

	Suggestions int    `json:"suggestions,omitempty"`
	Tasks       []Task `json:"tasks,omitempty"`
	Ask         []Ask  `json:"ask,omitempty"`
}

// Event names a record's ev can carry.
const (
	evSessionStart = "session.start"
	evSessionEnd   = "session.end"
	evCompact      = "compact"
	evTurnStart    = "turn.start"
	evTurnComplete = "turn.complete"
	evToolStart    = "tool.start"
	evToolEnd      = "tool.end"
	evToolCheck    = "tool.check"
	evNotification = "notification"
	evPermission   = "permission"
	evStop         = "stop"
	evTaskEnd      = "task.end"
)

var validEv = map[string]bool{
	evSessionStart: true, evSessionEnd: true, evCompact: true, evTurnStart: true,
	evTurnComplete: true, evToolStart: true, evToolEnd: true, evToolCheck: true,
	evNotification: true, evPermission: true, evStop: true, evTaskEnd: true,
}

// Decode reads exactly one record from r. It returns ErrTooLarge for a body
// over MaxBody and rejects invalid JSON, trailing data, v != 1, an empty sid
// or boot, seq < 1 and an unknown ev.
func Decode(r io.Reader) (Record, error) {
	body, err := io.ReadAll(io.LimitReader(r, MaxBody+1))
	if err != nil {
		return Record{}, fmt.Errorf("read record: %w", err)
	}
	if len(body) > MaxBody {
		return Record{}, ErrTooLarge
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	var rec Record
	if err := dec.Decode(&rec); err != nil {
		return Record{}, fmt.Errorf("decode record: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Record{}, errors.New("trailing data after record")
	}
	switch {
	case rec.V != 1:
		return Record{}, fmt.Errorf("unsupported version %d", rec.V)
	case rec.SID == "":
		return Record{}, errors.New("empty sid")
	case rec.Boot == "":
		return Record{}, errors.New("empty boot")
	case rec.Seq < 1:
		return Record{}, fmt.Errorf("invalid seq %d", rec.Seq)
	case !validEv[rec.Ev]:
		return Record{}, fmt.Errorf("unknown ev %q", rec.Ev)
	}
	return rec, nil
}
