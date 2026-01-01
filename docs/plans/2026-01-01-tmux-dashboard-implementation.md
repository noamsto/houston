# tmux-dashboard MVP Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a mobile-first web dashboard for monitoring Claude Code agents in tmux sessions

**Architecture:** Go HTTP server with SSE streaming, htmx frontend with ansi_up for terminal rendering. Status from hook files + output parsing. Alert-first UI showing sessions needing attention.

**Tech Stack:** Go 1.22+, htmx, Tailwind CSS (CDN), ansi_up, SSE

---

## Task 1: Project Setup

**Files:**
- Create: `go.mod`
- Create: `main.go`

**Step 1: Initialize go module**

```bash
cd /home/noams/Data/git/tmux-dashboard
go mod init github.com/noams/tmux-dashboard
```

**Step 2: Create minimal main.go**

```go
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	flag.Parse()

	fmt.Fprintf(os.Stderr, "tmux-dashboard starting on %s\n", *addr)
}
```

**Step 3: Verify it builds and runs**

```bash
go build -o tmux-dashboard .
./tmux-dashboard
```

Expected: `tmux-dashboard starting on 127.0.0.1:8080`

**Step 4: Commit**

```bash
git add go.mod main.go
git commit -m "feat: initialize go module and main entry point"
```

---

## Task 2: Tmux Client - List Sessions

**Files:**
- Create: `tmux/client.go`
- Create: `tmux/client_test.go`

**Step 1: Write failing test for ListSessions**

```go
// tmux/client_test.go
package tmux

import (
	"testing"
)

func TestParseSessionLine(t *testing.T) {
	line := "main|1735689600|3|1|1735690000"

	session, err := parseSessionLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if session.Name != "main" {
		t.Errorf("expected name 'main', got %q", session.Name)
	}
	if session.Windows != 3 {
		t.Errorf("expected 3 windows, got %d", session.Windows)
	}
	if !session.Attached {
		t.Error("expected attached=true")
	}
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./tmux/... -v
```

Expected: FAIL - package doesn't exist

**Step 3: Create tmux client with Session type and parser**

```go
// tmux/client.go
package tmux

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type Session struct {
	Name         string
	Created      time.Time
	Windows      int
	Attached     bool
	LastActivity time.Time
}

type Client struct {
	tmuxPath string
}

func NewClient() *Client {
	return &Client{tmuxPath: "tmux"}
}

func parseSessionLine(line string) (Session, error) {
	parts := strings.Split(line, "|")
	if len(parts) != 5 {
		return Session{}, fmt.Errorf("invalid session line: %s", line)
	}

	created, _ := strconv.ParseInt(parts[1], 10, 64)
	windows, _ := strconv.Atoi(parts[2])
	attached := parts[3] == "1"
	activity, _ := strconv.ParseInt(parts[4], 10, 64)

	return Session{
		Name:         parts[0],
		Created:      time.Unix(created, 0),
		Windows:      windows,
		Attached:     attached,
		LastActivity: time.Unix(activity, 0),
	}, nil
}

func (c *Client) ListSessions() ([]Session, error) {
	cmd := exec.Command(c.tmuxPath, "list-sessions", "-F",
		"#{session_name}|#{session_created}|#{session_windows}|#{session_attached}|#{session_activity}")

	out, err := cmd.Output()
	if err != nil {
		if strings.Contains(err.Error(), "no server running") {
			return nil, nil
		}
		return nil, err
	}

	var sessions []Session
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		s, err := parseSessionLine(line)
		if err != nil {
			continue
		}
		sessions = append(sessions, s)
	}

	return sessions, nil
}
```

**Step 4: Run test to verify it passes**

```bash
go test ./tmux/... -v
```

Expected: PASS

**Step 5: Commit**

```bash
git add tmux/
git commit -m "feat(tmux): add client with ListSessions"
```

---

## Task 3: Tmux Client - Capture Pane

**Files:**
- Modify: `tmux/client.go`
- Modify: `tmux/client_test.go`

**Step 1: Write failing test for CapturePaneOutput parsing**

```go
// tmux/client_test.go (add to existing file)

func TestCapturePaneOutput(t *testing.T) {
	// This tests the output structure, actual capture requires tmux
	output := `$ echo hello
hello
$ _`

	if len(output) == 0 {
		t.Error("expected non-empty output")
	}
}
```

**Step 2: Add Pane type and CapturePane method**

```go
// tmux/client.go (add to existing file)

type Pane struct {
	Session string
	Window  int
	Index   int
}

func (p Pane) Target() string {
	return fmt.Sprintf("%s:%d.%d", p.Session, p.Window, p.Index)
}

func (c *Client) CapturePane(p Pane, lines int) (string, error) {
	cmd := exec.Command(c.tmuxPath, "capture-pane",
		"-t", p.Target(),
		"-p",
		"-S", fmt.Sprintf("-%d", lines))

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("capture-pane failed: %w", err)
	}

	return string(out), nil
}
```

