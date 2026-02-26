# React Frontend Redesign — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the templ+htmx+vanilla-JS frontend with a React SPA featuring a sidebar session tree, xterm.js terminals, and resizable split panes — while keeping the Go backend as a clean JSON+WebSocket API.

**Architecture:** Go backend gets `/api/*` JSON routes and a WebSocket endpoint for pane I/O. React SPA (Vite + TypeScript) is embedded in the Go binary via `go:embed`. Old templ views are removed after React is complete.

**Tech Stack:** React 19, TypeScript, Vite, xterm.js, allotment (split panes), gorilla/websocket (Go WebSocket), go:embed.

**Design doc:** `docs/plans/2026-02-26-react-frontend-redesign.md`

---

## Phase 1: Backend API Layer

### Task 1: Add JSON tags to all data types

All Go structs that will be serialized to JSON need explicit `json` tags.

**Files:**
- Modify: `tmux/client.go` — Session, Window, Pane, PaneInfo, CaptureResult structs
- Modify: `parser/parser.go` — Result, ResultType, Mode types
- Modify: `agents/agent.go` — AgentType, AgentState types
- Modify: `views/types.go` — all view types

**Step 1: Add JSON tags to tmux types**

In `tmux/client.go`, add `json:"..."` tags to all exported struct fields:

```go
type Session struct {
	Name         string    `json:"name"`
	Created      time.Time `json:"created"`
	Windows      int       `json:"windows"`
	Attached     bool      `json:"attached"`
	LastActivity time.Time `json:"last_activity"`
}

type Window struct {
	Index        int       `json:"index"`
	Name         string    `json:"name"`
	Active       bool      `json:"active"`
	Panes        int       `json:"panes"`
	LastActivity time.Time `json:"last_activity"`
	Path         string    `json:"path"`
	Branch       string    `json:"branch"`
}

type Pane struct {
	Session string `json:"session"`
	Window  int    `json:"window"`
	Index   int    `json:"index"`
}

type PaneInfo struct {
	Index   int    `json:"index"`
	Active  bool   `json:"active"`
	Command string `json:"command"`
	Path    string `json:"path"`
	Title   string `json:"title"`
}

type CaptureResult struct {
	Output     string `json:"output"`
	Mode       string `json:"mode"`
	StatusLine string `json:"status_line"`
}
```

**Step 2: Add JSON tags to parser types**

In `parser/parser.go`:

```go
type Result struct {
	Type         ResultType `json:"type"`
	Mode         Mode       `json:"mode"`
	Question     string     `json:"question,omitempty"`
	Choices      []string   `json:"choices,omitempty"`
	ErrorSnippet string     `json:"error_snippet,omitempty"`
	Activity     string     `json:"activity,omitempty"`
	Suggestion   string     `json:"suggestion,omitempty"`
}
```

Also add `MarshalJSON`/`UnmarshalJSON` or string methods for `ResultType` and `Mode` so they serialize as strings ("idle", "working", "done", "question", "choice", "error") instead of integers.

**Step 3: Add JSON tags to agents types**

In `agents/agent.go`, `AgentType` is already a `string` type so it serializes fine. Add tag to `AgentState`:

```go
type AgentState struct {
	Agent  AgentType    `json:"agent"`
	Result parser.Result `json:"result"`
}
```

**Step 4: Add JSON tags to views types**

In `views/types.go`:

```go
type WindowWithStatus struct {
	Window         tmux.Window      `json:"window"`
	Pane           tmux.Pane        `json:"pane"`
	ParseResult    parser.Result    `json:"parse_result"`
	Preview        []string         `json:"preview"`
	NeedsAttention bool             `json:"needs_attention"`
	Branch         string           `json:"branch"`
	Process        string           `json:"process"`
	AgentType      agents.AgentType `json:"agent_type"`
}

type SessionWithWindows struct {
	Session        tmux.Session       `json:"session"`
	Windows        []WindowWithStatus `json:"windows"`
	AttentionCount int                `json:"attention_count"`
	HasWorking     bool               `json:"has_working"`
}

type SessionsData struct {
	NeedsAttention []SessionWithWindows `json:"needs_attention"`
	Active         []SessionWithWindows `json:"active"`
	Idle           []SessionWithWindows `json:"idle"`
}

type AgentStripItem struct {
	Session   string           `json:"session"`
	Window    int              `json:"window"`
	Pane      int              `json:"pane"`
	Name      string           `json:"name"`
	Indicator string           `json:"indicator"`
	AgentType agents.AgentType `json:"agent_type"`
	Active    bool             `json:"active"`
}

type PaneData struct {
	Pane        tmux.Pane        `json:"pane"`
	Output      string           `json:"output"`
	ParseResult parser.Result    `json:"parse_result"`
	Windows     []tmux.Window    `json:"windows"`
	Panes       []tmux.PaneInfo  `json:"panes"`
	PaneWidth   int              `json:"pane_width"`
	PaneHeight  int              `json:"pane_height"`
	Suggestion  string           `json:"suggestion"`
	StripItems  []AgentStripItem `json:"strip_items"`
}

type OpenCodeSession struct {
	State          opencode.SessionState `json:"state"`
	NeedsAttention bool                  `json:"needs_attention"`
	IsWorking      bool                  `json:"is_working"`
	Preview        []string              `json:"preview"`
}

type OpenCodeData struct {
	NeedsAttention []OpenCodeSession `json:"needs_attention"`
	Active         []OpenCodeSession `json:"active"`
	Idle           []OpenCodeSession `json:"idle"`
	Servers        []*opencode.Server `json:"servers"`
}
```

**Step 5: Verify it compiles**

Run: `go build ./...`
Expected: clean build, no errors.

**Step 6: Commit**

```
git add tmux/client.go parser/parser.go agents/agent.go views/types.go
git commit -m "feat(api): add JSON struct tags to all data types"
```

---

### Task 2: Add JSON API routes for sessions

Add `/api/sessions` endpoint that returns JSON, and `/api/sessions?stream=1` that streams JSON via SSE.

