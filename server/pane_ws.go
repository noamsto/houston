package server

import (
	"encoding/json"
	"errors"
	"fmt"
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

// wsCloseServerChanged is a private-use close code (RFC 6455 §7.4.2, range
// 4000-4999): the client must not treat it like an ordinary drop and retry.
const wsCloseServerChanged = 4409

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

// metaPollInterval is how often each pane connection re-runs agent detection.
const metaPollInterval = time.Second

func (s *Server) handlePaneWS(w http.ResponseWriter, r *http.Request, pane tmux.Pane) {
	up := s.wsUpgrader()
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "error", err)
		return
	}

	servePane(conn, s.tmux, controlManagerAdapter{mgr: s.controlMgr}, s.registry, pane, metaPollInterval)
}

// servePane owns an upgraded pane connection: seeding, streaming and cleanup.
func servePane(conn *websocket.Conn, tm tmuxOps, cm controlManagerOps, registry *agents.Registry, pane tmux.Pane, metaEvery time.Duration) {
	// Look up tmux pane ID (%N format) for control mode routing
	paneID, err := tm.GetPaneID(pane)
	if err != nil {
		slog.Error("failed to get pane ID", "target", pane.Target(), "error", err)
		_ = conn.Close()
		return
	}

	// Get or create control client for this session (ref-counted)
	cc, err := cm.GetClient(pane.Session)
	if err != nil {
		slog.Error("failed to get control client", "session", pane.Session, "error", err)
		_ = conn.Close()
		return
	}

	// verified is the last control-client generation confirmed to still be on
	// pane.Server. Checked before auto-zoom and pause, so neither can land on
	// a foreign server.
	verified := new(atomic.Uint64)
	verified.Store(cc.Generation())
	if !serverStillMatches(conn, tm, pane, paneID) {
		_ = conn.Close()
		cm.ReleaseClient(pane.Session)
		return
	}

	// Per-connection lifetime. The control client is shared across every
	// socket on this session, so its Done channel outlives this connection.
	connDone := make(chan struct{})

	slog.Info("pane websocket connected (control mode)", "target", pane.Target(), "paneID", paneID)

	// Auto-zoom: if window has multiple panes, zoom the target pane so it
	// fills the window — gives a much better view, especially on mobile.
	weZoomed := false
	if count, err := tm.WindowPaneCount(pane); err == nil && count > 1 {
		if zoomed, err := tm.IsZoomed(pane); err == nil && !zoomed {
			if err := tm.ZoomPane(pane); err == nil {
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

	var serverChanged bool
	defer func() {
		// Ensure pane is resumed if we exit before the explicit continue
		if paused {
			continueCmd := fmt.Sprintf("refresh-client -A %s:continue", paneID)
			_, _ = cc.RunCommand(continueCmd)
		}
		// Restore zoom state if we auto-zoomed on connect, unless the socket
		// closed because the pane's server changed — the zoom toggle would
		// then hit an unrelated window on the new server.
		if weZoomed && !serverChanged {
			_ = tm.ZoomPane(pane) // toggle off
		}
		close(connDone)
		_ = conn.Close()
		cc.Unsubscribe(paneID, sub)
		cm.ReleaseClient(pane.Session)
	}()

	// reverify re-checks the server if the generation moved since it was last
	// verified, closing conn on mismatch. A reconnect between the baseline
	// above and Subscribe bumps the generation without marking sub dirty, and
	// one during the initial capture marks sub dirty too late to stop the
	// seed write — so it runs before the dims write and again after the capture.
	reverify := func() bool {
		g := cc.Generation()
		if g == verified.Load() {
			return true
		}
		if !serverStillMatches(conn, tm, pane, paneID) {
			serverChanged = true
			return false
		}
		verified.Store(g)
		return true
	}

	// Keepalive
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	})
	_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))

	// Fetch pane dimensions so the frontend can resize xterm.js to match.
	// Absolute cursor positions in %output depend on matching dimensions.
	// Fetched before the server re-check and written only after it passes —
	// the same fetch-then-check-then-write order the seed path uses. A
	// reconnect landing inside GetPaneSize (a tmux CLI exec addressed by
	// session:window.index) would otherwise read the new server's pane size
	// and send those cols/rows before this socket notices and closes.
	w, h, sizeErr := tm.GetPaneSize(pane)
	if !reverify() {
		return
	}
	if sizeErr == nil && w > 0 && h > 0 {
		dimsJSON, _ := json.Marshal(WSDims{Cols: w, Rows: h})
		dimsMsg, _ := json.Marshal(WSMessage{Type: "dims", Data: dimsJSON})
		if err := conn.WriteMessage(websocket.TextMessage, dimsMsg); err != nil {
			return
		}
	}

	// Seed: capture-pane provides scrollback history and initial visible
	// content. Pane is paused so no %output races with this seed. A capture
	// failure costs scrollback, not the connection.
	if seed, ok := captureSeed(tm, pane); ok {
		if !reverify() {
			return
		}
		if err := writeSeed(conn, seed); err != nil {
			return
		}
	}

	// Force the TUI to redraw via SIGWINCH (resize pane to same dimensions),
	// prompting it to repaint any garbled state. tmux discards %output while
	// paused, so the redraw's own output is not queued for later delivery —
	// this just nudges the TUI before we resume normal streaming below.
	if err := tm.ForceRedraw(pane); err != nil {
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

	go paneWSReadLoop(conn, cc, paneID, verified)
	serverChanged = paneWSWriteLoop(conn, tm, cc, registry, pane, paneID, sub, verified, connDone, metaEvery)
}

