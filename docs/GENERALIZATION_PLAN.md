# Houston Generalization Plan

Making houston agent-agnostic to support multiple coding agents (Claude Code, Aider, Cursor, Cline, etc.)

## Current State

### Claude-Specific Code Locations

| File | Lines | Description |
|------|-------|-------------|
| `parser/parser.go` | All | Claude-specific output parsing (spinners, tools, modes) |
| `tmux/client.go` | 224-319 | `LooksLikeClaudeOutput()`, `filterStatusBar()`, mode detection |
| `server/server.go` | 131-164, 352-379 | `isClaudeWindow` checks, `detectAutoAccept()` |
| `status/panes.go` | 11 | Hardcoded `/tmp/claude-status/panes` path |
| `views/views.templ` | Various | Mode badge, auto-accept toggle, prompt patterns |

### Current Hook Integration

- **Pane Priority** (`/tmp/claude-status/panes`): Used to select which pane to show first
- **Status Watcher** (`--status-dir`): Loaded but not integrated (TODO)

---

## Phase 1: Agent Interface (Foundation)

### 1.1 Create Agent Interface

```go
// agents/agent.go
package agents

import "regexp"

type ResultType int

const (
    TypeIdle ResultType = iota
    TypeWorking
    TypeQuestion
    TypeChoice
    TypeError
)

type ParseResult struct {
    Type         ResultType
    Activity     string   // What agent is doing (for TypeWorking)
    Question     string   // Question text (for TypeQuestion)
    Choices      []string // Options (for TypeChoice)
    ErrorSnippet string   // Error text (for TypeError)
}

type Feature struct {
    ID          string                       // "autoaccept", "yolo-mode", etc.
    Label       string                       // "Auto-accept edits"
    Icon        string                       // "⏵⏵"
    Detect      func(output string) string   // Returns "on", "off", or ""
    ToggleKey   string                       // tmux key to send: "BTab"
}

type Agent interface {
    // Identity
    Name() string                              // "claude", "aider", "cursor"
    Detect(output string) bool                 // Does this output belong to this agent?

    // Output Processing
    Parse(output string) ParseResult           // Extract state from output
    FilterOutput(output string) string         // Remove status bars, noise

    // Mode (optional - return "" if agent has no modes)
    GetMode(output string) string              // "insert", "normal", ""

    // Input Detection (optional)
    GetPromptPattern() *regexp.Regexp          // Pattern for click-to-focus
    GetPendingInput(output string) string      // Text typed but not sent

    // Agent-Specific Features (optional)
    Features() []Feature
}
```

### 1.2 Create Agent Registry

```go
// agents/registry.go
package agents

var registered []Agent

func Register(a Agent) {
    registered = append(registered, a)
}

func Detect(output string) Agent {
    for _, a := range registered {
        if a.Detect(output) {
            return a
        }
    }
    return &GenericAgent{} // Fallback
}

func init() {
    Register(&claude.Agent{})
    Register(&GenericAgent{})
}
```

### 1.3 Package Structure

```
agents/
├── agent.go          # Interface definition
├── registry.go       # Detection & registration
├── generic.go        # Fallback agent (no parsing)
└── claude/
    └── claude.go     # Claude Code implementation (refactor from parser/)
```

---

## Phase 2: Refactor Existing Code

### 2.1 Extract Claude Agent

Move code from:
- `parser/parser.go` → `agents/claude/parser.go`
- `tmux/client.go` (Claude functions) → `agents/claude/detection.go`
- `server/server.go` (`detectAutoAccept`) → `agents/claude/features.go`

### 2.2 Create Generic Agent

```go
// agents/generic.go
type GenericAgent struct{}

func (g *GenericAgent) Name() string { return "generic" }
func (g *GenericAgent) Detect(output string) bool { return true } // Always matches
func (g *GenericAgent) Parse(output string) ParseResult {
    return ParseResult{Type: TypeIdle}
}
func (g *GenericAgent) FilterOutput(output string) string { return output }
func (g *GenericAgent) GetMode(output string) string { return "" }
func (g *GenericAgent) GetPromptPattern() *regexp.Regexp { return nil }
func (g *GenericAgent) GetPendingInput(output string) string { return "" }
func (g *GenericAgent) Features() []Feature { return nil }
```