**Files:**
- Create: `server/api.go` — JSON API handlers
- Modify: `server/server.go` — register new routes in Handler()

**Step 1: Create `server/api.go` with sessions JSON handler**

```go
package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
)

func (s *Server) handleAPISessions(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("stream") == "1" {
		s.streamAPISessionsJSON(w, r)
		return
	}

	data := s.buildSessionsData()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (s *Server) streamAPISessionsJSON(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	// Send initial data immediately
	s.sendAPISessionsEvent(w, flusher)

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if err := s.sendAPISessionsEvent(w, flusher); err != nil {
				slog.Debug("SSE sessions write error", "error", err)
				return
			}
		}
	}
}

func (s *Server) sendAPISessionsEvent(w http.ResponseWriter, flusher http.Flusher) error {
	data := s.buildSessionsData()
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", jsonBytes)
	if err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
```

**Step 2: Register API routes in `server/server.go` Handler()**

Add below the existing routes in `Handler()`:

```go
// API routes (JSON)
mux.HandleFunc("/api/sessions", s.handleAPISessions)
```

**Step 3: Add CORS middleware for dev**

During development, Vite runs on :5173 and Go on :9090. Add CORS headers for `/api/*`:

```go
// In api.go
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

Wrap the API mux in Handler():

```go
apiMux := http.NewServeMux()
apiMux.HandleFunc("/api/sessions", s.handleAPISessions)
mux.Handle("/api/", corsMiddleware(apiMux))
```

**Step 4: Test manually**

Run: `go run . -addr localhost:9090`
Run: `curl -s localhost:9090/api/sessions | jq .`
Expected: JSON object with `needs_attention`, `active`, `idle` arrays.

Run: `curl -s -N localhost:9090/api/sessions?stream=1`
Expected: SSE stream with `data: {...}` every 3 seconds.

**Step 5: Commit**

```
git add server/api.go server/server.go
git commit -m "feat(api): add /api/sessions JSON and SSE endpoints"
```

---

### Task 3: Add WebSocket endpoint for pane I/O

This is the biggest backend change. Replace the SSE pane stream with a bidirectional WebSocket: terminal output downstream, keystrokes upstream.

**Files:**
- Modify: `go.mod` — add `github.com/gorilla/websocket`
- Create: `server/pane_ws.go` — WebSocket handler
- Modify: `server/api.go` — register route

**Step 1: Add gorilla/websocket dependency**

Run: `go get github.com/gorilla/websocket`

**Step 2: Create `server/pane_ws.go`**

```go
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
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
	defer conn.Close()

	slog.Info("pane websocket connected", "target", pane.Target())

	// Read loop: handle client messages (input, resize)
	go s.paneWSReadLoop(conn, pane)

	// Write loop: stream pane output and metadata
	s.paneWSWriteLoop(conn, pane)
}

func (s *Server) paneWSReadLoop(conn *websocket.Conn, pane tmux.Pane) {
	defer conn.Close()

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

		case "resize":
			var resize WSResize
			if err := json.Unmarshal(msg.Data, &resize); err != nil {
				continue
			}
			// Resize the tmux pane to match xterm.js dimensions
			if resize.Cols > 0 && resize.Rows > 0 {
				s.tmux.ResizePane(pane, "x", resize.Cols)
				s.tmux.ResizePane(pane, "y", resize.Rows)
			}
		}
	}
}

func (s *Server) paneWSWriteLoop(conn *websocket.Conn, pane tmux.Pane) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var lastOutput string
	var lastMeta WSMeta

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

	for range ticker.C {
		capture, err := s.tmux.CapturePaneWithMode(pane, 500)
		if err != nil {
			slog.Debug("capture failed", "error", err)
			return
		}

		// Detect agent and parse state
		paneID := pane.Target()
		agent := s.registry.Detect(paneID, paneCommand, capture.Output)
		parseResult := getAgentState(agent, panePath, capture.Output)
		filteredOutput := agent.FilterStatusBar(capture.Output)

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
		if filteredOutput != lastOutput {
			lastOutput = filteredOutput
			outputJSON, _ := json.Marshal(WSOutput{Data: filteredOutput})
			msg, _ := json.Marshal(WSMessage{Type: "output", Data: outputJSON})
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		}

		// Send meta if changed
		if meta != lastMeta {
			lastMeta = meta
			metaJSON, _ := json.Marshal(meta)
			msg, _ := json.Marshal(WSMessage{Type: "meta", Data: metaJSON})
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		}
	}
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
```

**Step 3: Register WebSocket route**

In `server/api.go`, add to the API routes:

```go
apiMux.HandleFunc("/api/pane/", s.handleAPIPane)
```

Add the router for pane API:

```go
func (s *Server) handleAPIPane(w http.ResponseWriter, r *http.Request) {
	// Strip /api prefix and reuse existing pane target parsing
	path := strings.TrimPrefix(r.URL.Path, "/api")
	pane, err := parsePaneTarget(path)
	if err != nil {
		http.Error(w, "invalid pane target", http.StatusBadRequest)
		return
	}

	// Check for sub-actions
	suffix := strings.TrimPrefix(path, "/pane/"+pane.URLTarget())

	switch {
	case suffix == "/ws":
		s.handlePaneWS(w, r, pane)
	case suffix == "/send" && r.Method == http.MethodPost:
		s.handlePaneSend(w, r, pane)
	case suffix == "/send-with-images" && r.Method == http.MethodPost:
		s.handlePaneSendWithImages(w, r, pane)
	case suffix == "/kill" && r.Method == http.MethodPost:
		s.handlePaneKill(w, r, pane)
	case suffix == "/respawn" && r.Method == http.MethodPost:
		s.handlePaneRespawn(w, r, pane)
	case suffix == "/kill-window" && r.Method == http.MethodPost:
		s.handleWindowKill(w, r, pane)
	case suffix == "/zoom" && r.Method == http.MethodPost:
		s.handlePaneZoom(w, r, pane)
	default:
		// Return pane data as JSON
		s.handlePaneJSON(w, r, pane)
	}
}

