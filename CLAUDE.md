# tmux-dashboard

A mobile-friendly web dashboard for monitoring and controlling tmux sessions remotely. Built for checking on AI agents and sending them instructions from your phone.

## Project Goals

1. **View tmux sessions** - See all sessions with status at a glance
2. **Monitor output** - Live-stream terminal output from any session/window/pane
3. **Send commands** - Text input with voice-to-text support for giving instructions
4. **Mobile-first** - Touch-friendly UI that works well on phones
5. **Simple deployment** - Single binary, runs as systemd service on NixOS

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                   Mobile Browser                     │
│  ┌──────────────────────────────────────────────────┐
│  │  Session List  │  Terminal View  │  Input Bar   │
│  │  (cards)       │  (live output)  │  (+ voice)   │
│  └──────────────────────────────────────────────────┘
└─────────────────────────────────────────────────────┘
                          │
              WebSocket + htmx (HTML fragments)
                          │
                          ▼
┌─────────────────────────────────────────────────────┐
│                  Go HTTP Server                      │
│                                                      │
│  Routes:                                             │
│  GET  /                    - Dashboard home          │
│  GET  /sessions            - List sessions (htmx)    │
│  GET  /sessions/:id        - Session detail view     │
│  GET  /sessions/:id/output - Pane output (htmx)      │
│  POST /sessions/:id/send   - Send keys to pane       │
│  WS   /sessions/:id/stream - Live output stream      │
│                                                      │
└─────────────────────────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────┐
│                   tmux CLI                           │
│                                                      │
│  tmux list-sessions -F "#{...}"                      │
│  tmux list-windows -t session -F "#{...}"            │
│  tmux capture-pane -t session:window.pane -p         │
│  tmux send-keys -t session:window.pane "text" Enter  │
│                                                      │
└─────────────────────────────────────────────────────┘
```

## Tech Stack

| Component | Choice | Rationale |
|-----------|--------|-----------|
| Backend | Go | Single binary, fast, good tmux process handling |
| Frontend | htmx + HTML templates | Simple, server-rendered, minimal JS |
| Styling | Tailwind CSS (CDN) | Mobile-first utilities, fast iteration |
| Live updates | WebSocket | Real-time terminal output streaming |
| Voice input | Web Speech API | Built into browsers, no backend needed |
| Auth | None initially | Rely on Tailscale/SSH tunnel for security |

## Features

### MVP (v0.1)

- [ ] List all tmux sessions with window count
- [ ] View session detail (list windows/panes)
- [ ] Display recent output from a pane (last 100 lines)
- [ ] Send text input to a pane
- [ ] Auto-refresh output every 2 seconds
- [ ] Mobile-responsive layout

### v0.2

- [ ] WebSocket live streaming (replace polling)
- [ ] Voice-to-text input (Web Speech API)
- [ ] Session status indicators (active/idle/has-activity)
- [ ] Quick actions (scroll up/down, clear, ctrl+c)

### v0.3

- [ ] Search/filter sessions
- [ ] Pane output highlighting (errors in red, etc.)
- [ ] Keyboard shortcuts for power users
- [ ] Dark/light theme (match system)

### Future Ideas

- Claude agent status integration (parse output for status indicators)
- Notification on activity (via service worker)
- Multiple server support (SSH to different hosts)
- Session creation/killing from UI

## UI Design

### Mobile Layout (Primary)

```
┌─────────────────────────────┐
│  tmux-dashboard        [☰]  │  <- Header with menu
├─────────────────────────────┤
│ ┌─────────────────────────┐ │
│ │ 🟢 main                 │ │  <- Session cards
│ │    3 windows            │ │     (tap to expand)
│ └─────────────────────────┘ │
│ ┌─────────────────────────┐ │
│ │ 🟡 nix-config           │ │
│ │    2 windows • activity │ │
│ └─────────────────────────┘ │
│ ┌─────────────────────────┐ │
│ │ 🔵 claude-agent-1       │ │
│ │    1 window • running   │ │
│ └─────────────────────────┘ │
├─────────────────────────────┤
│                             │
│  [Terminal output area]     │  <- Selected pane output
│  $ claude --chat            │     (scrollable)
│  > Working on task...       │
│  > Reading file xyz.nix     │
│                             │
├─────────────────────────────┤
│ [________________] [🎤] [↵] │  <- Input bar with voice
└─────────────────────────────┘
```

### Session Card States

- 🟢 Green dot: Recently active (output in last 30s)
- 🟡 Yellow dot: Has unseen activity
- 🔵 Blue dot: Idle
- ⚪ Gray dot: No recent activity

## API Design

### tmux Data Structures

```go
type Session struct {
    Name        string    `json:"name"`
    Created     time.Time `json:"created"`
    Windows     int       `json:"windows"`
    Attached    bool      `json:"attached"`
    LastActivity time.Time `json:"last_activity"`
}

type Window struct {
    Index   int    `json:"index"`
    Name    string `json:"name"`
    Active  bool   `json:"active"`
    Panes   int    `json:"panes"`
}