**Step 3: Run tests**

```bash
go test ./tmux/... -v
```

Expected: PASS

**Step 4: Commit**

```bash
git add tmux/
git commit -m "feat(tmux): add CapturePane method"
```

---

## Task 4: Tmux Client - Send Keys

**Files:**
- Modify: `tmux/client.go`
- Modify: `tmux/client_test.go`

**Step 1: Add SendKeys method**

```go
// tmux/client.go (add to existing file)

func (c *Client) SendKeys(p Pane, keys string, enter bool) error {
	args := []string{"send-keys", "-t", p.Target(), keys}
	if enter {
		args = append(args, "Enter")
	}

	cmd := exec.Command(c.tmuxPath, args...)
	return cmd.Run()
}

func (c *Client) SendSpecialKey(p Pane, key string) error {
	cmd := exec.Command(c.tmuxPath, "send-keys", "-t", p.Target(), key)
	return cmd.Run()
}
```

**Step 2: Run tests**

```bash
go test ./tmux/... -v
```

Expected: PASS

**Step 3: Commit**

```bash
git add tmux/
git commit -m "feat(tmux): add SendKeys method"
```

---

## Task 5: Output Parser - Detect Patterns

**Files:**
- Create: `parser/parser.go`
- Create: `parser/parser_test.go`

**Step 1: Write failing test for choice detection**

```go
// parser/parser_test.go
package parser

import (
	"testing"
)

func TestDetectChoices(t *testing.T) {
	output := `What approach should we use?

1. Option A - do this
2. Option B - do that
3. Option C - something else
4. All of the above`

	result := Parse(output)

	if result.Type != TypeChoice {
		t.Errorf("expected TypeChoice, got %v", result.Type)
	}
	if len(result.Choices) != 4 {
		t.Errorf("expected 4 choices, got %d", len(result.Choices))
	}
	if result.Question != "What approach should we use?" {
		t.Errorf("unexpected question: %q", result.Question)
	}
}

func TestDetectError(t *testing.T) {
	output := `Running build...
Error: missing dependency xyz
Build failed`

	result := Parse(output)

	if result.Type != TypeError {
		t.Errorf("expected TypeError, got %v", result.Type)
	}
	if result.ErrorSnippet == "" {
		t.Error("expected error snippet")
	}
}

func TestDetectQuestion(t *testing.T) {
	output := `I've made the changes.

Does this look right?`

	result := Parse(output)

	if result.Type != TypeQuestion {
		t.Errorf("expected TypeQuestion, got %v", result.Type)
	}
}

func TestDetectIdle(t *testing.T) {
	output := `$ echo done
done
$`

	result := Parse(output)

	if result.Type != TypeIdle {
		t.Errorf("expected TypeIdle, got %v", result.Type)
	}
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./parser/... -v
```

Expected: FAIL - package doesn't exist

**Step 3: Implement parser**

```go
// parser/parser.go
package parser

import (
	"regexp"
	"strings"
)

type ResultType int

const (
	TypeIdle ResultType = iota
	TypeWorking
	TypeQuestion
	TypeChoice
	TypeError
)

func (t ResultType) String() string {
	return [...]string{"idle", "working", "question", "choice", "error"}[t]
}

type Result struct {
	Type         ResultType
	Question     string
	Choices      []string
	ErrorSnippet string
}

var (
	choicePattern   = regexp.MustCompile(`(?m)^\s*([1-4])[.)\]]\s+(.+)$`)
	questionPattern = regexp.MustCompile(`(?m)^(.+\?)\s*$`)
	errorPattern    = regexp.MustCompile(`(?mi)(error|failed|exception)[:\s]+(.{0,100})`)
	approvalPattern = regexp.MustCompile(`(?i)(proceed|continue|look right|does this|should i)\?`)
)

func Parse(output string) Result {
	lines := strings.Split(output, "\n")
	lastLines := lastN(lines, 30)
	text := strings.Join(lastLines, "\n")

	// Check for errors first (highest priority)
	if matches := errorPattern.FindStringSubmatch(text); len(matches) > 0 {
		return Result{
			Type:         TypeError,
			ErrorSnippet: strings.TrimSpace(matches[0]),
		}
	}

	// Check for multiple choice
	choiceMatches := choicePattern.FindAllStringSubmatch(text, -1)
	if len(choiceMatches) >= 2 {
		var choices []string
		for _, m := range choiceMatches {
			choices = append(choices, strings.TrimSpace(m[2]))
		}

		// Find the question before choices
		question := ""
		if qMatches := questionPattern.FindAllStringSubmatch(text, -1); len(qMatches) > 0 {
			question = strings.TrimSpace(qMatches[len(qMatches)-1][1])
		}

		return Result{
			Type:     TypeChoice,
			Question: question,
			Choices:  choices,
		}
	}

	// Check for approval/confirmation question
	if approvalPattern.MatchString(text) {
		if qMatches := questionPattern.FindAllStringSubmatch(text, -1); len(qMatches) > 0 {
			return Result{
				Type:     TypeQuestion,
				Question: strings.TrimSpace(qMatches[len(qMatches)-1][1]),
			}
		}
	}

	// Check for general question
	if qMatches := questionPattern.FindAllStringSubmatch(text, -1); len(qMatches) > 0 {
		lastQ := qMatches[len(qMatches)-1][1]
		// Only flag as question if it's near the end
		if strings.Contains(strings.Join(lastN(lines, 5), "\n"), lastQ) {
			return Result{
				Type:     TypeQuestion,
				Question: strings.TrimSpace(lastQ),
			}
		}
	}

	return Result{Type: TypeIdle}
}

func lastN(slice []string, n int) []string {
	if len(slice) <= n {
		return slice
	}
	return slice[len(slice)-n:]
}
```