func (s *Server) handlePaneJSON(w http.ResponseWriter, r *http.Request, pane tmux.Pane) {
	windows, _ := s.tmux.ListWindows(pane.Session)
	paneInfos, _ := s.tmux.ListPanes(pane.Session, pane.Window)

	capture, err := s.tmux.CapturePaneWithMode(pane, 500)
	if err != nil {
		http.Error(w, "failed to capture pane", http.StatusInternalServerError)
		return
	}

	var panePath, paneCommand string
	for _, p := range paneInfos {
		if p.Index == pane.Index {
			panePath = p.Path
			paneCommand = p.Command
			break
		}
	}

	paneID := pane.Target()
	agent := s.registry.Detect(paneID, paneCommand, capture.Output)
	parseResult := getAgentState(agent, panePath, capture.Output)

	suggestion := ""
	if agent.Type() == agents.AgentClaudeCode {
		suggestion = claude.ExtractSuggestion(capture.Output)
	}

	width, height, _ := s.tmux.GetPaneSize(pane)

	data := views.PaneData{
		Pane:        pane,
		Output:      agent.FilterStatusBar(capture.Output),
		ParseResult: parseResult,
		Windows:     windows,
		Panes:       paneInfos,
		PaneWidth:   width,
		PaneHeight:  height,
		Suggestion:  suggestion,
		StripItems:  s.buildAgentStripItems(pane.Session, pane.Window, pane.Index),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}
```

**Step 4: Verify it compiles**

Run: `go build ./...`

**Step 5: Test manually**

Run: `go run . -addr localhost:9090`

Test JSON endpoint:
Run: `curl -s localhost:9090/api/pane/SESSION:0.0 | jq .` (replace SESSION with real session name)
Expected: JSON PaneData object.

Test WebSocket (use websocat or similar):
Run: `websocat ws://localhost:9090/api/pane/SESSION:0.0/ws`
Expected: JSON messages with `{"type":"output",...}` and `{"type":"meta",...}`.

**Step 6: Commit**

```
go mod tidy
git add go.mod go.sum server/pane_ws.go server/api.go server/server.go
git commit -m "feat(api): add pane WebSocket and JSON API endpoints"
```

---

### Task 4: Add OpenCode API routes

**Files:**
- Modify: `server/api.go` — add OpenCode JSON endpoints

**Step 1: Add OpenCode handlers to `server/api.go`**

```go
func (s *Server) handleAPIOpenCodeSessions(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("stream") == "1" {
		s.streamAPIOpenCodeJSON(w, r)
		return
	}

	data := s.buildOpenCodeData(r.Context())
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (s *Server) streamAPIOpenCodeJSON(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Send initial data
	s.sendAPIOpenCodeEvent(r.Context(), w, flusher)

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if err := s.sendAPIOpenCodeEvent(r.Context(), w, flusher); err != nil {
				return
			}
		}
	}
}

func (s *Server) sendAPIOpenCodeEvent(ctx context.Context, w http.ResponseWriter, flusher http.Flusher) error {
	data := s.buildOpenCodeData(ctx)
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", jsonBytes)
	if err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
```

**Step 2: Register routes**

In the API mux section of `server/api.go`:

```go
apiMux.HandleFunc("/api/opencode/sessions", s.handleAPIOpenCodeSessions)
apiMux.HandleFunc("/api/opencode/session/", s.handleAPIOpenCodeSession)
```

Reuse the existing OpenCode session handler but return JSON. Add a handler that wraps the existing logic:

```go
func (s *Server) handleAPIOpenCodeSession(w http.ResponseWriter, r *http.Request) {
	// Delegate to existing handler — it already supports JSON via Accept header
	// For API routes, force JSON response
	w.Header().Set("Content-Type", "application/json")
	s.handleOpenCodeSession(w, r)
}
```

**Step 3: Test**

Run: `curl -s localhost:9090/api/opencode/sessions | jq .`

**Step 4: Commit**

```
git add server/api.go
git commit -m "feat(api): add OpenCode JSON and SSE API endpoints"
```

---

## Phase 2: React App Scaffold

### Task 5: Scaffold React app with Vite

**Files:**
- Create: `ui/` directory with Vite + React + TypeScript project
- Modify: `justfile` — add frontend dev commands

**Step 1: Initialize Vite project**

Run from project root:
```bash
npm create vite@latest ui -- --template react-ts
cd ui
npm install
```

**Step 2: Install dependencies**

```bash
cd ui
npm install @xterm/xterm @xterm/addon-fit @xterm/addon-web-links allotment
npm install -D @types/node
```

**Step 3: Configure Vite proxy**

Replace `ui/vite.config.ts`:

```typescript
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': {
        target: 'http://localhost:9090',
        ws: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
```

**Step 4: Clean up scaffold**

Remove generated boilerplate: `ui/src/App.css`, `ui/src/index.css`, `ui/public/vite.svg`, `ui/src/assets/`.

Replace `ui/src/App.tsx`:

```tsx
export default function App() {
  return <div>houston</div>
}
```

Replace `ui/src/main.tsx`:

```tsx
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
```

**Step 5: Verify it runs**

Run: `cd ui && npm run dev`
Expected: Vite dev server on :5173 showing "houston".

Run: `cd ui && npm run build`
Expected: Clean build in `ui/dist/`.

**Step 6: Add to justfile**

```just
# Start React dev server (proxy to Go backend)
ui-dev:
    cd ui && npm run dev

# Build React frontend
ui-build:
    cd ui && npm run build
```

**Step 7: Commit**

```
git add ui/ justfile
git commit -m "feat(ui): scaffold React app with Vite + TypeScript"
```

---

### Task 6: Add go:embed for production serving

**Files:**
- Create: `embed.go` — embed ui/dist
- Modify: `server/server.go` — add SPA serving from embedded FS
- Modify: `main.go` — pass embedded FS to server

**Step 1: Create `embed.go`**

```go
package main

import "embed"

//go:embed ui/dist/*
var uiFS embed.FS
```

**Step 2: Add SPA handler to server**

In `server/server.go`, add a function that serves the embedded SPA with fallback to index.html for client-side routing:

```go
import "io/fs"

func SPAHandler(uiFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(uiFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Try to serve the file directly
		path := r.URL.Path
		if path == "/" {
			path = "index.html"
		} else {
			path = strings.TrimPrefix(path, "/")
		}

		// Check if file exists
		if f, err := uiFS.Open(path); err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}

		// Fallback to index.html for client-side routing
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}
```

**Step 3: Update Handler() to serve SPA**

Pass `uiFS` through Config and use it as the fallback handler:

```go
type Config struct {
	// ... existing fields ...
	UIFS fs.FS // Embedded frontend filesystem (nil = don't serve SPA)
}
```

In `Handler()`, replace the handleIndex route:

```go
if s.uiFS != nil {
	mux.Handle("/", SPAHandler(s.uiFS))
} else {
	mux.HandleFunc("/", s.handleIndex) // Legacy templ handler
}
```

**Step 4: Update `main.go`**

Pass the embedded FS (with subdirectory stripping):

```go
uiSubFS, _ := fs.Sub(uiFS, "ui/dist")
srv, err := server.New(server.Config{
	// ... existing ...
	UIFS: uiSubFS,
})
```

**Step 5: Test production build**

Run:
```bash
cd ui && npm run build
cd .. && go build -o houston .
./houston -addr localhost:9090
```
Visit `http://localhost:9090` — should show the React app.

**Step 6: Commit**

```
git add embed.go main.go server/server.go
git commit -m "feat: serve React SPA via go:embed in production"
```

---

### Task 7: Define TypeScript API types

Mirror Go data types in TypeScript for type-safe API consumption.

**Files:**
- Create: `ui/src/api/types.ts`

**Step 1: Create type definitions**

```typescript
// ui/src/api/types.ts

// Mirror of parser.ResultType
export type ResultType = 'idle' | 'working' | 'done' | 'question' | 'choice' | 'error'

// Mirror of parser.Mode
export type Mode = 'unknown' | 'insert' | 'normal'

// Mirror of agents.AgentType
export type AgentType = 'claude-code' | 'amp' | 'generic'

// Mirror of parser.Result
export interface ParseResult {
  type: ResultType
  mode: Mode
  question?: string
  choices?: string[]
  error_snippet?: string
  activity?: string
  suggestion?: string
}

// Mirror of tmux.Session
export interface Session {
  name: string
  created: string // ISO 8601
  windows: number
  attached: boolean
  last_activity: string
}

// Mirror of tmux.Window
export interface Window {
  index: number
  name: string
  active: boolean
  panes: number
  last_activity: string
  path: string
  branch: string
}

// Mirror of tmux.Pane
export interface Pane {
  session: string
  window: number
  index: number
}

// Mirror of tmux.PaneInfo
export interface PaneInfo {
  index: number
  active: boolean
  command: string
  path: string
  title: string
}

// Mirror of views.WindowWithStatus
export interface WindowWithStatus {
  window: Window
  pane: Pane
  parse_result: ParseResult
  preview: string[]
  needs_attention: boolean
  branch: string
  process: string
  agent_type: AgentType
}

// Mirror of views.SessionWithWindows
export interface SessionWithWindows {
  session: Session
  windows: WindowWithStatus[]
  attention_count: number
  has_working: boolean
}

// Mirror of views.SessionsData
export interface SessionsData {
  needs_attention: SessionWithWindows[]
  active: SessionWithWindows[]
  idle: SessionWithWindows[]
}

// Mirror of views.AgentStripItem
export interface AgentStripItem {
  session: string
  window: number
  pane: number
  name: string
  indicator: string
  agent_type: AgentType
  active: boolean
}

// Mirror of views.PaneData
export interface PaneData {
  pane: Pane
  output: string
  parse_result: ParseResult
  windows: Window[]
  panes: PaneInfo[]
  pane_width: number
  pane_height: number
  suggestion: string
  strip_items: AgentStripItem[]
}

// WebSocket message types
export type WSMessageType = 'output' | 'meta' | 'input' | 'resize'

export interface WSMessage {
  type: WSMessageType
  data: unknown
}

export interface WSOutput {
  data: string
}

export interface WSMeta {
  agent: AgentType
  mode: string
  status: string
  choices?: string[]
  suggestion?: string
  status_line?: string
  activity?: string
}

export interface WSInput {
  data: string
}

export interface WSResize {
  cols: number
  rows: number
}
```

**Step 2: Commit**

```
git add ui/src/api/types.ts
git commit -m "feat(ui): add TypeScript API type definitions"
```

---

### Task 8: Build API hooks (SSE + WebSocket)

**Files:**
- Create: `ui/src/hooks/useSessionsStream.ts` — SSE hook for session list
- Create: `ui/src/hooks/usePaneSocket.ts` — WebSocket hook for pane I/O

**Step 1: Create SSE hook**

```typescript
// ui/src/hooks/useSessionsStream.ts
import { useEffect, useRef, useState } from 'react'
import type { SessionsData } from '../api/types'

export function useSessionsStream() {
  const [sessions, setSessions] = useState<SessionsData | null>(null)
  const [connected, setConnected] = useState(false)
  const eventSourceRef = useRef<EventSource | null>(null)

  useEffect(() => {
    const es = new EventSource('/api/sessions?stream=1')
    eventSourceRef.current = es

    es.onopen = () => setConnected(true)

    es.onmessage = (event) => {
      try {
        const data: SessionsData = JSON.parse(event.data)
        setSessions(data)
      } catch (e) {
        console.error('Failed to parse sessions SSE:', e)
      }
    }

    es.onerror = () => {
      setConnected(false)
      // EventSource auto-reconnects
    }

    return () => {
      es.close()
      eventSourceRef.current = null
    }
  }, [])

  return { sessions, connected }
}
```

**Step 2: Create WebSocket hook**

```typescript
// ui/src/hooks/usePaneSocket.ts
import { useEffect, useRef, useCallback, useState } from 'react'
import type { WSMeta, WSOutput } from '../api/types'

interface PaneSocketCallbacks {
  onOutput: (data: string) => void
  onMeta: (meta: WSMeta) => void
}

export function usePaneSocket(target: string | null, callbacks: PaneSocketCallbacks) {
  const wsRef = useRef<WebSocket | null>(null)
  const [connected, setConnected] = useState(false)

  const sendInput = useCallback((data: string) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({
        type: 'input',
        data: JSON.stringify({ data }),
      }))
    }
  }, [])

  const sendResize = useCallback((cols: number, rows: number) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({
        type: 'resize',
        data: JSON.stringify({ cols, rows }),
      }))
    }
  }, [])

  useEffect(() => {
    if (!target) return

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const wsUrl = `${protocol}//${window.location.host}/api/pane/${target}/ws`

    const ws = new WebSocket(wsUrl)
    wsRef.current = ws

    ws.onopen = () => setConnected(true)
    ws.onclose = () => setConnected(false)

    ws.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data)
        switch (msg.type) {
          case 'output': {
            const output: WSOutput = JSON.parse(msg.data)
            callbacks.onOutput(output.data)
            break
          }
          case 'meta': {
            const meta: WSMeta = JSON.parse(msg.data)
            callbacks.onMeta(meta)
            break
          }
        }
      } catch (e) {
        console.error('Failed to parse WS message:', e)
      }
    }

    return () => {
      ws.close()
      wsRef.current = null
    }
  }, [target]) // Reconnect when target changes

  return { connected, sendInput, sendResize }
}
```

**Step 3: Commit**

```
git add ui/src/hooks/useSessionsStream.ts ui/src/hooks/usePaneSocket.ts
git commit -m "feat(ui): add SSE and WebSocket hooks for sessions and pane data"
```

---

## Phase 3: Core UI Components

### Task 9: Build the App shell and layout

Top-level layout with sidebar + terminal area, responsive to mobile/desktop.

**Files:**
- Create: `ui/src/App.tsx` — layout shell
- Create: `ui/src/hooks/useLayout.ts` — split pane state + localStorage persistence
- Create: `ui/src/hooks/useMediaQuery.ts` — mobile/desktop detection
- Create: `ui/src/theme/tokens.css` — CSS custom properties

**Step 1: Create CSS tokens**

```css
/* ui/src/theme/tokens.css */
:root {
  /* Dark theme (default) */
  --bg-base: #0a0a0f;
  --bg-sidebar: #0f1017;
  --bg-terminal: #000000;
  --bg-header: #12131a;
  --bg-surface: #1a1b26;

  --text-primary: #e1e1e6;
  --text-secondary: #8b8b96;
  --text-muted: #5b5b66;

  --accent-attention: #f59e0b;
  --accent-working: #3b82f6;
  --accent-done: #22c55e;
  --accent-idle: #4b5563;
  --accent-error: #ef4444;

  --border: #1e1e2a;
  --divider: #151520;

  --sidebar-width: 240px;
  --header-height: 24px;

  --font-mono: 'JetBrains Mono', 'Fira Code', 'Cascadia Code', monospace;
  --font-ui: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
}

