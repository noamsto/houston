# houston pi extension

A read-only pi extension (`houston.ts`) that posts session, turn, step and tool
events to houston over the same local Unix socket the Claude mod uses. While it
is live for a session, houston folds its records into that pi run's state and
the hook path stays the fallback.

## Install

Not loaded from the user's pi config yet; that is a separate deployment change.
houston listens on its socket by default (`-claude-mod=false` turns the socket
off for pi too). To load the extension now, start pi from the repo root with:

```fish
pi -e contrib/pi-mod/houston.ts
```

## Read-only contract

The extension subscribes to observation events only and never returns a value
from a handler, so it cannot change a prompt, a tool call, a result or a
compaction. It never reads houston's response. A missing or slow houston cannot
affect the session. `agent_end`, `turn_end` and every mutating hook are not
subscribed. `claudemod/piguard_test.go` scans `houston.ts` in CI.

## Delivery

The factory opens no socket and no timer; both start with the first record.

- A handler builds its record and queues it. The queue holds at most 256
  waiting records; past that the oldest is dropped.
- One POST is in flight at a time, with a 2 s timeout.
- Any HTTP answer counts as delivered, and houston's reply is never read. A
  connection error or a timeout puts the record back at the head of the queue
  and retries after 1 s.
- When nothing is queued for 15 s the extension posts a `heartbeat`, so houston
  can judge liveness by recency.
- `session_shutdown` flushes the queue, bounded at 1 s.

## Records

`POST http://houston.invalid/v1/record` over the socket, one JSON object per
event: `{v:1, engine:"pi", sid, boot, seq, ts, ev, ...}`. The format is the
Claude mod's plus `engine`; a record without `engine` is a Claude record.
`sid` is pi's `getSessionId()`, the id hookyard's pi bridge sends, so the hook
and the extension land on one session. `boot` is minted per load (`/new`,
`/resume`, `/fork` and `/reload` reload the runtime), and `seq` counts from 1
per load. `ts` is milliseconds since the epoch.

| pi event | `ev` | fields |
|---|---|---|
| `session_start` | `session.start` | `cwd`, `transcript`, `pane`, `tmux_server` |
| `session_shutdown` (reason other than `reload`) | `session.end` | `reason` |
| `agent_start` | `turn.start` | `cwd`, `transcript`, `pane`, `tmux_server` |
| `turn_start` | `step.start` | none |
| `tool_execution_start` | `tool.start` | `tool`, `id` |
| `tool_execution_end` | `tool.end` | `tool`, `id`, `err` (only when true) |
| `agent_settled` | `turn.complete` | `reason`: `aborted` or `answer` |
| `session_compact`, `session_compact_failed` | `compact.end` | none |
| timer | `heartbeat` | none |

- `session_shutdown` with reason `reload` posts nothing: pi re-emits
  `session_start` for the same session.
- `agent_start` also fires on a retry and on a continuation, so `turn.start`
  can repeat within one run.
- `step.start` is one LLM turn inside a run. It lets houston pick up a run that
  was already going when it restarted.

## Privacy

The records hold, beyond ids, names, states and counts: the session's cwd and
transcript path, the tmux pane id and server pid, and tool names and ids. No
prompt, no message content, no tool arguments and no tool output is sent.
Records go only to the local socket.

## Socket

`<dir>/claude-mod.sock`, with `<dir>` = `$HOUSTON_STATE_DIR`, else
`$HOUSTON_STATUS_DIR`, else `$HOME/.local/state/houston` (houston's
`-status-dir` must match).

## Tests

`just mod-check` runs `claudemod/piguard_test.go` (the read-only scan of
`houston.ts`) and `node --test contrib/pi-mod/houston.test.ts`.
