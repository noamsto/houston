package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/noamsto/houston/tmux"
)

// maxInputBody matches the legacy send-with-images limit, which is what an
// image upload needs.
const maxInputBody = 50 << 20

// runPaneOps is what the run-addressed terminal routes need from tmux.
type runPaneOps interface {
	ResolvePane(paneID string) (tmux.Pane, error)
	SendKeys(p tmux.Pane, keys string, enter bool) error
	SendSpecialKey(p tmux.Pane, key string) error
}

// terminalKeys mirrors MobileInputBar's quick actions plus the choice ordinals
// a tap sends. It bounds the POST input route's "key" type only: the WebSocket
// "input" message carries raw keystrokes by design.
var terminalKeys = map[string]bool{
	"Escape": true, "C-c": true, "Enter": true, "Tab": true, "BTab": true,
	"Up": true, "Down": true, "M-p": true, "C-o": true, "C-z": true,
	"y": true, "n": true,
	"1": true, "2": true, "3": true, "4": true, "5": true,
	"6": true, "7": true, "8": true, "9": true,
}

type runInput struct {
	Type   string        `json:"type"`
	Text   string        `json:"text"`
	Key    string        `json:"key"`
	Images []imageUpload `json:"images"`
}

// runPane resolves a run to the live tmux pane it runs in, writing the refusal
// itself when there is none. The pane id is re-resolved against tmux on every
// request because the registry's view is only as fresh as its last poll.
func (s *Server) runPane(w http.ResponseWriter, r *http.Request) (tmux.Pane, bool) {
	id := r.PathValue("id")
	if s.runs == nil {
		terminalRefusal(w, id, http.StatusServiceUnavailable, "run registry not started")
		return tmux.Pane{}, false
	}

	run, ok := s.findRun(id)
	if !ok {
		terminalRefusal(w, id, http.StatusNotFound, "no such run")
		return tmux.Pane{}, false
	}
	if !run.Caps.Terminal || run.Tmux == nil || run.Tmux.PaneID == "" {
		terminalRefusal(w, id, http.StatusConflict, "run has no terminal")
		return tmux.Pane{}, false
	}

	pane, err := s.runPanes.ResolvePane(run.Tmux.PaneID)
	if err != nil {
		if errors.Is(err, tmux.ErrPaneNotFound) {
			slog.Debug("resolve run pane failed", "id", id, "pane_id", run.Tmux.PaneID, "error", err)
			terminalRefusal(w, id, http.StatusConflict, "terminal pane is gone")
			return tmux.Pane{}, false
		}
		slog.Warn("resolve run pane failed", "id", id, "pane_id", run.Tmux.PaneID, "error", err)
		terminalRefusal(w, id, http.StatusServiceUnavailable, "tmux unavailable")
		return tmux.Pane{}, false
	}
	return pane, true
}

func terminalRefusal(w http.ResponseWriter, id string, code int, detail string) {
	slog.Info("run terminal refused", "id", id, "status", code, "outcome", detail)
	http.Error(w, detail, code)
}

// handleRunTerminal streams a run's pane over a WebSocket.
//
//	GET /api/runs/{id}/terminal  (upgrade)
//
// Resolution failures are plain HTTP errors, returned before the upgrade.
func (s *Server) handleRunTerminal(w http.ResponseWriter, r *http.Request) {
	pane, ok := s.runPane(w, r)
	if !ok {
		return
	}

	up := s.wsUpgrader()
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "error", err)
		return
	}

	servePane(conn, s.tmux, controlManagerAdapter{mgr: s.controlMgr}, s.registry, pane, metaPollInterval)
}

// handleRunInput sends one piece of input to a run's pane.
//
//	POST /api/runs/{id}/input
//	  {"type":"text","text":"..."}                     literal text, then Enter
//	  {"type":"key","key":"<terminalKeys>"}            one key, no Enter
//	  {"type":"image","text":"...","images":[{...}]}   temp-file paths + text, then Enter
//	→ 204
func (s *Server) handleRunInput(w http.ResponseWriter, r *http.Request) {
	pane, ok := s.runPane(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")

	r.Body = http.MaxBytesReader(w, r.Body, maxInputBody)
	var in runInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			inputOutcome(w, id, pane, in, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		inputOutcome(w, id, pane, in, http.StatusBadRequest, "malformed request body")
		return
	}

	var err error
	switch in.Type {
	case "text":
		if strings.TrimSpace(in.Text) == "" {
			inputOutcome(w, id, pane, in, http.StatusBadRequest, "empty text")
			return
		}
		err = s.runPanes.SendKeys(pane, in.Text, true)
	case "key":
		if !terminalKeys[in.Key] {
			inputOutcome(w, id, pane, in, http.StatusBadRequest, "key not allowed")
			return
		}
		err = s.runPanes.SendSpecialKey(pane, in.Key)
	case "image":
		if len(in.Images) == 0 {
			inputOutcome(w, id, pane, in, http.StatusBadRequest, "no images provided")
			return
		}
		paths, status, saveErr := saveImages(in.Images)
		if saveErr != nil {
			inputOutcome(w, id, pane, in, status, saveErr.Error())
			return
		}
		message := strings.Join(paths, " ")
		if in.Text != "" {
			message += " " + in.Text
		}
		err = s.runPanes.SendKeys(pane, message, true)
	default:
		inputOutcome(w, id, pane, in, http.StatusBadRequest, "unknown input type")
		return
	}

	if err != nil {
		inputOutcome(w, id, pane, in, http.StatusInternalServerError, "failed to send: "+err.Error())
		return
	}
	inputOutcome(w, id, pane, in, http.StatusNoContent, "delivered")
}

// inputOutcome logs the attempt and writes its result. It never logs a client
// string verbatim: only text length, and the type and key names once they are
// known to be ours.
func inputOutcome(w http.ResponseWriter, id string, pane tmux.Pane, in runInput, code int, detail string) {
	attrs := []any{"id", id, "pane", pane.Target(), "status", code, "outcome", detail}
	switch in.Type {
	case "text":
		attrs = append(attrs, "type", in.Type, "text_len", len(in.Text))
	case "key":
		attrs = append(attrs, "type", in.Type)
		if terminalKeys[in.Key] {
			attrs = append(attrs, "key", in.Key)
		}
	case "image":
		attrs = append(attrs, "type", in.Type, "text_len", len(in.Text), "images", len(in.Images))
	}
	slog.Info("run input", attrs...)
	if code == http.StatusNoContent {
		w.WriteHeader(code)
		return
	}
	http.Error(w, detail, code)
}