// serverStillMatches checks pane's tmux server is still the one paneID
// resolves to, closing conn and returning false if not. An unknown
// pane.Server is never refused.
func serverStillMatches(conn wsWriter, tm tmuxOps, pane tmux.Pane, paneID string) bool {
	if pane.Server == "" {
		return true
	}
	fresh, err := tm.ResolvePane(paneID)
	if err != nil {
		if errors.Is(err, tmux.ErrPaneNotFound) {
			slog.Info("pane websocket closing: pane gone", "paneID", paneID)
			_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(wsCloseServerChanged, "tmux server changed"))
			return false
		}
		slog.Warn("pane websocket closing: could not verify tmux server", "paneID", paneID, "error", err)
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "could not verify tmux server"))
		return false
	}
	if tmux.ServerMismatch(pane.Server, fresh.Server) {
		slog.Info("pane websocket closing: server mismatch", "paneID", paneID, "pane_server", pane.Server, "fresh_server", fresh.Server)
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(wsCloseServerChanged, "tmux server changed"))
		return false
	}
	return true
}

// captureSeed takes a capture-pane snapshot. The bool reports whether a
// non-empty seed was captured; neither a capture failure nor an empty pane
// is an error.
func captureSeed(tm tmuxOps, pane tmux.Pane) (string, bool) {
	seed, capErr := tm.CapturePane(pane, 500)
	if capErr != nil {
		slog.Debug("capture-pane failed", "target", pane.Target(), "error", capErr)
		return "", false
	}
	if seed == "" {
		return "", false
	}
	return seed, true
}

func writeSeed(conn wsWriter, seed string) error {
	outputJSON, _ := json.Marshal(WSOutput{Data: seed})
	msg, _ := json.Marshal(WSMessage{Type: "seed", Data: outputJSON})
	return conn.WriteMessage(websocket.TextMessage, msg)
}

// paneWSWriteLoop streams pane output to conn until it exits. serverChanged
// reports whether it exited because the pane's server changed.
func paneWSWriteLoop(conn wsWriter, tm tmuxOps, cc controlClientOps, registry *agents.Registry, pane tmux.Pane, paneID string, sub paneSub, verified *atomic.Uint64, connDone <-chan struct{}, metaEvery time.Duration) (serverChanged bool) {
	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	// Meta polling runs in its own goroutine so capture-pane calls
	// never block output delivery to the WebSocket client.
	metaCh := make(chan WSMeta, 1)
	go metaPollLoop(tm, registry, pane, connDone, metaCh, metaEvery)

	var lastMeta WSMeta

	for {
		select {
		case ev := <-sub.C():
			if ev.Dirty {
				// The stream has a hole. Everything buffered was discarded,
				// so capture-pane is the only trustworthy screen state.
				slog.Debug("pane stream dirty, re-seeding", "target", pane.Target())
				if g := cc.Generation(); g != verified.Load() {
					if !serverStillMatches(conn, tm, pane, paneID) {
						return true
					}
					verified.Store(g)
				}
				seed, ok := captureSeed(tm, pane)
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
				if cc.Generation() != verified.Load() {
					// A reconnect landed during the capture. markDirtyLocked
					// was a no-op while sub was still dirty, so AckReseed just
					// erased the only record of it. Re-arm so the next
					// iteration verifies, and don't send a seed that may be
					// the new server's.
					cc.MarkPendingReseed(sub)
					continue
				}
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
func metaPollLoop(tm tmuxOps, registry *agents.Registry, pane tmux.Pane, done <-chan struct{}, metaCh chan<- WSMeta, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	// Fetch initial pane info for agent detection
	panes, _ := tm.ListPanes(pane.Session, pane.Window)
	var panePath, paneCommand string
	for _, p := range panes {
		if p.Index == pane.Index {
			panePath = p.Path
			paneCommand = p.Command
			break
		}
	}
	var windowName string
	if windows, err := tm.ListWindows(pane.Session); err == nil {
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
			capture, err := tm.CapturePaneWithMode(pane, 500)
			if err != nil {
				continue
			}
			agent := registry.Detect(pane.Target(), paneCommand, capture.Output)
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

func paneWSReadLoop(conn *websocket.Conn, cc controlClientOps, paneID string, verified *atomic.Uint64) {
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
			if err := cc.SendKeys(verified.Load(), paneID, input.Data); err != nil {
				// A generation that moved mid-input writes a prefix and refuses
				// the rest; that partial delivery is worth an operator's
				// attention, unlike the full drop below.
				var partial *tmux.PartialSendError
				if errors.As(err, &partial) && partial.Sent > 0 {
					slog.Warn("partial input delivery pending tmux server check",
						"paneID", paneID, "sent", partial.Sent, "total", partial.Total)
					continue
				}
				if errors.Is(err, tmux.ErrStaleGeneration) {
					slog.Debug("dropping input pending tmux server check", "paneID", paneID)
					continue
				}
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