**Step 4: Run tests**

```bash
go test ./parser/... -v
```

Expected: PASS

**Step 5: Commit**

```bash
git add parser/
git commit -m "feat(parser): add output pattern detection"
```

---

## Task 6: Status Watcher - Hook Files

**Files:**
- Create: `status/watcher.go`
- Create: `status/watcher_test.go`

**Step 1: Write failing test for status file parsing**

```go
// status/watcher_test.go
package status

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadStatusFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-session")

	err := os.WriteFile(path, []byte("needs_attention"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	status, err := readStatusFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if status != StatusNeedsAttention {
		t.Errorf("expected StatusNeedsAttention, got %v", status)
	}
}

func TestWatcherGetAll(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "session1"), []byte("needs_attention"), 0644)
	os.WriteFile(filepath.Join(dir, "session2"), []byte("idle"), 0644)

	w := NewWatcher(dir)
	statuses := w.GetAll()

	if len(statuses) != 2 {
		t.Errorf("expected 2 statuses, got %d", len(statuses))
	}
}
```

**Step 2: Run test to verify it fails**

```bash
go test ./status/... -v
```

Expected: FAIL - package doesn't exist

**Step 3: Implement watcher**

```go
// status/watcher.go
package status

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Status int

const (
	StatusUnknown Status = iota
	StatusIdle
	StatusWorking
	StatusNeedsAttention
)

func (s Status) String() string {
	return [...]string{"unknown", "idle", "working", "needs_attention"}[s]
}

type SessionStatus struct {
	Session   string
	Status    Status
	UpdatedAt time.Time
}

type Watcher struct {
	dir string
}

func NewWatcher(dir string) *Watcher {
	return &Watcher{dir: dir}
}

func readStatusFile(path string) (Status, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return StatusUnknown, err
	}

	content := strings.TrimSpace(string(data))
	switch content {
	case "needs_attention":
		return StatusNeedsAttention, nil
	case "working":
		return StatusWorking, nil
	case "idle":
		return StatusIdle, nil
	default:
		return StatusUnknown, nil
	}
}

func (w *Watcher) GetAll() map[string]SessionStatus {
	result := make(map[string]SessionStatus)

	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return result
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		path := filepath.Join(w.dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}

		status, err := readStatusFile(path)
		if err != nil {
			continue
		}

		result[entry.Name()] = SessionStatus{
			Session:   entry.Name(),
			Status:    status,
			UpdatedAt: info.ModTime(),
		}
	}

	return result
}

func (w *Watcher) Get(session string) (SessionStatus, bool) {
	path := filepath.Join(w.dir, session)
	info, err := os.Stat(path)
	if err != nil {
		return SessionStatus{}, false
	}

	status, err := readStatusFile(path)
	if err != nil {
		return SessionStatus{}, false
	}

	return SessionStatus{
		Session:   session,
		Status:    status,
		UpdatedAt: info.ModTime(),
	}, true
}
```

**Step 4: Run tests**

```bash
go test ./status/... -v
```

Expected: PASS

**Step 5: Commit**

```bash
git add status/
git commit -m "feat(status): add hook file watcher"
```

---

## Task 7: HTTP Server Setup

**Files:**
- Create: `server/server.go`
- Modify: `main.go`

**Step 1: Create server struct**

