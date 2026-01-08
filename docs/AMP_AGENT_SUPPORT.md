# Amp Agent Support

Adding support for Amp (Sourcegraph's AI coding agent) alongside Claude Code.

## Exploration Findings

### Data Locations

| Aspect | Claude Code | Amp |
|--------|-------------|-----|
| **Config** | `~/.claude/` | `~/.config/amp/` |
| **Data** | `~/.claude/` | `~/.local/share/amp/` |
| **State** | `~/.claude/session-env/` | `~/.local/state/amp/` |
| **Thread format** | JSONL per project in `projects/` | JSON per thread in `threads/` |
| **Thread ID** | UUID | `T-{uuid}` prefixed |
| **Current thread** | N/A (per-project) | `~/.local/state/amp/last-thread-id` |
| **History** | `~/.claude/history.jsonl` | `~/.local/share/amp/history.jsonl` |

### Thread/Session File Structure

**Amp thread file** (`~/.local/share/amp/threads/T-*.json`):
```json
{
  "id": "T-019b9d03-62e3-772f-9780-3e637f90eaec",
  "title": "Thread title",
  "agentMode": "smart",
  "created": 1767865803494,
  "env": {
    "initial": {
      "trees": [{
        "displayName": "project-name",
        "uri": "file:///path/to/project",
        "repository": { "type": "git", "url": "...", "ref": "...", "sha": "..." }
      }],
      "platform": { "os": "linux", "client": "CLI", "clientVersion": "..." }
    }
  },
  "messages": [
    {
      "role": "user|assistant",
      "messageId": 0,
      "content": [...],
      "state": { "type": "complete|cancelled|running", "stopReason": "tool_use|end_turn" },
      "usage": { "model": "...", "inputTokens": N, "outputTokens": N }
    }
  ]
}
```

**Claude Code session** (`~/.claude/projects/{path}/*.jsonl`):
- JSONL format, one entry per event
- Types: `user`, `assistant`, `file-history-snapshot`, `summary`, `system`
- Contains hook execution info, file backups, etc.

---

## Terminal Output Comparison

### Status Bar

**Claude Code:**
```
❄ impure 📂 ~/path  main ≡!+  🤖 Sonnet 4.5 | 📊 50k/200k (25.0%) | ⏱️  0.05h | 💬 43 msgs
-- INSERT --
```

**Amp:**
```
╭─37% of 168k · $1.24 (free)─────────────────────────────────smart─╮
│                                                                  │
╰─────────────────────────────────~/Data/git/tmux-dashboard (main)─╯
```

### Key Visual Differences

| Feature | Claude Code | Amp |
|---------|-------------|-----|
| **Status bar style** | Flat line with emojis | Box with corners `╭╮╰╯` |
| **Mode indicator** | `-- INSERT --` / `-- NORMAL --` | None (no vim modes) |
| **Prompt** | `❯` (after separator) | `❯` (same) |
| **Separator** | `─────────` (solid line) | Box borders |
| **Thinking indicator** | `✻` spinner | `✻ Cogitated for Xm Ys` / `✻ Baked for Xm Ys` |
| **Tool output** | `⎿` prefix | `⎿` prefix + `Running PostToolUse hooks…` |
| **Token display** | `📊 50k/200k (25.0%)` | `37% of 168k` |
| **Cost display** | None | `$X.XX (free)` |
| **Path display** | `📂 ~/path` in status | In box footer |
| **Model indicator** | `🤖 Sonnet 4.5` | None in status bar |

### Unique Detection Markers

**Claude Code only:**
- `-- INSERT --` or `-- NORMAL --`
- `🤖` (model emoji)
- `📊` (stats emoji)
- `💬` (messages emoji)
- `⏱️` (time emoji)
- `❄ impure` or `❄ pure` (nix shell indicator)

**Amp only:**
- `Cogitated for` or `Baked for` (thinking time)
- `Running PostToolUse hooks…`
- Box-style status: `╭─...─╮` / `╰─...─╯`
- `smart` mode indicator in status box
- Dollar cost display: `$X.XX (free)`
- Percentage without emoji: `37% of 168k`

---

## Detection Strategies

### 1. Tmux Metadata Detection (Primary, Cheapest)

Use `pane_current_command` from tmux — already available, no extra syscalls:

```go
// Already in tmux.PaneInfo.Command from list-panes output
func detectFromCommand(command string) AgentType {
    switch {
    case strings.Contains(command, "claude"):
        return AgentClaudeCode
    case strings.Contains(command, "amp"):
        return AgentAmp
    default:
        return AgentGeneric  // bash, zsh, node, etc.
    }
}
```

**When command is generic** (bash, zsh, node), fall back to output-based detection.

### 2. Output-based Detection (Secondary)

Check terminal output markers when tmux metadata is ambiguous.

**Important:** Always strip ANSI codes before pattern matching.

```go
func detectAgentFromOutput(output string) AgentType {
    output = ansi.Strip(output)  // Strip ANSI first!
    
    // Claude Code markers (high confidence)
    if strings.Contains(output, "-- INSERT --") || 
       strings.Contains(output, "-- NORMAL --") ||
       strings.Contains(output, "🤖") {
        return AgentClaudeCode
    }
    
    // Amp markers (high confidence)
    if strings.Contains(output, "Cogitated for") ||
       strings.Contains(output, "Baked for") ||
       strings.Contains(output, "Running PostToolUse hooks") ||
       boxStatusPattern.MatchString(output) {  // ╭─.*─╮
        return AgentAmp
    }
    
    // Shared markers (need context)
    // Both use: ❯ prompt, ● tool prefix, ⎿ output prefix
    
    return AgentGeneric
}
```

### 3. Process-based Detection (Optional, Expensive)

Only use when tmux metadata + output detection fail:

```go
// Read /proc/<pane_pid>/cmdline for the specific pane process
// Avoid pgrep -P scanning all children
func detectFromProcess(panePID int) AgentType {
    cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", panePID))
    if err != nil {
        return AgentGeneric
    }
    cmd := string(cmdline)
    switch {
    case strings.Contains(cmd, "amp-wrapper"):
        return AgentAmp
    case strings.Contains(cmd, "/bin/claude"):
        return AgentClaudeCode
    default:
        return AgentGeneric
    }
}
```

### 4. File-based State (For Rich Status, Not Detection)

**Amp:** Read thread JSON for real-time state:
```go
// Match pane cwd to thread's workspace
threadFile := findThreadByCwd(cwd)  // ~/.local/share/amp/threads/T-*.json
state := thread.Messages[len(thread.Messages)-1].State
// state.Type: "complete", "cancelled", "running"
// state.StopReason: "tool_use", "end_turn"
```

**Claude Code:** Read JSONL for session state:
```go
// Match pane cwd to project directory
projectDir := pathToProjectDir(cwd)  // ~/.claude/projects/{encoded-path}/
sessionFile := findLatestSession(projectDir)
// Parse JSONL for latest state
```

---

## Implementation Plan

### Design Decisions (from Oracle Review)

1. **Reuse existing types** — Don't create new `State` type; use `parser.Result` and `parser.Mode`
2. **Drop speculative methods** — No `HasVimModes()`, `Features()` — YAGNI
3. **Keep `parser` as shared package** — Types like `ResultType`, `Mode` stay there
4. **Make `tmux.Client` agent-agnostic** — Return raw output, no filtering
5. **Per-pane detection** — Not per-window; cache with 10-30s TTL

### Phase 1: Agent Interface (Simplified)

Create `agents/agent.go`:

```go
package agents

import "houston/parser"

type AgentType string

const (
    AgentClaudeCode AgentType = "claude-code"
    AgentAmp        AgentType = "amp"
    AgentGeneric    AgentType = "generic"
)

// AgentState wraps parser.Result with agent metadata
type AgentState struct {
    Agent  AgentType
    Result parser.Result  // Type, Question, Choices, ErrorSnippet, Activity
    Mode   parser.Mode    // insert, normal, "" (empty for Amp)
}

type Agent interface {
    // Identity
    Type() AgentType
    
    // Detection (output must be ANSI-stripped)
    DetectFromOutput(output string) bool
    
    // State extraction
    ParseOutput(output string) AgentState
    GetStateFromFiles(cwd string) (*AgentState, error)
    
    // Output processing
    FilterStatusBar(output string) string
}
```

### Phase 2: Registry with Caching

Create `agents/registry.go`:

```go
type Registry struct {
    agents []Agent
    cache  map[string]cachedDetection  // key: pane ID
}

type cachedDetection struct {
    agent     AgentType
    expiresAt time.Time
}

const detectionTTL = 15 * time.Second

func (r *Registry) Detect(paneID string, command string, output string) Agent {
    // 1. Check cache
    if cached, ok := r.cache[paneID]; ok && time.Now().Before(cached.expiresAt) {
        return r.getAgent(cached.agent)
    }
    
    // 2. Try tmux command first (cheapest)
    if agent := detectFromCommand(command); agent != AgentGeneric {
        r.cacheResult(paneID, agent)
        return r.getAgent(agent)
    }
    
    // 3. Fall back to output detection
    stripped := ansi.Strip(output)
    for _, a := range r.agents {
        if a.DetectFromOutput(stripped) {
            r.cacheResult(paneID, a.Type())
            return a
        }
    }
    
    return r.getAgent(AgentGeneric)
}
```

### Phase 3: Package Structure

```
agents/
├── agent.go          # Interface + AgentState
├── registry.go       # Detection + caching
├── claude/
│   ├── claude.go     # Agent implementation
│   ├── detect.go     # Output detection patterns
│   ├── filter.go     # StatusBar filtering (from internal/statusbar/)
│   └── state.go      # JSONL reading (from claudelog/)
├── amp/
│   ├── amp.go        # Agent implementation
│   ├── detect.go     # Output detection patterns
│   ├── filter.go     # Box-style status filtering
│   └── state.go      # Thread JSON reading
└── generic/
    └── generic.go    # No-op fallback

parser/                # KEEP as shared package
├── parser.go         # ResultType, Mode, Result types
└── message_parser.go # Reusable parsing helpers
```

### Phase 4: Refactor Existing Code

| Current Location | Action | Notes |
|------------------|--------|-------|
| `parser/` | **Keep** | Shared types (`Result`, `Mode`, `ResultType`) |
| `claudelog/` | Move to `agents/claude/state.go` | JSONL reading |
| `internal/statusbar/` | Move to `agents/claude/filter.go` | Claude-specific filtering |
| `tmux/client.go` | **Simplify** | Remove `LooksLikeClaudeOutput`, return raw output |
| `server/server.go` | **Update** | Use `agents.Registry` instead of direct Claude parsing |

### Phase 5: Amp Implementation

```go
// agents/amp/amp.go
type AmpAgent struct {
    threadsDir string  // ~/.local/share/amp/threads/
    stateDir   string  // ~/.local/state/amp/
}

func (a *AmpAgent) Type() AgentType { return AgentAmp }

// agents/amp/detect.go
var boxStatusPattern = regexp.MustCompile(`╭─.*─╮`)

func (a *AmpAgent) DetectFromOutput(output string) bool {
    return strings.Contains(output, "Cogitated for") ||
           strings.Contains(output, "Baked for") ||
           strings.Contains(output, "Running PostToolUse hooks") ||
           boxStatusPattern.MatchString(output)
}

// agents/amp/state.go
func (a *AmpAgent) GetStateFromFiles(cwd string) (*AgentState, error) {
    // 1. Normalize cwd (resolve symlinks, clean path)
    cwd = filepath.Clean(cwd)
    cwd, _ = filepath.EvalSymlinks(cwd)
    
    // 2. Find thread by matching cwd to thread.env.initial.trees[].uri
    //    - Strip "file://" prefix from URI
    //    - Match exact or "is parent of" relationship
    //    - Prefer last-thread-id if it matches, else most recent created
    
    // 3. Guard against empty messages array
    if len(thread.Messages) == 0 {
        return &AgentState{Agent: AgentAmp, Result: parser.Result{Type: parser.Idle}}, nil
    }
    
    // 4. Handle partial JSON (concurrent writes)
    //    - Use json.Decoder, tolerate EOF errors
    //    - Retry on next tick if parse fails
}

// agents/amp/filter.go
func (a *AmpAgent) FilterStatusBar(output string) string {
    // Strip box borders: ╭─...─╮, │...│, ╰─...─╯
}
```

---

## Effort Estimate

| Task | Hours |
|------|-------|
| Phase 1: Agent interface + registry | 1-2h |
| Phase 2: Registry with caching | 1h |
| Phase 3: Extract Claude into `agents/claude/` | 2h |
| Phase 4: Refactor `tmux` + `server` | 1-2h |
| Phase 5: Amp implementation | 2h |
| Testing & edge cases | 1-2h |
| **Total** | **8-11h** |

**Recommended approach:** Do Phase 1-4 first (Claude-only via interface), validate design works, then add Amp in Phase 5.

---

## Resolved Questions

1. **Cache agent detection per pane?**
   ✅ Yes — 15-30s TTL, invalidate if `pane_current_command` changes

2. **Mixed sessions?**
   ✅ Detect per-pane (not per-window) via registry

3. **Thread-to-pane matching for Amp:**
   ✅ Match cwd to `thread.env.initial.trees[].uri`:
   - Strip `file://` prefix
   - Normalize paths (symlinks, clean)
   - Match exact or "is parent of"
   - Prefer `last-thread-id` if matches, else most recent `created`
   - Log ambiguities when multiple threads match

4. **Status bar filtering:**
   ✅ Agent-specific via `FilterStatusBar()` method

---

## Implementation Gotchas

### Amp-specific

1. **URI normalization** — Strip `file://`, resolve symlinks, use `filepath.Clean`
2. **Multiple threads for same cwd** — Prefer `last-thread-id` if matches, else most recent
3. **Partial JSON reads** — Use `json.Decoder`, tolerate EOF, retry on next tick
4. **Empty messages array** — Return idle state, don't crash

### General

1. **ANSI stripping** — Always strip before pattern matching (especially box chars `╭╮╰╯`)
2. **Import cycles** — Keep `tmux` agent-agnostic; only `server` imports `agents`
3. **Performance** — Only compute file-based state every 3-5s, use cached detection
4. **Stale classification** — Re-run detection periodically; allow downgrade to `AgentGeneric`

---

## References

- [GENERALIZATION_PLAN.md](./GENERALIZATION_PLAN.md) - Original multi-agent architecture plan
- Amp CLI: `~/.local/share/amp/`, `~/.local/state/amp/`
- Claude Code: `~/.claude/projects/`, `~/.claude/session-env/`