.light {
  --bg-base: #f5f5f0;
  --bg-sidebar: #eeeee9;
  --bg-terminal: #fafafa;
  --bg-header: #e8e8e3;
  --bg-surface: #ffffff;

  --text-primary: #1a1a2e;
  --text-secondary: #5a5a6e;
  --text-muted: #9a9aae;

  --border: #d8d8d2;
  --divider: #e0e0da;
}

* { box-sizing: border-box; margin: 0; padding: 0; }

html, body, #root {
  height: 100%;
  background: var(--bg-base);
  color: var(--text-primary);
  font-family: var(--font-ui);
}
```

**Step 2: Create useMediaQuery hook**

```typescript
// ui/src/hooks/useMediaQuery.ts
import { useState, useEffect } from 'react'

export function useMediaQuery(query: string): boolean {
  const [matches, setMatches] = useState(
    () => window.matchMedia(query).matches
  )

  useEffect(() => {
    const mql = window.matchMedia(query)
    const handler = (e: MediaQueryListEvent) => setMatches(e.matches)
    mql.addEventListener('change', handler)
    return () => mql.removeEventListener('change', handler)
  }, [query])

  return matches
}

export function useIsDesktop() {
  return useMediaQuery('(min-width: 1024px)')
}
```

**Step 3: Create useLayout hook**

```typescript
// ui/src/hooks/useLayout.ts
import { useReducer, useEffect } from 'react'