```go
// server/server.go
package server

import (
	"html/template"
	"net/http"
	"sync"

	"github.com/noams/tmux-dashboard/status"
	"github.com/noams/tmux-dashboard/tmux"
)

type Server struct {
	tmux      *tmux.Client
	watcher   *status.Watcher
	templates *template.Template
	mu        sync.RWMutex
}

type Config struct {
	StatusDir string
}

func New(cfg Config) (*Server, error) {
	tmpl, err := template.ParseGlob("templates/*.html")
	if err != nil {
		return nil, err
	}

	return &Server{
		tmux:      tmux.NewClient(),
		watcher:   status.NewWatcher(cfg.StatusDir),
		templates: tmpl,
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Static files
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	// Routes
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/sessions", s.handleSessions)
	mux.HandleFunc("/pane/", s.handlePane)

	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	s.templates.ExecuteTemplate(w, "index.html", nil)
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	// Will implement SSE vs HTML based on Accept header
	sessions, _ := s.tmux.ListSessions()
	statuses := s.watcher.GetAll()

	data := struct {
		Sessions []tmux.Session
		Statuses map[string]status.SessionStatus
	}{
		Sessions: sessions,
		Statuses: statuses,
	}

	s.templates.ExecuteTemplate(w, "sessions.html", data)
}

func (s *Server) handlePane(w http.ResponseWriter, r *http.Request) {
	// Will implement in next task
	w.Write([]byte("pane handler"))
}
```

**Step 2: Update main.go to use server**

```go
// main.go
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/noams/tmux-dashboard/server"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	statusDir := flag.String("status-dir", "", "Directory for hook status files")
	flag.Parse()

	if *statusDir == "" {
		home, _ := os.UserHomeDir()
		*statusDir = filepath.Join(home, ".local", "state", "claude")
	}

	srv, err := server.New(server.Config{
		StatusDir: *statusDir,
	})
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
	}

	fmt.Fprintf(os.Stderr, "tmux-dashboard starting on http://%s\n", *addr)
	fmt.Fprintf(os.Stderr, "status directory: %s\n", *statusDir)

	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}
```

**Step 3: Verify it compiles**

```bash
go build ./...
```

Expected: Success (templates will fail at runtime until created)

**Step 4: Commit**

```bash
git add server/ main.go
git commit -m "feat(server): add HTTP server setup"
```

---

## Task 8: HTML Templates - Layout

**Files:**
- Create: `templates/layout.html`
- Create: `templates/index.html`
- Create: `templates/sessions.html`

**Step 1: Create base layout with htmx and Tailwind**

```html
<!-- templates/layout.html -->
{{define "layout"}}
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{block "title" .}}tmux-dashboard{{end}}</title>
    <script src="https://cdn.tailwindcss.com"></script>
    <script src="https://unpkg.com/htmx.org@1.9.10"></script>
    <script src="https://unpkg.com/htmx.org@1.9.10/dist/ext/sse.js"></script>
    <script src="https://cdn.jsdelivr.net/npm/ansi_up@6/ansi_up.min.js"></script>
    <style>
        .terminal { font-family: ui-monospace, monospace; }
    </style>
</head>
<body class="bg-gray-100 min-h-screen">
    {{block "content" .}}{{end}}
    <script src="/static/app.js"></script>
</body>
</html>
{{end}}
```

**Step 2: Create index page**

```html
<!-- templates/index.html -->
{{template "layout" .}}

{{define "title"}}tmux-dashboard{{end}}

{{define "content"}}
<div class="max-w-lg mx-auto p-4">
    <header class="flex items-center justify-between mb-6">
        <h1 class="text-xl font-bold text-gray-800">tmux-dashboard</h1>
        <button
            hx-get="/sessions"
            hx-target="#sessions"
            hx-swap="innerHTML"
            class="p-2 rounded-full hover:bg-gray-200"
        >
            <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"></path>
            </svg>
        </button>
    </header>

    <div
        id="sessions"
        hx-ext="sse"
        sse-connect="/sessions?stream=1"
        sse-swap="message"
        hx-swap="innerHTML"
    >
        <div class="text-center py-8 text-gray-500">
            Loading sessions...
        </div>
    </div>
</div>
{{end}}
```

**Step 3: Create sessions partial**

