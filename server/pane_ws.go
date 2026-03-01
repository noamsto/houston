package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/noamsto/houston/agents"
	"github.com/noamsto/houston/agents/claude"
	"github.com/noamsto/houston/parser"
	"github.com/noamsto/houston/tmux"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// WebSocket message types
type WSMessage struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type WSOutput struct {
	Data string `json:"data"`
}

type WSMeta struct {
	Agent      agents.AgentType `json:"agent"`
	Mode       string           `json:"mode"`
	Status     string           `json:"status"`
	Choices    []string         `json:"choices,omitempty"`
	Suggestion string           `json:"suggestion,omitempty"`
	StatusLine string           `json:"status_line,omitempty"`
	Activity   string           `json:"activity,omitempty"`
}

type WSInput struct {
	Data string `json:"data"`
}

type WSResize struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

func (s *Server) handlePaneWS(w http.ResponseWriter, r *http.Request, pane tmux.Pane) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "error", err)
		return
	}

	slog.Info("pane websocket connected", "target", pane.Target())

	// Save original window size so we can restore it when this client disconnects.
	// resize-window overrides tmux's automatic sizing, which breaks other clients
	// (e.g. Kitty) viewing the same session.
	origW, origH, sizeErr := s.tmux.GetWindowSize(pane.Session, pane.Window)
	var didResize atomic.Bool

	defer func() {
		_ = conn.Close()
		if didResize.Load() && sizeErr == nil {
			if err := s.tmux.ResizeWindow(pane.Session, pane.Window, origW, origH); err != nil {
				slog.Debug("failed to restore window size", "error", err)
			} else {
				slog.Info("restored window size", "target", pane.Target(), "width", origW, "height", origH)
			}
		}
	}()

	// Keepalive: send pings every 30s, expect pong within 10s.
	// Mobile OSes silently kill idle TCP connections; without this
	// the server holds dead connections until the next write fails.
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	})
	_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))

	// nudge signals the write loop to capture immediately after input
	nudge := make(chan struct{}, 1)

	go s.paneWSReadLoop(conn, pane, nudge, &didResize)
	s.paneWSWriteLoop(conn, pane, nudge)
}

func (s *Server) paneWSReadLoop(conn *websocket.Conn, pane tmux.Pane, nudge chan<- struct{}, didResize *atomic.Bool) {
	defer func() { _ = conn.Close() }()

	for {
		_, msgBytes, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Debug("websocket read error", "error", err)
			}
			return
		}

		var msg WSMessage
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			slog.Debug("websocket unmarshal error", "error", err)
			continue
		}

		switch msg.Type {
		case "input":
			var input WSInput
			if err := json.Unmarshal(msg.Data, &input); err != nil {
				continue
			}
			if err := s.tmux.SendKeys(pane, input.Data, false); err != nil {
				slog.Error("send keys failed", "error", err)
			}
			// Signal write loop to capture immediately
			select {
			case nudge <- struct{}{}:
			default:
			}

		case "resize":
			var resize WSResize
			if err := json.Unmarshal(msg.Data, &resize); err != nil {
				continue
			}
			if resize.Cols > 0 && resize.Rows > 0 {
				// Resize the window (not just the pane) so tmux allows the
				// full dimensions even when another smaller client is attached.
				if err := s.tmux.ResizeWindow(pane.Session, pane.Window, resize.Cols, resize.Rows); err != nil {
					slog.Debug("resize window failed, falling back to pane resize", "error", err)
					_ = s.tmux.ResizePane(pane, "x", resize.Cols)
					_ = s.tmux.ResizePane(pane, "y", resize.Rows)
				}
				didResize.Store(true)
				// Signal write loop to capture immediately with new dimensions
				select {
				case nudge <- struct{}{}:
				default:
				}
			}
		}
	}
}

func (s *Server) paneWSWriteLoop(conn *websocket.Conn, pane tmux.Pane, nudge <-chan struct{}) {
	captureTicker := time.NewTicker(200 * time.Millisecond)
	defer captureTicker.Stop()

	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	var lastOutput string
	var lastMeta WSMeta
	var captureFailures int

	// Get initial pane info for agent detection
	panes, _ := s.tmux.ListPanes(pane.Session, pane.Window)
	var panePath, paneCommand string
	for _, p := range panes {
		if p.Index == pane.Index {
			panePath = p.Path
			paneCommand = p.Command
			break
		}
	}

	for {
		select {
		case <-captureTicker.C:
		case <-nudge:
			// Brief pause to let the process update its output after receiving input
			time.Sleep(50 * time.Millisecond)
			captureTicker.Reset(200 * time.Millisecond)
		case <-pingTicker.C:
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
			continue
		}
		capture, err := s.tmux.CapturePaneWithMode(pane, 500)
		if err != nil {
			captureFailures++
			if captureFailures >= 50 {
				slog.Debug("capture failed too many times, closing", "target", pane.Target(), "error", err)
				return
			}
			slog.Debug("capture failed, will retry", "target", pane.Target(), "error", err, "failures", captureFailures)
			continue
		}
		captureFailures = 0

		// Detect agent and parse state
		paneID := pane.Target()
		agent := s.registry.Detect(paneID, paneCommand, capture.Output)
		parseResult := getAgentState(agent, panePath, capture.Output)
		// Build metadata
		meta := WSMeta{
			Agent:    agent.Type(),
			Mode:     modeToString(parseResult.Mode),
			Activity: parseResult.Activity,
		}

		if len(parseResult.Choices) > 0 {
			meta.Choices = parseResult.Choices
		}

		statusLine := agent.ExtractStatusLine(capture.Output)
		if statusLine != "" {
			meta.StatusLine = statusLine
		}

		if agent.Type() == agents.AgentClaudeCode {
			meta.Suggestion = claude.ExtractSuggestion(capture.Output)
		}

		meta.Status = resultTypeToString(parseResult.Type)

		// Send output if changed
		if capture.Output != lastOutput {
			lastOutput = capture.Output
			outputJSON, _ := json.Marshal(WSOutput{Data: capture.Output})
			msg, _ := json.Marshal(WSMessage{Type: "output", Data: outputJSON})
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		}

		// Send meta if changed
		if !metaEqual(meta, lastMeta) {
			lastMeta = meta
			metaJSON, _ := json.Marshal(meta)
			msg, _ := json.Marshal(WSMessage{Type: "meta", Data: metaJSON})
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		}
	}
}

func metaEqual(a, b WSMeta) bool {
	return a.Agent == b.Agent &&
		a.Mode == b.Mode &&
		a.Status == b.Status &&
		a.Suggestion == b.Suggestion &&
		a.StatusLine == b.StatusLine &&
		a.Activity == b.Activity &&
		slices.Equal(a.Choices, b.Choices)
}

func modeToString(m parser.Mode) string {
	switch m {
	case parser.ModeInsert:
		return "insert"
	case parser.ModeNormal:
		return "normal"
	default:
		return "unknown"
	}
}

func resultTypeToString(t parser.ResultType) string {
	switch t {
	case parser.TypeIdle:
		return "idle"
	case parser.TypeWorking:
		return "working"
	case parser.TypeDone:
		return "done"
	case parser.TypeQuestion:
		return "question"
	case parser.TypeChoice:
		return "choice"
	case parser.TypeError:
		return "error"
	default:
		return "unknown"
	}
}