export interface PaneInstance {
  id: string
  target: string // "session:window.pane"
}

export type SplitLayout =
  | { type: 'empty' }
  | { type: 'single'; paneId: string }
  | { type: 'split'; direction: 'horizontal' | 'vertical'; ratio: number; first: SplitLayout; second: SplitLayout }

interface LayoutState {
  panes: PaneInstance[]
  layout: SplitLayout
  focusedPaneId: string | null
}

type LayoutAction =
  | { type: 'OPEN_PANE'; target: string }
  | { type: 'SPLIT_PANE'; target: string; direction: 'horizontal' | 'vertical' }
  | { type: 'CLOSE_PANE'; paneId: string }
  | { type: 'FOCUS_PANE'; paneId: string }
  | { type: 'SET_RATIO'; ratio: number }

let nextId = 1
function genId() { return `pane-${nextId++}` }

function layoutReducer(state: LayoutState, action: LayoutAction): LayoutState {
  switch (action.type) {
    case 'OPEN_PANE': {
      const id = genId()
      const pane: PaneInstance = { id, target: action.target }

      if (state.layout.type === 'empty') {
        return { panes: [pane], layout: { type: 'single', paneId: id }, focusedPaneId: id }
      }

      // Replace focused pane's target
      if (state.focusedPaneId) {
        const updated = state.panes.map(p =>
          p.id === state.focusedPaneId ? { ...p, target: action.target } : p
        )
        return { ...state, panes: updated }
      }

      return state
    }

    case 'SPLIT_PANE': {
      const id = genId()
      const pane: PaneInstance = { id, target: action.target }

      if (state.layout.type === 'empty' || state.layout.type === 'single') {
        const currentLayout = state.layout.type === 'single' ? state.layout : null
        if (!currentLayout) {
          return { panes: [pane], layout: { type: 'single', paneId: id }, focusedPaneId: id }
        }
        return {
          panes: [...state.panes, pane],
          layout: {
            type: 'split',
            direction: action.direction,
            ratio: 0.5,
            first: currentLayout,
            second: { type: 'single', paneId: id },
          },
          focusedPaneId: id,
        }
      }

      // For nested splits: add next to focused pane
      // (simplified — full tree manipulation for deeper nesting)
      return {
        panes: [...state.panes, pane],
        layout: {
          type: 'split',
          direction: action.direction,
          ratio: 0.5,
          first: state.layout,
          second: { type: 'single', paneId: id },
        },
        focusedPaneId: id,
      }
    }

    case 'CLOSE_PANE': {
      const remaining = state.panes.filter(p => p.id !== action.paneId)
      if (remaining.length === 0) {
        return { panes: [], layout: { type: 'empty' }, focusedPaneId: null }
      }

      // Simplify layout: remove closed pane, collapse single-child splits
      const newLayout = removePaneFromLayout(state.layout, action.paneId)
      return {
        panes: remaining,
        layout: newLayout,
        focusedPaneId: remaining[0]?.id ?? null,
      }
    }

    case 'FOCUS_PANE':
      return { ...state, focusedPaneId: action.paneId }

    default:
      return state
  }
}

