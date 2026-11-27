// Package crewview decodes the per-view JSON that `crew dash --json --watch`
// emits, one document per line. It imports only the standard library.
package crewview

import (
	"encoding/json"
	"fmt"
)

const (
	OriginLocal  = "local"
	OriginRemote = "remote_worker"
	OriginPeer   = "peer"
)

const ViewOverview = "overview"

// SchemaMin and SchemaMax bound the schema_version Decode accepts.
const SchemaMin, SchemaMax = 1, 1

// Overview is one line of the overview view.
type Overview struct {
	SchemaVersion int      `json:"schema_version"`
	View          string   `json:"view"`
	GeneratedAt   int64    `json:"generated_at"`
	Attention     []Item   `json:"attention"`
	Workers       []Worker `json:"workers"`
	Errors        []string `json:"errors"`
}

type Worker struct {
	Crew   string  `json:"crew"`
	Branch string  `json:"branch"`
	Name   *string `json:"name"`
	Host   string  `json:"host"`
	Origin string  `json:"origin"`
	State  string  `json:"state"`
	Detail *string `json:"detail"`
	Tier   *string `json:"tier"`
	Engine *string `json:"engine"`
	Model  *string `json:"model"`
	Title  *string `json:"title"`
	PRURL  *string `json:"pr_url"`
	PR     *int    `json:"pr"`
	AgeSec *int64  `json:"age_sec"`

	// Unix ms: the start of the current blocked episode, and the dispatcher
	// reply that moved it to state "answered".
	BlockedSince *int64 `json:"blocked_since"`
	AnsweredAt   *int64 `json:"answered_at"`

	// Session is the newest worker session, Live and ExitSuspect the worker's
	// own liveness as crew judges it; nil when unknown.
	Session     *string     `json:"session"`
	Live        *bool       `json:"live"`
	ExitSuspect *bool       `json:"exit_suspect"`
	Dispatcher  *Dispatcher `json:"dispatcher"`
}

// Dispatcher is the registration of a crew's dispatcher: where it lives
// relative to the bus the row was read from, its pid, pane and a signal-0
// verdict. PID, Pane and Alive are nil when unknown.
type Dispatcher struct {
	Where string  `json:"where"`
	PID   *int    `json:"pid"`
	Pane  *string `json:"pane"`
	Alive *bool   `json:"alive"`
}

// Item is one attention entry; Action is nil when the item has no runnable
// command (peer items).
type Item struct {
	ID       string  `json:"id"`
	Crew     string  `json:"crew"`
	Branch   string  `json:"branch"`
	Worker   string  `json:"worker"`
	Host     string  `json:"host"`
	Origin   string  `json:"origin"`
	Severity string  `json:"severity"`
	Kind     string  `json:"kind"`
	Message  string  `json:"message"`
	AgeSec   int64   `json:"age_sec"`
	Action   *Action `json:"action"`
}

type Action struct {
	Key        string   `json:"key"`
	Verb       string   `json:"verb"`
	Label      string   `json:"label"`
	Command    string   `json:"command"`
	Argv       []string `json:"argv"`
	NeedsInput bool     `json:"needs_input"`
	PR         *int     `json:"pr"`
}

// UnsupportedError reports a well-formed line of a view or schema version
// houston does not read.
type UnsupportedError struct {
	Version int
	View    string
}

func (e *UnsupportedError) Error() string {
	if e.View != ViewOverview {
		return fmt.Sprintf("crew dash view %q is not supported (houston reads %s)", e.View, ViewOverview)
	}
	reads := fmt.Sprint(SchemaMin)
	if SchemaMax != SchemaMin {
		reads = fmt.Sprintf("%d-%d", SchemaMin, SchemaMax)
	}
	return fmt.Sprintf("crew dash schema_version %d is not supported (houston reads %s)", e.Version, reads)
}

// Decode parses one overview line. The version and view are checked before the
// full decode so a newer producer is reported as unsupported rather than
// half-read.
func Decode(line []byte) (Overview, error) {
	var probe struct {
		SchemaVersion int    `json:"schema_version"`
		View          string `json:"view"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return Overview{}, fmt.Errorf("crewview: %w", err)
	}
	if probe.View != ViewOverview || probe.SchemaVersion < SchemaMin || probe.SchemaVersion > SchemaMax {
		return Overview{}, &UnsupportedError{Version: probe.SchemaVersion, View: probe.View}
	}
	var ov Overview
	if err := json.Unmarshal(line, &ov); err != nil {
		return Overview{}, fmt.Errorf("crewview: %w", err)
	}
	return ov, nil
}
