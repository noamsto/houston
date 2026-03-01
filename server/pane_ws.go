package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
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
	WindowName string           `json:"window_name,omitempty"`
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

	// Look up tmux pane ID (%N format) for control mode routing
	paneID, err := s.tmux.GetPaneID(pane)
	if err != nil {
		slog.Error("failed to get pane ID", "target", pane.Target(), "error", err)
		_ = conn.Close()
		return
	}

	// Get or create control client for this session (ref-counted)
	cc, err := s.controlMgr.GetClient(pane.Session)
	if err != nil {
		slog.Error("failed to get control client", "session", pane.Session, "error", err)
		_ = conn.Close()
		return
	}

	slog.Info("pane websocket connected (control mode)", "target", pane.Target(), "paneID", paneID)

	// Subscribe BEFORE capture so we buffer any output that arrives during seed
	outputCh := cc.Subscribe(paneID)

	defer func() {
		_ = conn.Close()
		cc.Unsubscribe(paneID, outputCh)
		s.controlMgr.ReleaseClient(pane.Session)
	}()

	// Keepalive
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	})
	_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))

	// Seed: send current pane state as initial snapshot
	if seedOutput, err := s.tmux.CapturePaneWithMode(pane, 500); err == nil && seedOutput.Output != "" {
		outputJSON, _ := json.Marshal(WSOutput{Data: seedOutput.Output})
		msg, _ := json.Marshal(WSMessage{Type: "seed", Data: outputJSON})
		if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}

	// Drain any buffered incremental output that arrived during the seed capture
drainLoop:
	for {
		select {
		case data := <-outputCh:
			outputJSON, _ := json.Marshal(WSOutput{Data: string(data)})
			msg, _ := json.Marshal(WSMessage{Type: "output", Data: outputJSON})
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		default:
			break drainLoop
		}
	}

	go s.paneWSReadLoop(conn, cc, paneID)
	s.paneWSWriteLoop(conn, cc, pane, outputCh)
}

func (s *Server) paneWSWriteLoop(conn *websocket.Conn, cc *tmux.ControlClient, pane tmux.Pane, outputCh <-chan []byte) {
	metaTicker := time.NewTicker(1 * time.Second)
	defer metaTicker.Stop()

	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	var lastMeta WSMeta

	// Fetch initial pane info for agent detection
	panes, _ := s.tmux.ListPanes(pane.Session, pane.Window)
	var panePath, paneCommand string
	for _, p := range panes {
		if p.Index == pane.Index {
			panePath = p.Path
			paneCommand = p.Command
			break
		}
	}
	var windowName string
	if windows, err := s.tmux.ListWindows(pane.Session); err == nil {
		for _, w := range windows {
			if w.Index == pane.Window {
				windowName = w.Name
				break
			}
		}
	}

	for {
		select {
		case data := <-outputCh:
			outputJSON, _ := json.Marshal(WSOutput{Data: string(data)})
			msg, _ := json.Marshal(WSMessage{Type: "output", Data: outputJSON})
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}

		case <-metaTicker.C:
			// Agent detection via capture-pane (side channel for state parsing)
			capture, err := s.tmux.CapturePaneWithMode(pane, 500)
			if err != nil {
				continue
			}
			agent := s.registry.Detect(pane.Target(), paneCommand, capture.Output)
			parseResult := getAgentState(agent, panePath, capture.Output)

			meta := WSMeta{
				Agent:      agent.Type(),
				Mode:       modeToString(parseResult.Mode),
				Activity:   parseResult.Activity,
				WindowName: windowName,
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

			if !metaEqual(meta, lastMeta) {
				lastMeta = meta
				metaJSON, _ := json.Marshal(meta)
				msg, _ := json.Marshal(WSMessage{Type: "meta", Data: metaJSON})
				if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					return
				}
			}

		case <-pingTicker.C:
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}

		case <-cc.Done():
			return
		}
	}
}

func (s *Server) paneWSReadLoop(conn *websocket.Conn, cc *tmux.ControlClient, paneID string) {
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
			if err := cc.SendKeys(paneID, input.Data); err != nil {
				slog.Error("send keys failed", "error", err)
			}

		case "resize":
			var resize WSResize
			if err := json.Unmarshal(msg.Data, &resize); err != nil {
				continue
			}
			if resize.Cols > 0 && resize.Rows > 0 {
				if err := cc.SetClientSize(resize.Cols, resize.Rows); err != nil {
					slog.Debug("set client size failed", "error", err)
				}
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
		a.WindowName == b.WindowName &&
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