function removePaneFromLayout(layout: SplitLayout, paneId: string): SplitLayout {
  if (layout.type === 'single') {
    return layout.paneId === paneId ? { type: 'empty' } : layout
  }
  if (layout.type === 'split') {
    const first = removePaneFromLayout(layout.first, paneId)
    const second = removePaneFromLayout(layout.second, paneId)
    if (first.type === 'empty') return second
    if (second.type === 'empty') return first
    return { ...layout, first, second }
  }
  return layout
}

const STORAGE_KEY = 'houston-layout'

function loadState(): LayoutState {
  try {
    const saved = localStorage.getItem(STORAGE_KEY)
    if (saved) return JSON.parse(saved)
  } catch {}
  return { panes: [], layout: { type: 'empty' }, focusedPaneId: null }
}

export function useLayout() {
  const [state, dispatch] = useReducer(layoutReducer, undefined, loadState)

  useEffect(() => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(state))
  }, [state])

  return { ...state, dispatch }
}
```

**Step 4: Create App shell**

```tsx
// ui/src/App.tsx
import { useState } from 'react'
import { useSessionsStream } from './hooks/useSessionsStream'
import { useLayout } from './hooks/useLayout'
import { useIsDesktop } from './hooks/useMediaQuery'
import { Sidebar } from './components/Sidebar'
import { TerminalArea } from './components/TerminalArea'
import './theme/tokens.css'

export default function App() {
  const { sessions, connected } = useSessionsStream()
  const layout = useLayout()
  const isDesktop = useIsDesktop()
  const [sidebarOpen, setSidebarOpen] = useState(false)

  const handleSelectWindow = (target: string) => {
    layout.dispatch({ type: 'OPEN_PANE', target })
    if (!isDesktop) setSidebarOpen(false)
  }

  const handleSplitWindow = (target: string) => {
    layout.dispatch({ type: 'SPLIT_PANE', target, direction: 'horizontal' })
    if (!isDesktop) setSidebarOpen(false)
  }

  return (
    <div style={{ display: 'flex', height: '100vh', overflow: 'hidden' }}>
      <Sidebar
        sessions={sessions}
        connected={connected}
        open={isDesktop || sidebarOpen}
        onClose={() => setSidebarOpen(false)}
        onSelectWindow={handleSelectWindow}
        onSplitWindow={handleSplitWindow}
        isDesktop={isDesktop}
      />
      <TerminalArea
        layout={layout}
        onMenuClick={() => setSidebarOpen(true)}
        isDesktop={isDesktop}
      />
    </div>
  )
}
```

**Step 5: Create placeholder components**

Create minimal stubs for Sidebar and TerminalArea so the app compiles. These will be fleshed out in subsequent tasks.

```tsx
// ui/src/components/Sidebar.tsx
import type { SessionsData } from '../api/types'

interface Props {
  sessions: SessionsData | null
  connected: boolean
  open: boolean
  onClose: () => void
  onSelectWindow: (target: string) => void
  onSplitWindow: (target: string) => void
  isDesktop: boolean
}

export function Sidebar({ sessions, open, onClose, onSelectWindow, isDesktop }: Props) {
  if (!open) return null

  return (
    <aside style={{
      width: isDesktop ? 'var(--sidebar-width)' : '100vw',
      background: 'var(--bg-sidebar)',
      borderRight: '1px solid var(--border)',
      overflow: 'auto',
      flexShrink: 0,
      position: isDesktop ? 'relative' : 'fixed',
      zIndex: isDesktop ? 'auto' : 100,
      height: '100%',
    }}>
      <div style={{ padding: '12px' }}>
        <h2 style={{ fontSize: 14, color: 'var(--text-secondary)' }}>houston</h2>
        {!sessions && <p style={{ color: 'var(--text-muted)' }}>Loading...</p>}
        {sessions && <pre style={{ fontSize: 11, color: 'var(--text-secondary)' }}>
          {JSON.stringify(sessions, null, 2).slice(0, 500)}
        </pre>}
      </div>
    </aside>
  )
}
```

```tsx
// ui/src/components/TerminalArea.tsx
import type { useLayout } from '../hooks/useLayout'

interface Props {
  layout: ReturnType<typeof useLayout>
  onMenuClick: () => void
  isDesktop: boolean
}