```html
<!-- templates/sessions.html -->
{{define "sessions"}}
{{if .NeedsAttention}}
<section class="mb-6">
    <h2 class="text-sm font-semibold text-gray-600 mb-3 uppercase tracking-wide">
        Needs Attention ({{len .NeedsAttention}})
    </h2>
    {{range .NeedsAttention}}
    <a href="/pane/{{.Session.Name}}:0.0" class="block mb-3">
        <div class="bg-white rounded-lg shadow p-4 border-l-4 border-red-500">
            <div class="flex items-center gap-2 mb-2">
                <span class="w-3 h-3 rounded-full {{if eq .ParseResult.Type.String "error"}}bg-red-500{{else}}bg-orange-500{{end}}"></span>
                <span class="font-medium text-gray-900">{{.Session.Name}}</span>
            </div>
            <p class="text-sm text-gray-600 mb-2">
                {{if eq .ParseResult.Type.String "error"}}Error encountered
                {{else if eq .ParseResult.Type.String "choice"}}Waiting for choice
                {{else if eq .ParseResult.Type.String "question"}}Waiting for input
                {{else}}Needs attention{{end}}
            </p>
            {{if .ParseResult.Question}}
            <p class="text-sm text-gray-800 italic">"{{truncate .ParseResult.Question 60}}"</p>
            {{end}}
            {{if .ParseResult.Choices}}
            <div class="flex gap-2 mt-3">
                {{range $i, $c := .ParseResult.Choices}}
                {{if lt $i 4}}
                <button
                    hx-post="/pane/{{$.Session.Name}}:0.0/send"
                    hx-vals='{"input": "{{add $i 1}}"}'
                    class="px-3 py-1 text-sm bg-gray-100 hover:bg-gray-200 rounded"
                    onclick="event.stopPropagation()"
                >{{add $i 1}}</button>
                {{end}}
                {{end}}
            </div>
            {{end}}
            <p class="text-xs text-gray-400 mt-2">{{timeAgo .Session.LastActivity}}</p>
        </div>
    </a>
    {{end}}
</section>
{{end}}

{{if .OtherSessions}}
<section>
    <h2 class="text-sm font-semibold text-gray-600 mb-3 uppercase tracking-wide">
        Other Sessions ({{len .OtherSessions}})
    </h2>
    <div class="bg-white rounded-lg shadow divide-y">
        {{range .OtherSessions}}
        <a href="/pane/{{.Name}}:0.0" class="flex items-center gap-3 p-3 hover:bg-gray-50">
            <span class="w-2 h-2 rounded-full {{if .Attached}}bg-green-500{{else}}bg-gray-300{{end}}"></span>
            <span class="text-gray-700">{{.Name}}</span>
            <span class="text-xs text-gray-400 ml-auto">{{.Windows}} win</span>
        </a>
        {{end}}
    </div>
</section>
{{end}}

{{if and (not .NeedsAttention) (not .OtherSessions)}}
<div class="text-center py-12 text-gray-500">
    <p>No tmux sessions running</p>
    <p class="text-sm mt-2">Start a tmux session to see it here</p>
</div>
{{end}}
{{end}}
```

**Step 4: Create empty static/app.js**

```bash
mkdir -p static
cat > static/app.js << 'EOF'
// tmux-dashboard client-side JS
document.addEventListener('DOMContentLoaded', function() {
    console.log('tmux-dashboard loaded');
});
EOF
```

**Step 5: Commit**

```bash
git add templates/ static/
git commit -m "feat(templates): add layout and session list templates"
```

---

## Task 9: Template Functions & Session Handler

**Files:**
- Modify: `server/server.go`
- Create: `server/funcs.go`

**Step 1: Create template functions**

```go
// server/funcs.go
package server

import (
	"html/template"
	"time"
)

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"add": func(a, b int) int {
			return a + b
		},
		"truncate": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "..."
		},
		"timeAgo": func(t time.Time) string {
			d := time.Since(t)
			switch {
			case d < time.Minute:
				return "just now"
			case d < time.Hour:
				m := int(d.Minutes())
				if m == 1 {
					return "1m ago"
				}
				return string(rune(m)) + "m ago"
			case d < 24*time.Hour:
				h := int(d.Hours())
				if h == 1 {
					return "1h ago"
				}
				return string(rune(h)) + "h ago"
			default:
				return t.Format("Jan 2")
			}
		},
	}
}
```

**Step 2: Update server to use functions and build session data**

