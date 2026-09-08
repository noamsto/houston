package server

import (
	"encoding/json"
	"fmt"
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

// wsUpgrader validates Origin against the same allowlist as the HTTP API. A
// browser always sends Origin on a WebSocket handshake, so this is a real
// check, not a formality.
func (s *Server) wsUpgrader() websocket.Upgrader {
	return websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			if s.auth == nil {
				// Unwired gate — refuse rather than accept every origin.
				return false
			}
			if !s.auth.enabled {
				return true
			}
			return originAllowed(r, s.auth.allowedOrigins)
		},
	}
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
	InputText  string           `json:"input_text,omitempty"`
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

type WSDims struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

func (s *Server) handlePaneWS(w http.ResponseWriter, r *http.Request, pane tmux.Pane) {
	up := s.wsUpgrader()
	conn, err := up.Upgrade(w, r, nil)
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

	// Auto-zoom: if window has multiple panes, zoom the target pane so it
	// fills the window — gives a much better view, especially on mobile.
	weZoomed := false
	if count, err := s.tmux.WindowPaneCount(pane); err == nil && count > 1 {
		if zoomed, err := s.tmux.IsZoomed(pane); err == nil && !zoomed {
			if err := s.tmux.ZoomPane(pane); err == nil {
				weZoomed = true
				slog.Debug("auto-zoomed pane", "target", pane.Target())
			}
		}
	}

	// Pause %output for this pane before subscribing so no stale events
	// enter the channel while we capture and send the seed snapshot.
	paused := false
	pauseCmd := fmt.Sprintf("refresh-client -A %s:pause", paneID)
	if _, err := cc.RunCommand(pauseCmd); err == nil {
		paused = true
	} else {
		slog.Debug("pause pane failed, proceeding without", "paneID", paneID, "error", err)
	}

	outputCh := cc.Subscribe(paneID)

	defer func() {
		// Ensure pane is resumed if we exit before the explicit continue
		if paused {
			continueCmd := fmt.Sprintf("refresh-client -A %s:continue", paneID)
			_, _ = cc.RunCommand(continueCmd)
		}
		// Restore zoom state if we auto-zoomed on connect
		if weZoomed {
			_ = s.tmux.ZoomPane(pane) // toggle off
		}
		_ = conn.Close()
		cc.Unsubscribe(paneID, outputCh)
		s.controlMgr.ReleaseClient(pane.Session)
	}()

	// Keepalive
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	})
	_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))

	// Send pane dimensions so the frontend can resize xterm.js to match.
	// Absolute cursor positions in %output depend on matching dimensions.
	if w, h, err := s.tmux.GetPaneSize(pane); err == nil && w > 0 && h > 0 {
		dimsJSON, _ := json.Marshal(WSDims{Cols: w, Rows: h})
		dimsMsg, _ := json.Marshal(WSMessage{Type: "dims", Data: dimsJSON})
		if err := conn.WriteMessage(websocket.TextMessage, dimsMsg); err != nil {
			return
		}
	}

	// Seed: capture-pane provides scrollback history and initial visible content.
	// This is a visual-only snapshot (no terminal state like modes/scroll regions).
	// Pane is paused so no %output races with this seed.
	if seedOutput, err := s.tmux.CapturePane(pane, 500); err == nil && seedOutput != "" {
		outputJSON, _ := json.Marshal(WSOutput{Data: seedOutput})
		msg, _ := json.Marshal(WSMessage{Type: "seed", Data: outputJSON})
		if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			return
		}
	}

	// Force the TUI to redraw via SIGWINCH (resize pane to same dimensions).
	// While paused, the redraw output is buffered by tmux and will be
	// delivered when we continue — giving the client a clean seed+redraw sequence.
	if err := s.tmux.ForceRedraw(pane); err != nil {
		slog.Debug("force redraw failed", "target", pane.Target(), "error", err)
	}

	// Resume %output delivery — buffered events (including any SIGWINCH redraw) flow
	if paused {
		continueCmd := fmt.Sprintf("refresh-client -A %s:continue", paneID)
		if _, err := cc.RunCommand(continueCmd); err != nil {
			slog.Debug("continue pane failed", "paneID", paneID, "error", err)
		}
		paused = false
	}

	go s.paneWSReadLoop(conn, cc, paneID)
	s.paneWSWriteLoop(conn, cc, pane, outputCh)
}

func (s *Server) paneWSWriteLoop(conn *websocket.Conn, cc *tmux.ControlClient, pane tmux.Pane, outputCh <-chan []byte) {
	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	// Meta polling runs in its own goroutine so capture-pane calls
	// never block output delivery to the WebSocket client.
	metaCh := make(chan WSMeta, 1)
	go s.metaPollLoop(pane, cc.Done(), metaCh)

	var lastMeta WSMeta

	for {
		select {
		case data := <-outputCh:
			// Coalesce: drain all buffered chunks into one write
			// to keep the channel drained and reduce WS round-trips.
			buf := append([]byte(nil), data...)
			for {
				select {
				case more := <-outputCh:
					buf = append(buf, more...)
				default:
					goto send
				}
			}
		send:
			outputJSON, _ := json.Marshal(WSOutput{Data: string(buf)})
			msg, _ := json.Marshal(WSMessage{Type: "output", Data: outputJSON})
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}

		case meta := <-metaCh:
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

// metaPollLoop runs agent detection in its own goroutine, sending
// results to metaCh. Exits when done closes.
func (s *Server) metaPollLoop(pane tmux.Pane, done <-chan struct{}, metaCh chan<- WSMeta) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

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
		case <-ticker.C:
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
				meta.InputText = claude.ExtractInputText(capture.Output)
			}
			meta.Status = resultTypeToString(parseResult.Type)

			select {
			case metaCh <- meta:
			default:
			}

		case <-done:
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
			// No-op: CC client size is fixed at 400x200 (set on connect).
			// kitty controls actual pane dimensions via window-size=latest.
		}
	}
}

func metaEqual(a, b WSMeta) bool {
	return a.Agent == b.Agent &&
		a.Mode == b.Mode &&
		a.Status == b.Status &&
		a.Suggestion == b.Suggestion &&
		a.InputText == b.InputText &&
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