export function TerminalArea({ layout, onMenuClick, isDesktop }: Props) {
  return (
    <main style={{ flex: 1, background: 'var(--bg-terminal)', display: 'flex', flexDirection: 'column' }}>
      {!isDesktop && (
        <header style={{ padding: '8px 12px', background: 'var(--bg-header)', display: 'flex', alignItems: 'center', gap: 8 }}>
          <button onClick={onMenuClick} style={{ background: 'none', border: 'none', color: 'var(--text-primary)', cursor: 'pointer' }}>☰</button>
          <span style={{ fontSize: 14 }}>houston</span>
        </header>
      )}
      <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
        {layout.layout.type === 'empty' ? (
          <p style={{ color: 'var(--text-muted)' }}>Select a session to start</p>
        ) : (
          <p style={{ color: 'var(--text-secondary)' }}>
            {layout.panes.map(p => p.target).join(', ')}
          </p>
        )}
      </div>
    </main>
  )
}
```

**Step 6: Verify app runs**

Run: `cd ui && npm run dev`
Expected: Dark background, sidebar shows session JSON data from SSE, terminal area shows empty state.

**Step 7: Commit**

```
git add ui/src/
git commit -m "feat(ui): app shell with sidebar, terminal area, layout state, and theme"
```

---

### Task 10: Build the Sidebar session tree

**Files:**
- Rewrite: `ui/src/components/Sidebar.tsx` — full implementation
- Create: `ui/src/components/SessionTree.tsx` — grouped session list

Implement the sidebar with:
- Session groups (attention / active / idle)
- Expandable session → window tree
- Click window to open in terminal
- Ctrl+click to split
- Attention pulse animation
- Filter input
- Theme toggle

This is a large component. Follow the design doc sidebar section exactly. Use CSS-in-JS via inline styles or a small CSS module — no CSS framework dependency.

**Step 1: Build SessionTree component**

Renders the three groups. Each session expands to show windows. Windows are clickable.

**Step 2: Add attention pulse CSS animation**

Add keyframes to `tokens.css`:
```css
@keyframes attention-pulse {
  0%, 100% { box-shadow: inset 2px 0 0 var(--accent-attention), 0 0 8px rgba(245,158,11,0.15); }
  50%      { box-shadow: inset 2px 0 0 var(--accent-attention), 0 0 16px rgba(245,158,11,0.25); }
}
```

**Step 3: Add filter input at top**

Local state for filter text. Filter sessions by name + window name + branch.

**Step 4: Add theme toggle**

Toggle `.light` class on `<html>` element. Persist to localStorage.

**Step 5: Test with live data**

Run Go backend + Vite dev server. Verify sessions appear, grouped correctly, expandable, clickable.

**Step 6: Commit**

```
git add ui/src/components/Sidebar.tsx ui/src/components/SessionTree.tsx ui/src/theme/tokens.css
git commit -m "feat(ui): sidebar with session tree, filter, attention glow, theme toggle"
```

---

### Task 11: Build TerminalPane with xterm.js

The core component. Mounts xterm.js, connects WebSocket, handles I/O.

**Files:**
- Create: `ui/src/components/TerminalPane.tsx`
- Create: `ui/src/components/PaneHeader.tsx`
- Create: `ui/src/lib/xterm.ts` — xterm.js config and theme

**Step 1: Create xterm config**

```typescript
// ui/src/lib/xterm.ts
import type { ITheme } from '@xterm/xterm'

export const darkTheme: ITheme = {
  background: '#000000',
  foreground: '#e1e1e6',
  cursor: '#e1e1e6',
  cursorAccent: '#000000',
  selectionBackground: '#3b82f644',
  black: '#1a1b26',
  red: '#f7768e',
  green: '#9ece6a',
  yellow: '#e0af68',
  blue: '#7aa2f7',
  magenta: '#bb9af7',
  cyan: '#7dcfff',
  white: '#c0caf5',
  brightBlack: '#414868',
  brightRed: '#f7768e',
  brightGreen: '#9ece6a',
  brightYellow: '#e0af68',
  brightBlue: '#7aa2f7',
  brightMagenta: '#bb9af7',
  brightCyan: '#7dcfff',
  brightWhite: '#c0caf5',
}