```go
// server/server.go - replace the New function and handleSessions
package server

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"sync"

	"github.com/noams/tmux-dashboard/parser"
	"github.com/noams/tmux-dashboard/status"
	"github.com/noams/tmux-dashboard/tmux"
)

type Server struct {
	tmux      *tmux.Client
	watcher   *status.Watcher
	templates *template.Template
	mu        sync.RWMutex
}

type Config struct {
	StatusDir string
}

func New(cfg Config) (*Server, error) {
	tmpl, err := template.New("").Funcs(templateFuncs()).ParseGlob("templates/*.html")
	if err != nil {
		return nil, err
	}

	return &Server{
		tmux:      tmux.NewClient(),
		watcher:   status.NewWatcher(cfg.StatusDir),
		templates: tmpl,
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/sessions", s.handleSessions)
	mux.HandleFunc("/pane/", s.handlePane)

	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	s.templates.ExecuteTemplate(w, "index.html", nil)
}

type sessionWithStatus struct {
	Session     tmux.Session
	Status      status.SessionStatus
	ParseResult parser.Result
}

type sessionsData struct {
	NeedsAttention []sessionWithStatus
	OtherSessions  []tmux.Session
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	accept := r.Header.Get("Accept")

	// Check if SSE stream requested
	if strings.Contains(accept, "text/event-stream") || r.URL.Query().Get("stream") == "1" {
		s.streamSessions(w, r)
		return
	}

	data := s.buildSessionsData()
	s.templates.ExecuteTemplate(w, "sessions", data)
}

func (s *Server) buildSessionsData() sessionsData {
	sessions, _ := s.tmux.ListSessions()
	statuses := s.watcher.GetAll()

	var data sessionsData

	for _, sess := range sessions {
		st, hasStatus := statuses[sess.Name]

		// Capture pane output for parsing
		pane := tmux.Pane{Session: sess.Name, Window: 0, Index: 0}
		output, _ := s.tmux.CapturePane(pane, 100)
		parseResult := parser.Parse(output)

		needsAttention := hasStatus && st.Status == status.StatusNeedsAttention
		needsAttention = needsAttention || parseResult.Type == parser.TypeError
		needsAttention = needsAttention || parseResult.Type == parser.TypeChoice
		needsAttention = needsAttention || parseResult.Type == parser.TypeQuestion

		if needsAttention {
			data.NeedsAttention = append(data.NeedsAttention, sessionWithStatus{
				Session:     sess,
				Status:      st,
				ParseResult: parseResult,
			})
		} else {
			data.OtherSessions = append(data.OtherSessions, sess)
		}
	}

	return data
}

func (s *Server) streamSessions(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Send initial data
	s.sendSessionsEvent(w, flusher)

	// Poll and send updates
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			s.sendSessionsEvent(w, flusher)
		}
	}
}

func (s *Server) sendSessionsEvent(w http.ResponseWriter, flusher http.Flusher) {
	var buf strings.Builder
	data := s.buildSessionsData()
	s.templates.ExecuteTemplate(&buf, "sessions", data)

	// SSE format: data lines followed by blank line
	for _, line := range strings.Split(buf.String(), "\n") {
		fmt.Fprintf(w, "data: %s\n", line)
	}
	fmt.Fprintf(w, "\n")
	flusher.Flush()
}

func (s *Server) handlePane(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("pane handler - will implement next"))
}
```

**Step 3: Fix timeAgo function**

```go
// server/funcs.go - fix timeAgo
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"add": func(a, b int) int {
			return a + b
		},
		"truncate": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "..."
		},
		"timeAgo": func(t time.Time) string {
			d := time.Since(t)
			switch {
			case d < time.Minute:
				return "just now"
			case d < time.Hour:
				m := int(d.Minutes())
				return fmt.Sprintf("%dm ago", m)
			case d < 24*time.Hour:
				h := int(d.Hours())
				return fmt.Sprintf("%dh ago", h)
			default:
				return t.Format("Jan 2")
			}
		},
	}
}
```

**Step 4: Add missing import to funcs.go**

```go
// server/funcs.go - add import
package server

import (
	"fmt"
	"html/template"
	"time"
)
```

**Step 5: Verify it compiles**

```bash
go build ./...
```

**Step 6: Commit**

```bash
git add server/
git commit -m "feat(server): add template functions and session data builder"
```

---

## Task 10: Pane View Handler & Template

**Files:**
- Create: `templates/pane.html`
- Modify: `server/server.go`

**Step 1: Create pane view template**

