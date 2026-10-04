// Mirror of parser.ResultType (serialized as strings)
export type ResultType = 'idle' | 'working' | 'done' | 'question' | 'choice' | 'error'

// Mirror of agents.AgentType
export type AgentType = 'claude-code' | 'amp' | 'generic'

// WebSocket message types
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