export const lightTheme: ITheme = {
  background: '#fafafa',
  foreground: '#1a1a2e',
  cursor: '#1a1a2e',
  cursorAccent: '#fafafa',
  selectionBackground: '#3b82f644',
  black: '#1a1a2e',
  red: '#d32f2f',
  green: '#388e3c',
  yellow: '#f9a825',
  blue: '#1976d2',
  magenta: '#7b1fa2',
  cyan: '#0097a7',
  white: '#e1e1e6',
  brightBlack: '#5a5a6e',
  brightRed: '#d32f2f',
  brightGreen: '#388e3c',
  brightYellow: '#f9a825',
  brightBlue: '#1976d2',
  brightMagenta: '#7b1fa2',
  brightCyan: '#0097a7',
  brightWhite: '#1a1a2e',
}
```

**Step 2: Build TerminalPane component**

```tsx
// ui/src/components/TerminalPane.tsx
// - useRef for container div and Terminal instance
// - useEffect: create Terminal, load FitAddon + WebLinksAddon, open, fit
// - useEffect: connect WebSocket via usePaneSocket
//   - onOutput → term.write(data)
//   - onMeta → set local meta state
// - useEffect: ResizeObserver on container → fitAddon.fit() → sendResize
// - On mobile (disableStdin: true): don't capture keyboard
// - Render: PaneHeader + terminal container div
// - Cleanup: dispose terminal, close WebSocket
```

Key: import `@xterm/xterm/css/xterm.css` for xterm styles.

**Step 3: Build PaneHeader**

```tsx
// ui/src/components/PaneHeader.tsx
// Thin 24px bar above terminal:
// - Agent icon + type
// - Status text (activity / status)
// - Mode badge (INS/NOR)
// - Choice buttons (when available)
// - Close button (×)
```

**Step 4: Test with a real tmux session**

Run both servers. Click a session in sidebar. Verify:
- xterm.js mounts and shows terminal output
- Typing sends keystrokes (desktop)
- Output updates in real time
- Agent status shows in header

**Step 5: Commit**

```
git add ui/src/components/TerminalPane.tsx ui/src/components/PaneHeader.tsx ui/src/lib/xterm.ts
git commit -m "feat(ui): terminal pane with xterm.js, WebSocket I/O, and agent header"
```

---

### Task 12: Build split pane container with allotment

**Files:**
- Create: `ui/src/components/SplitContainer.tsx`
- Rewrite: `ui/src/components/TerminalArea.tsx` — use SplitContainer

**Step 1: Build SplitContainer**

Recursively renders the `SplitLayout` tree using `allotment`:

```tsx
// ui/src/components/SplitContainer.tsx
// - If layout is 'single': render TerminalPane
// - If layout is 'split': render Allotment with two children (recurse)
// - Allotment handles drag-to-resize
// - Each TerminalPane gets its own pane instance from layout.panes
```

**Step 2: Update TerminalArea to use SplitContainer**

Replace the placeholder with actual SplitContainer rendering.

**Step 3: Test splits**

Open a session. Ctrl+click another session in sidebar. Verify:
- Two terminals appear side by side
- Dragging divider resizes both
- Both have independent WebSocket connections
- Both show live output
- Clicking one focuses it (visual indicator)

**Step 4: Commit**

```
git add ui/src/components/SplitContainer.tsx ui/src/components/TerminalArea.tsx
git commit -m "feat(ui): split pane layout with allotment and recursive rendering"
```

---

### Task 13: Build MobileInputBar

**Files:**
- Create: `ui/src/components/MobileInputBar.tsx`
- Modify: `ui/src/components/TerminalPane.tsx` — render MobileInputBar on mobile

**Step 1: Build MobileInputBar**

```tsx
// ui/src/components/MobileInputBar.tsx
// - Choice buttons row (when choices available from WSMeta)
// - Text input + Send button
// - Voice input button (Web Speech API)
// - Sends input via POST /api/pane/:target/send (full lines, not keystrokes)
```

**Step 2: Integrate into TerminalPane**

On mobile, render MobileInputBar below the xterm.js container. xterm.js has `disableStdin: true`.

**Step 3: Test on mobile viewport**

Use browser dev tools responsive mode. Verify:
- Terminal is read-only
- Input bar appears below
- Choice buttons appear when agent has choices
- Send button works
- Sidebar slides in/out on hamburger

**Step 4: Commit**

```
git add ui/src/components/MobileInputBar.tsx ui/src/components/TerminalPane.tsx
git commit -m "feat(ui): mobile input bar with choices, text input, and voice"
```

---

## Phase 4: Polish & Cleanup

### Task 14: Visual polish and animations

**Files:**
- Modify: `ui/src/theme/tokens.css` — animations, transitions
- Modify: various components — add transition classes

Implement:
- Attention pulse on sidebar items
- Smooth split animation (200ms ease-out)
- Status color transitions (done→idle fade over 2s)
- Frosted sidebar overlay on mobile (backdrop-filter)
- Choice buttons slide-up spring animation
- Invisible split dividers that appear on hover
- Consistent 1px gap separators (no borders)

**Step 1:** Add CSS animations to tokens.css.
**Step 2:** Add transitions to components.
**Step 3:** Visual QA across dark/light themes, mobile/desktop.
**Step 4: Commit**

```
git add -u
git commit -m "feat(ui): visual polish — animations, transitions, attention glow"
```

---

### Task 15: Update flake.nix for React build

**Files:**
- Modify: `flake.nix` — add Node.js build step, update buildGoModule

**Step 1: Update flake.nix**

Add `nodejs` to build inputs. Add `preBuild` phase that runs `cd ui && npm ci && npm run build`. Add `ui/package.json` and `ui/package-lock.json` to nix store.

**Step 2: Add Node.js to devShell**

Add `nodejs` and `nodePackages.npm` to devShell buildInputs.

**Step 3: Test nix build**

Run: `nix build`
Expected: Single binary with embedded React frontend.

**Step 4: Commit**

```
git add flake.nix
git commit -m "build: add React frontend build to nix flake"
```

---

### Task 16: Delete old frontend

Only do this after the React app is fully functional.

**Files:**
- Delete: `views/` — all templ and generated files
- Delete: `static/app.js`, `static/sw.js`
- Delete: old handler code from `server/server.go` (handleIndex, HTML rendering paths)
- Modify: `go.mod` — remove templ dependency
- Modify: `justfile` — remove `generate` task, update `dev`

**Step 1: Remove templ dependency**

```bash
go get -u github.com/a-h/templ@none
go mod tidy
```

**Step 2: Delete old view files**

```bash
gtrash put views/
gtrash put static/app.js static/sw.js
```

**Step 3: Remove old HTML handlers from server.go**

Remove `handleIndex`, the templ rendering paths in `handleSessions` and `handlePane`, and all templ imports. Keep the data-building functions (`buildSessionsData`, `buildAgentStripItems`, etc.) as they're used by the API handlers.

**Step 4: Remove templ from devShell**

Remove `templ` from flake.nix devShell buildInputs.

**Step 5: Verify everything builds and runs**

Run: `go build ./...`
Run: `cd ui && npm run build && cd .. && go run .`

**Step 6: Commit**

```
git add -A
git commit -m "chore: remove old templ frontend, templ dependency, and legacy handlers"
```

---

## Summary

| Phase | Tasks | What it delivers |
|-------|-------|------------------|
| 1: Backend API | Tasks 1-4 | JSON + SSE + WebSocket API alongside existing HTML |
| 2: React Scaffold | Tasks 5-8 | Vite project, go:embed, TypeScript types, API hooks |
| 3: Core UI | Tasks 9-13 | Sidebar, xterm.js terminals, split panes, mobile input |
| 4: Polish & Cleanup | Tasks 14-16 | Animations, nix build, delete old frontend |