```html
<!-- templates/pane.html -->
{{template "layout" .}}

{{define "title"}}{{.Pane.Session}} - tmux-dashboard{{end}}

{{define "content"}}
<div class="flex flex-col h-screen max-w-lg mx-auto">
    <!-- Header -->
    <header class="flex items-center gap-3 p-4 bg-white border-b">
        <a href="/" class="p-2 -ml-2 hover:bg-gray-100 rounded">
            <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 19l-7-7 7-7"></path>
            </svg>
        </a>
        <h1 class="font-medium text-gray-900 flex-1">{{.Pane.Session}}</h1>
        <div class="relative" x-data="{ open: false }">
            <button class="p-2 hover:bg-gray-100 rounded" onclick="toggleMenu(this)">
                <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 5v.01M12 12v.01M12 19v.01M12 6a1 1 0 110-2 1 1 0 010 2zm0 7a1 1 0 110-2 1 1 0 010 2zm0 7a1 1 0 110-2 1 1 0 010 2z"></path>
                </svg>
            </button>
            <div id="menu" class="hidden absolute right-0 mt-2 w-48 bg-white rounded-lg shadow-lg border z-10">
                <button
                    hx-post="/pane/{{.Pane.Target}}/send"
                    hx-vals='{"input": "C-c", "special": true}'
                    class="w-full text-left px-4 py-2 hover:bg-gray-100"
                >Send Ctrl+C</button>
                <button
                    onclick="scrollToTop()"
                    class="w-full text-left px-4 py-2 hover:bg-gray-100"
                >Scroll to top</button>
                <button
                    hx-get="/pane/{{.Pane.Target}}"
                    hx-target="#output"
                    hx-swap="innerHTML"
                    class="w-full text-left px-4 py-2 hover:bg-gray-100"
                >Refresh</button>
            </div>
        </div>
    </header>

    <!-- Output area -->
    <div
        id="output-container"
        class="flex-1 overflow-y-auto bg-gray-900"
        hx-ext="sse"
        sse-connect="/pane/{{.Pane.Target}}?stream=1"
        sse-swap="message"
        hx-target="#output"
        hx-swap="innerHTML"
    >
        <pre id="output" class="terminal text-sm text-gray-100 p-4 whitespace-pre-wrap">{{.Output}}</pre>
    </div>

    {{if .ParseResult.Choices}}
    <!-- Quick choice buttons -->
    <div class="flex gap-2 p-3 bg-gray-800 border-t border-gray-700">
        {{range $i, $c := .ParseResult.Choices}}
        {{if lt $i 4}}
        <button
            hx-post="/pane/{{$.Pane.Target}}/send"
            hx-vals='{"input": "{{add $i 1}}"}'
            hx-swap="none"
            class="flex-1 px-3 py-2 text-sm bg-gray-700 hover:bg-gray-600 text-white rounded"
        >{{add $i 1}}</button>
        {{end}}
        {{end}}
    </div>
    {{end}}

    <!-- Input bar -->
    <form
        hx-post="/pane/{{.Pane.Target}}/send"
        hx-swap="none"
        hx-on::after-request="this.reset()"
        class="flex gap-2 p-3 bg-white border-t"
    >
        <input
            type="text"
            name="input"
            placeholder="Send input..."
            autocomplete="off"
            class="flex-1 px-4 py-2 border rounded-lg focus:outline-none focus:ring-2 focus:ring-blue-500"
        >
        <button
            type="submit"
            class="px-4 py-2 bg-blue-500 text-white rounded-lg hover:bg-blue-600"
        >
            <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 5l7 7-7 7M5 5l7 7-7 7"></path>
            </svg>
        </button>
    </form>
</div>

<script>
    // Auto-scroll to bottom on load
    document.addEventListener('DOMContentLoaded', function() {
        const container = document.getElementById('output-container');
        container.scrollTop = container.scrollHeight;
    });

    // Convert ANSI codes in output
    document.addEventListener('htmx:afterSwap', function(e) {
        if (e.target.id === 'output') {
            const ansi = new AnsiUp();
            e.target.innerHTML = ansi.ansi_to_html(e.target.textContent);
            e.target.parentElement.scrollTop = e.target.parentElement.scrollHeight;
        }
    });

    function toggleMenu(btn) {
        const menu = document.getElementById('menu');
        menu.classList.toggle('hidden');
    }

    function scrollToTop() {
        document.getElementById('output-container').scrollTop = 0;
        document.getElementById('menu').classList.add('hidden');
    }

    // Close menu when clicking outside
    document.addEventListener('click', function(e) {
        const menu = document.getElementById('menu');
        if (!e.target.closest('#menu') && !e.target.closest('[onclick="toggleMenu(this)"]')) {
            menu.classList.add('hidden');
        }
    });
</script>
{{end}}
```

**Step 2: Add pane handler to server**

```go
// server/server.go - replace handlePane and add helper

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/noams/tmux-dashboard/parser"
	"github.com/noams/tmux-dashboard/status"
	"github.com/noams/tmux-dashboard/tmux"
)

func parsePaneTarget(path string) (tmux.Pane, error) {
	// Path format: /pane/session:window.pane or /pane/session:window.pane/send
	path = strings.TrimPrefix(path, "/pane/")
	path = strings.TrimSuffix(path, "/send")

	// Parse session:window.pane
	var session string
	var window, pane int

	colonIdx := strings.Index(path, ":")
	if colonIdx == -1 {
		return tmux.Pane{Session: path, Window: 0, Index: 0}, nil
	}

	session = path[:colonIdx]
	rest := path[colonIdx+1:]

	dotIdx := strings.Index(rest, ".")
	if dotIdx == -1 {
		fmt.Sscanf(rest, "%d", &window)
	} else {
		fmt.Sscanf(rest[:dotIdx], "%d", &window)
		fmt.Sscanf(rest[dotIdx+1:], "%d", &pane)
	}

	return tmux.Pane{Session: session, Window: window, Index: pane}, nil
}

func (s *Server) handlePane(w http.ResponseWriter, r *http.Request) {
	pane, err := parsePaneTarget(r.URL.Path)
	if err != nil {
		http.Error(w, "invalid pane target", http.StatusBadRequest)
		return
	}

	// Handle send action
	if strings.HasSuffix(r.URL.Path, "/send") {
		s.handlePaneSend(w, r, pane)
		return
	}

	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "text/event-stream") || r.URL.Query().Get("stream") == "1" {
		s.streamPane(w, r, pane)
		return
	}

	output, err := s.tmux.CapturePane(pane, 500)
	if err != nil {
		http.Error(w, "failed to capture pane: "+err.Error(), http.StatusInternalServerError)
		return
	}

	parseResult := parser.Parse(output)

	data := struct {
		Pane        tmux.Pane
		Output      string
		ParseResult parser.Result
	}{
		Pane:        pane,
		Output:      output,
		ParseResult: parseResult,
	}

	s.templates.ExecuteTemplate(w, "pane.html", data)
}

func (s *Server) handlePaneSend(w http.ResponseWriter, r *http.Request, pane tmux.Pane) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.ParseForm()
	input := r.FormValue("input")
	special := r.FormValue("special") == "true"

	var err error
	if special {
		err = s.tmux.SendSpecialKey(pane, input)
	} else {
		err = s.tmux.SendKeys(pane, input, true)
	}

	if err != nil {
		http.Error(w, "failed to send keys: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) streamPane(w http.ResponseWriter, r *http.Request, pane tmux.Pane) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	var lastOutput string

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			output, err := s.tmux.CapturePane(pane, 500)
			if err != nil {
				continue
			}

			if output != lastOutput {
				lastOutput = output
				for _, line := range strings.Split(output, "\n") {
					fmt.Fprintf(w, "data: %s\n", line)
				}
				fmt.Fprintf(w, "\n")
				flusher.Flush()
			}
		}
	}
}
```

