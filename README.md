# 🚀 houston

> Mission control for Claude Code agents in tmux

**houston** is a mobile-friendly web dashboard for monitoring and controlling tmux sessions remotely. Built specifically for keeping an eye on AI coding agents and sending them instructions from your phone.

![Go](https://img.shields.io/badge/Go-1.23-00ADD8?logo=go)
![License](https://img.shields.io/badge/license-MIT-blue)

## Why houston?

When you're running Claude Code agents in tmux sessions, you want to check on them without SSH'ing from your phone. houston gives you:

- 📱 **Mobile-first UI** - Touch-friendly interface optimized for phones
- 👁️ **Live monitoring** - Stream terminal output in real-time via SSE
- 🎯 **Smart status detection** - Recognizes Claude modes and activity states
- 📸 **Image support** - Send screenshots directly to Claude Code
- ⚡ **Lightweight** - Single Go binary, minimal dependencies
- 🔒 **Secure** - Localhost-only, designed for Tailscale/SSH tunnel access

## Features

### Current (v0.1+)

- ✅ List all tmux sessions with smart status indicators
- ✅ View windows and panes for any session
- ✅ Live-stream terminal output from selected panes (SSE)
- ✅ Send text input to any pane
- ✅ Send images (screenshots) to Claude Code
- ✅ Session status tracking (idle, active, mode-based)
- ✅ Claude mode detection (plan mode, accept edits, etc.)
- ✅ Mobile-responsive layout with dark mode
- ✅ Activity-based window sorting

### Roadmap

- [ ] Voice-to-text input (Web Speech API integration)
- [ ] Quick actions (Ctrl+C, clear, scroll)
- [ ] Search/filter sessions
- [ ] Output highlighting (errors, warnings)
- [ ] Keyboard shortcuts for power users
- [ ] Session creation/management from UI
- [ ] Notification on activity (service worker)
- [ ] Multi-agent support (Aider, Cursor, Cline, etc.) - see `docs/GENERALIZATION_PLAN.md`

## Quick Start

### Using Nix (Recommended)

```bash
# Clone the repository
git clone https://github.com/noamsto/houston
cd houston

# Enter development shell
nix develop

# Run the server (auto-finds available port 9090-9095)
just dev
```

### Using Go

```bash
# Clone and build
git clone https://github.com/noamsto/houston
cd houston
go build -o houston .

# Run
./houston -addr localhost:9090
```

### Configuration

```bash
# Available flags
./houston \
  -addr 127.0.0.1:9090 \                      # Listen address (localhost only)
  -status-dir ~/.local/state/houston \        # Status files directory
  -debug                                       # Enable debug logging
```

## Usage

### Access Securely

houston binds to localhost by default for security. Access it via:

**Option 1: Tailscale (Recommended)**
```bash
# Access from any device on your Tailnet
http://your-machine:9090
```

**Option 2: SSH Tunnel**
```bash
# From your phone/remote machine
ssh -L 9090:localhost:9090 your-server

# Then visit http://localhost:9090
```

### Monitoring Sessions

1. **Dashboard** - See all tmux sessions organized by status:
   - 🔴 **Needs Attention** - Claude asking questions, showing errors, or awaiting choices
   - 🟢 **Active** - Claude working or servers running
   - ⚪ **Idle** - Shells, editors, or inactive sessions

2. **Session Cards** - Show session name, branch, activity status, and preview

3. **Live Output** - Tap a window to stream its terminal output in real-time

4. **Send Commands** - Type in the input bar and hit Enter to send to Claude

5. **Send Images** - Paste or upload screenshots to send to Claude Code

### Status Detection

houston intelligently detects what's happening in your tmux sessions:

- **Claude Modes** - Recognizes plan mode, accept edits mode, etc.
- **Activity States** - Working, waiting for input, error, question, choice
- **Process Types** - Distinguishes shells, servers, editors, and Claude agents
- **Git Branches** - Shows current branch for each window
- **Priority Sorting** - Windows needing attention appear first

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                   Mobile Browser                     │
│  ┌──────────────────────────────────────────────────┐
│  │  Session List  │  Terminal View  │  Input Bar   │
│  │  (cards)       │  (live output)  │  (+ image)   │
│  └──────────────────────────────────────────────────┘
└─────────────────────────────────────────────────────┘
                          │
                SSE streams + templ components
                          │
                          ▼
┌─────────────────────────────────────────────────────┐
│                  Go HTTP Server                      │
│  • Session listing with smart filtering             │
│  • SSE output streaming                             │
│  • Command input handling                           │
│  • Image upload and forwarding                      │
│  • ANSI parser for status detection                 │
└─────────────────────────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────┐
│                   tmux CLI                           │
│  • List sessions/windows/panes                      │
│  • Capture output with history                      │
│  • Send keys and special commands                   │
└─────────────────────────────────────────────────────┘
```

### Tech Stack

| Component | Technology | Why |
|-----------|-----------|-----|
| Backend | Go | Single binary, excellent process handling |
| Templates | templ | Type-safe HTML components |
| Frontend | Vanilla JS | Minimal, fast, mobile-friendly |
| Styling | Tailwind CSS | Utility-first, no build step |
| Live updates | SSE (Server-Sent Events) | Simple, unidirectional real-time streaming |
| Parsing | Custom ANSI parser | Detects Claude states and terminal modes |

### Why SSE instead of WebSocket?

houston uses **Server-Sent Events (SSE)** rather than WebSocket because:

- **Simpler protocol** - SSE is just HTTP, easier to debug
- **One-way streaming** - We only need server → client (terminal output)
- **Auto-reconnection** - Built into browser EventSource API
- **Input via POST** - User input sent through regular HTTP requests

SSE is perfect for streaming terminal output while keeping the architecture simple.

## Project Structure

```
houston/
├── main.go              # Entry point, CLI flags
├── server/
│   └── server.go        # HTTP server, routes, handlers
├── tmux/
│   └── client.go        # tmux command wrappers
├── status/
│   ├── watcher.go       # Hook file monitoring
│   └── panes.go         # Pane status tracking
├── parser/
│   ├── parser.go        # ANSI/control sequence parsing
│   └── message_parser.go # Status message extraction
├── views/
│   └── *.templ          # HTML templates (templ)
└── static/
    ├── app.js           # Client-side JS
    └── favicon.svg      # Icon
```

## Development

### Prerequisites

- Go 1.23+
- tmux
- (Optional) Nix for reproducible builds

### Development Workflow

```bash
# Enter dev shell (Nix)
nix develop

# Run with auto-restart on file changes
just dev

# Run tests
go test ./...

# Lint
golangci-lint run

# Format templates
templ fmt ./views

# Generate template Go code
templ generate
```

### Testing

```bash
# Run all tests
go test ./...

# Test specific package
go test ./parser -v

# Run with race detector
go test -race ./...

# Test ANSI parser
go test ./parser -run TestParse
```

### Building

```bash
# Standard build
go build -o houston .

# Nix build
nix build

# Result in ./result/bin/houston
```

## NixOS Integration

houston includes a NixOS module for easy deployment:

```nix
# In your configuration.nix
services.houston = {
  enable = true;
  port = 9090;
  address = "127.0.0.1";
  user = "yourusername";
};
```

This sets up a systemd user service that starts automatically.

## Security Considerations

houston **does not include built-in authentication** by design. It relies on network-level security:

1. **Binds to localhost only** - Not accessible from external networks
2. **Tailscale recommended** - Secure private network access
3. **SSH tunnel fallback** - Port forwarding for secure remote access

### Future Authentication

Planned authentication options:
- Basic auth with password
- Tailscale identity integration
- mTLS with client certificates

## tmux Integration

### Hook Files

houston can integrate with Claude Code hooks to detect session states. Create hook scripts that write status files to `~/.local/state/houston/`:

```bash
# Example: ~/.config/claude-code/hooks/session-start.sh
echo "started" > ~/.local/state/houston/session-$CLAUDE_SESSION_ID
```

houston watches these files and updates session status accordingly.

### Control Mode

houston includes special support for Claude Code's control mode architecture, detecting when Claude is working in normal vs. control mode.

## Contributing

Contributions welcome! Please:

1. Open an issue to discuss major changes
2. Follow existing code style (use `gofmt`)
3. Add tests for new functionality
4. Update documentation
5. Run `golangci-lint run` before submitting

## License

MIT License - See [LICENSE](LICENSE) for details

## Credits

Built with:
- [templ](https://templ.guide) - Type-safe Go templating
- [Tailwind CSS](https://tailwindcss.com) - Utility-first CSS

Inspired by the need to check on AI agents from a phone without fighting with terminal SSH clients.

---

**houston** - Because remote mission control shouldn't require a laptop.