### 2.3 Update Server

```go
// server/server.go

func (s *Server) buildSessionsData() views.SessionsData {
    for _, win := range windows {
        output, _ := s.tmux.CapturePane(pane, 100)

        // Detect agent type from output
        agent := agents.Detect(output)
        parseResult := agent.Parse(output)
        filteredOutput := agent.FilterOutput(output)

        // Use agent-specific logic
        windowNeedsAttention := parseResult.Type == agents.TypeError ||
            parseResult.Type == agents.TypeChoice ||
            parseResult.Type == agents.TypeQuestion

        // ...
    }
}
```

### 2.4 Update UI

- Mode badge: Only show if `agent.GetMode() != ""`
- Features: Dynamically render from `agent.Features()`
- Prompt click: Use `agent.GetPromptPattern()`

---

## Phase 3: Hooks Generalization

### 3.1 Make Hook Paths Configurable

```go
// status/config.go
type HookConfig struct {
    PanesDir  string // Default: /tmp/houston/panes (was /tmp/claude-status/panes)
    StatusDir string // Configurable via --status-dir
}
```

### 3.2 Document Hook Format

Create `docs/HOOKS.md` documenting the status file format so other agents can integrate:

```markdown
# Houston Hooks Integration

Agents can write status files to integrate with houston.

## Pane Status Files

Location: `/tmp/houston/panes/{pane_id}`

Format:
```
session=my-session
state=processing|waiting|done|idle
timestamp=1234567890
```

## Session Status Files

Location: `{status-dir}/{session-name}.json`

Format:
```json
{
  "tmux_session": "my-session",
  "status": "working|waiting|idle|permission",
  "message": "Running tests...",
  "tool": "Bash",
  "timestamp": 1234567890
}
```
```

---

## Phase 4: Community Agents

### 4.1 Agent Template

Create `agents/TEMPLATE.md`:

```markdown
# Adding a New Agent

1. Create `agents/{name}/{name}.go`
2. Implement the `Agent` interface
3. Register in `agents/registry.go`
4. Add detection markers
5. Add tests
```

### 4.2 Potential Agents

| Agent | Detection Markers | Modes | Features |
|-------|-------------------|-------|----------|
| Claude Code | `-- INSERT --`, `>>>`, `Claude:` | insert/normal | auto-accept |
| Aider | `/add`, `/run`, `aider>` | - | - |
| Cursor | TBD | TBD | TBD |
| Cline | TBD | TBD | TBD |
| Continue | TBD | TBD | TBD |

---

## Implementation Order

### MVP (v1.0)
1. [ ] Create agent interface
2. [ ] Extract Claude agent from existing code
3. [ ] Create generic fallback agent
4. [ ] Update server to use agent detection
5. [ ] Make hook paths configurable

### v1.1
6. [ ] Dynamic UI for agent features
7. [ ] Document hook format
8. [ ] Create agent template

### v1.2+
9. [ ] Community-contributed agents (Aider, Cursor, etc.)
10. [ ] Agent-specific settings/config

---

## Migration Notes

- Existing Claude Code users: No changes needed, works as before
- New users: Auto-detects agent, falls back to generic
- Hook paths: Change from `/tmp/claude-status/` to `/tmp/houston/` (with backwards compat)

---

## Open Questions

1. Should agent detection be per-session or per-window?
   - Currently: Per-window (each window could be different agent)
   - Simpler: Per-session (assume all windows in session are same agent)

2. Should we support multiple agents in same session?
   - Use case: Claude in one window, shell in another
   - Current approach: Yes, detect per-window

3. Config file for custom patterns?
   - Allow users to tweak detection without code changes
   - Could be overkill for v1.0

---

## Future Ideas

### Amp Threads Support
- Read Amp thread history/context
- Display thread metadata (handoff info, parent thread)
- Navigate between related threads

### Custom Agent Features
- User-configurable status line parsing patterns
- Custom notification patterns for right-side display
- Configurable mode toggle keybindings per agent
- Agent-specific status bar layouts (like Amp's ctx/cost/mode)
