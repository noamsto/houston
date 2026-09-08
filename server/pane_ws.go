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

// wsUpgrader validates Origin against the same allowlist as the HTTP API.
// This runs regardless of whether auth is enabled: -no-auth disables the
// token requirement, not cross-origin drivability — see originAllowed's
// comment in auth.go for why an absent Origin is still safe to allow.
func (s *Server) wsUpgrader() websocket.Upgrader {
	return websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			if s.auth == nil {
				// Unwired gate — refuse rather than accept every origin.
				return false
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

	sub := cc.Subscribe(paneID)

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
		cc.Unsubscribe(paneID, sub)
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

	// Seed: capture-pane provides scrollback history and initial visible
	// content. Pane is paused so no %output races with this seed. A capture
	// failure costs scrollback, not the connection.
	if _, err := s.sendSeed(conn, pane); err != nil {
		return
	}

	// Force the TUI to redraw via SIGWINCH (resize pane to same dimensions),
	// prompting it to repaint any garbled state. tmux discards %output while
	// paused, so the redraw's own output is not queued for later delivery —
	// this just nudges the TUI before we resume normal streaming below.
	if err := s.tmux.ForceRedraw(pane); err != nil {
		slog.Debug("force redraw failed", "target", pane.Target(), "error", err)
	}

	// Resume %output delivery — normal streaming resumes from here.
	if paused {
		continueCmd := fmt.Sprintf("refresh-client -A %s:continue", paneID)
		if _, err := cc.RunCommand(continueCmd); err != nil {
			slog.Debug("continue pane failed", "paneID", paneID, "error", err)
		}
		paused = false
	}

	go s.paneWSReadLoop(conn, cc, paneID)
	s.paneWSWriteLoop(conn, cc, pane, sub)
}

// sendSeed pushes a capture-pane snapshot as the authoritative screen state.
// Used on connect and again after any gap in the control stream.
//
// ok reports whether a seed was delivered; err is non-nil only when the
// WebSocket write failed, which is always fatal. A capture-pane failure is
// (false, nil), leaving the caller to decide whether it can proceed without
// a seed.
func (s *Server) sendSeed(conn *websocket.Conn, pane tmux.Pane) (ok bool, err error) {
	seed, ok := s.captureSeed(pane)
	if !ok {
		return false, nil
	}
	if err := writeSeed(conn, seed); err != nil {
		return false, err
	}
	return true, nil
}

// captureSeed takes a capture-pane snapshot. The bool reports whether a
// non-empty seed was captured; neither a capture failure nor an empty pane
// is an error.
func (s *Server) captureSeed(pane tmux.Pane) (string, bool) {
	seed, capErr := s.tmux.CapturePane(pane, 500)
	if capErr != nil {
		slog.Debug("capture-pane failed", "target", pane.Target(), "error", capErr)
		return "", false
	}
	if seed == "" {
		return "", false
	}
	return seed, true
}

func writeSeed(conn *websocket.Conn, seed string) error {
	outputJSON, _ := json.Marshal(WSOutput{Data: seed})
	msg, _ := json.Marshal(WSMessage{Type: "seed", Data: outputJSON})
	return conn.WriteMessage(websocket.TextMessage, msg)
}

func (s *Server) paneWSWriteLoop(conn *websocket.Conn, cc *tmux.ControlClient, pane tmux.Pane, sub *tmux.PaneSub) {
	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	// Meta polling runs in its own goroutine so capture-pane calls
	// never block output delivery to the WebSocket client.
	metaCh := make(chan WSMeta, 1)
	go s.metaPollLoop(pane, cc.Done(), metaCh)

	var lastMeta WSMeta

	for {
		select {
		case ev := <-sub.C():
			if ev.Dirty {
				// The stream has a hole. Everything buffered was discarded,
				// so capture-pane is the only trustworthy screen state.
				slog.Debug("pane stream dirty, re-seeding", "target", pane.Target())
				seed, ok := s.captureSeed(pane)
				if !ok {
					// Cannot re-seed, so the screen is unrecoverable on this
					// socket. Close it and let the client reconnect for a
					// coherent seed rather than ack and resume over a hole.
					slog.Warn("re-seed failed, closing pane socket", "target", pane.Target())
					return
				}
				// Ack before the write: this goroutine stays inside
				// writeSeed until the seed reaches the socket, so output
				// produced after the capture queues behind it instead of
				// being dropped for the duration of the write.
				cc.AckReseed(sub)
				if err := writeSeed(conn, seed); err != nil {
					return
				}
				continue
			}
			// Coalesce: drain all buffered chunks into one write
			// to keep the channel drained and reduce WS round-trips.
			buf := append([]byte(nil), ev.Data...)
			for {
				select {
				case more := <-sub.C():
					if more.Dirty {
						// Rare: a drop while coalescing. Flush what we have,
						// then let the next iteration re-seed over it.
						cc.MarkPendingReseed(sub)
						goto send
					}
					buf = append(buf, more.Data...)
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
			// No-op by design: the CC client is created with
			// `refresh-client -f ignore-size`, so houston never resizes a
			// pane a human is attached to. The browser absorbs the mismatch.
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
