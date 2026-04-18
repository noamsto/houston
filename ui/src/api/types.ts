// Mirror of parser.ResultType (serialized as strings)
export type ResultType = 'idle' | 'working' | 'done' | 'question' | 'choice' | 'error'

// Mirror of parser.Mode (serialized as strings)
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
  created: string        // ISO 8601
  windows: number
  attached: boolean
  last_activity: string  // ISO 8601
}

// Mirror of tmux.Window
export interface Window {
  index: number
  name: string
  active: boolean
  panes: number
  last_activity: string  // ISO 8601
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
  status: ResultType
  choices?: string[]
  suggestion?: string
  input_text?: string
  status_line?: string
  activity?: string
  window_name?: string
}

export interface WSInput {
  data: string
}

export interface WSResize {
  cols: number
  rows: number
}

// ────────────────────────────────────────────────────────────────
// Agent cards (new) — mirror of hub.SessionView
// ────────────────────────────────────────────────────────────────

export type AgentState =
  | 'starting'
  | 'thinking'
  | 'tool-running'
  | 'waiting'
  | 'waiting:permission'
  | 'compacting'
  | 'ended'

// Mirror of hub.TrailChip
export interface TrailChip {
  tool: string
  hint: string
  done: boolean
  error?: boolean
}

// Mirror of hub.SessionView
export interface SessionView {
  session_id: string
  cwd?: string
  tmux_session?: string
  tmux_window?: string
  state: AgentState
  tool?: string
  tool_input_hint?: string
  last_message?: string
  turn: number
  since?: number
  updated_at: number
  trail?: TrailChip[]
  preview?: string
  input_tokens: number
  output_tokens: number
  transcript_path?: string
}