type Pane struct {
    Index   int    `json:"index"`
    Active  bool   `json:"active"`
    Command string `json:"command"`
    Pid     int    `json:"pid"`
}
```

### tmux Commands Reference

```bash
# List sessions
tmux list-sessions -F "#{session_name}|#{session_created}|#{session_windows}|#{session_attached}|#{session_activity}"

# List windows in session
tmux list-windows -t "session" -F "#{window_index}|#{window_name}|#{window_active}|#{window_panes}"

# List panes in window
tmux list-panes -t "session:window" -F "#{pane_index}|#{pane_active}|#{pane_current_command}|#{pane_pid}"

# Capture pane output (last 100 lines)
tmux capture-pane -t "session:window.pane" -p -S -100

# Send keys to pane
tmux send-keys -t "session:window.pane" "command text" Enter

# Send special keys
tmux send-keys -t "session:window.pane" C-c  # Ctrl+C
tmux send-keys -t "session:window.pane" C-l  # Clear
```

## Voice Input Implementation

```javascript
// Web Speech API - works in Chrome, Safari, Edge on mobile
const recognition = new webkitSpeechRecognition();
recognition.continuous = false;
recognition.interimResults = true;
recognition.lang = 'en-US';

recognition.onresult = (event) => {
    const transcript = event.results[0][0].transcript;
    document.getElementById('input').value = transcript;
};

// Trigger with microphone button
document.getElementById('voice-btn').onclick = () => recognition.start();
```

## Security Considerations

**Initial approach: No built-in auth**

Rely on network-level security:
1. Bind to localhost only (`127.0.0.1:8080`)
2. Access via Tailscale (recommended)
3. Or SSH tunnel: `ssh -L 8080:localhost:8080 host`

**Future auth options:**
- Basic auth with password
- Tailscale auth headers
- mTLS with client certificates

## NixOS Integration

### Package Definition

```nix
{
  lib,
  buildGoModule,
  fetchFromGitHub,
}:

buildGoModule rec {
  pname = "tmux-dashboard";
  version = "0.1.0";

  src = ./.;

  vendorHash = "sha256-AAAA...";  # Update after go mod tidy

  meta = with lib; {
    description = "Mobile-friendly web dashboard for tmux";
    homepage = "https://github.com/USER/tmux-dashboard";
    license = licenses.mit;
    maintainers = [];
  };
}
```

### NixOS Module

```nix
{ config, lib, pkgs, ... }:

with lib;

let
  cfg = config.services.tmux-dashboard;
in {
  options.services.tmux-dashboard = {
    enable = mkEnableOption "tmux-dashboard web interface";

    port = mkOption {
      type = types.port;
      default = 8080;
      description = "Port to listen on";
    };

    address = mkOption {
      type = types.str;
      default = "127.0.0.1";
      description = "Address to bind to";
    };

    user = mkOption {
      type = types.str;
      default = "noams";
      description = "User whose tmux sessions to expose";
    };
  };

  config = mkIf cfg.enable {
    systemd.user.services.tmux-dashboard = {
      description = "tmux dashboard web server";
      wantedBy = [ "default.target" ];
      after = [ "network.target" ];

      serviceConfig = {
        ExecStart = "${pkgs.tmux-dashboard}/bin/tmux-dashboard -addr ${cfg.address}:${toString cfg.port}";
        Restart = "on-failure";
      };
    };
  };
}
```

## Project Structure

```
tmux-dashboard/
├── main.go              # Entry point, CLI flags
├── server.go            # HTTP server setup, routes
├── tmux.go              # tmux command wrappers
├── handlers.go          # HTTP handlers
├── websocket.go         # WebSocket streaming
├── templates/
│   ├── layout.html      # Base template with htmx
│   ├── index.html       # Dashboard home
│   ├── sessions.html    # Session list partial
│   ├── session.html     # Session detail view
│   ├── output.html      # Pane output partial
│   └── input.html       # Input bar partial
├── static/
│   └── app.js           # Voice input, minimal JS
├── flake.nix            # Nix flake for building
├── go.mod
├── go.sum
└── README.md
```

## Development Setup

```bash
# Enter dev shell
nix develop

# Run with hot reload
go run . -addr localhost:8080

# Build
go build -o tmux-dashboard .

# Test tmux commands
tmux list-sessions
```

## Dependencies (Go)

```go
// go.mod
module github.com/USER/tmux-dashboard

go 1.22

require (
    github.com/gorilla/websocket v1.5.1  // WebSocket support
)
```

Minimal dependencies - stdlib for HTTP, templates, and process execution.

## References

- [htmx documentation](https://htmx.org/docs/)
- [tmux man page - FORMATS section](https://man7.org/linux/man-pages/man1/tmux.1.html)
- [Web Speech API](https://developer.mozilla.org/en-US/docs/Web/API/Web_Speech_API)
- [Tailwind CSS](https://tailwindcss.com/docs)