**Step 3: Verify it compiles**

```bash
go build ./...
```

**Step 4: Commit**

```bash
git add templates/pane.html server/
git commit -m "feat(pane): add pane view with output streaming and input"
```

---

## Task 11: Integration Test & Polish

**Files:**
- Modify: `static/app.js`
- Create: `Makefile`

**Step 1: Enhance app.js for better UX**

```javascript
// static/app.js
document.addEventListener('DOMContentLoaded', function() {
    console.log('tmux-dashboard loaded');

    // Update page title with attention count
    function updateTitle() {
        const needsAttention = document.querySelectorAll('[class*="border-red-500"]').length;
        if (needsAttention > 0) {
            document.title = `(${needsAttention}) tmux-dashboard`;
        } else {
            document.title = 'tmux-dashboard';
        }
    }

    // Run on initial load and after htmx swaps
    updateTitle();
    document.body.addEventListener('htmx:afterSwap', updateTitle);

    // Handle SSE reconnection
    document.body.addEventListener('htmx:sseError', function(e) {
        console.log('SSE connection lost, will reconnect...');
    });

    // Auto-scroll output on new content
    document.body.addEventListener('htmx:afterSwap', function(e) {
        if (e.target.id === 'output') {
            const container = document.getElementById('output-container');
            if (container) {
                // Only auto-scroll if already near bottom
                const isNearBottom = container.scrollHeight - container.scrollTop - container.clientHeight < 100;
                if (isNearBottom) {
                    container.scrollTop = container.scrollHeight;
                }
            }
        }
    });
});
```

**Step 2: Create Makefile for convenience**

```makefile
# Makefile
.PHONY: build run dev clean

build:
	go build -o tmux-dashboard .

run: build
	./tmux-dashboard

dev:
	go run . -addr localhost:8080

clean:
	rm -f tmux-dashboard

test:
	go test ./... -v

lint:
	golangci-lint run
```

**Step 3: Commit**

```bash
git add static/app.js Makefile
git commit -m "feat: add client JS polish and Makefile"
```

---

## Task 12: Manual Integration Test

**Step 1: Start the server**

```bash
make dev
```

**Step 2: Open in browser**

Open http://localhost:8080 in your browser (or phone via Tailscale)

**Step 3: Verify functionality**

- [ ] Home page loads with session list
- [ ] Sessions categorized into "Needs Attention" and "Other"
- [ ] SSE streaming updates sessions
- [ ] Clicking session opens pane view
- [ ] Pane output displays with ANSI colors
- [ ] Input bar sends text to pane
- [ ] Quick choice buttons appear for multiple choice
- [ ] Ctrl+C button works
- [ ] Mobile layout is responsive

**Step 4: Fix any issues found**

**Step 5: Final commit**

```bash
git add -A
git commit -m "feat: complete MVP implementation"
```

---

## Summary

| Task | Component | Description |
|------|-----------|-------------|
| 1 | Setup | Go module and main entry point |
| 2 | tmux | ListSessions |
| 3 | tmux | CapturePane |
| 4 | tmux | SendKeys |
| 5 | parser | Output pattern detection |
| 6 | status | Hook file watcher |
| 7 | server | HTTP server setup |
| 8 | templates | Layout and index |
| 9 | server | Template functions and session handler |
| 10 | pane | Pane view with streaming |
| 11 | polish | Client JS and Makefile |
| 12 | test | Manual integration test |

**Total: ~12 tasks, each 5-15 minutes**
