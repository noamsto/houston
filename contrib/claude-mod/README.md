# houston Claude Code mod

A read-only Claude Code mod (hooks module, Claude Code 2.1.302) that posts
session, turn, tool, permission and background-task events to houston over a
local Unix socket. It is houston's primary source of Claude run state and
background tasks whenever it is live for a session; the hook and transcript
path stays the fallback. Design:
`docs/superpowers/specs/2026-10-18-claude-mod-primary-source.md`.

## Install

nix-config ships it: Claude Code is started with `--plugin-dir` pointing at
this folder, next to hookyard. houston listens on its socket by default
(`-claude-mod=false` turns that off).

To try a working copy in one session:

```fish
houston
claude --plugin-dir contrib/claude-mod
```

## Read-only contract

Every hook is `($, e, next)`, passes its own `e` to `next` unmodified and
returns what `next` resolved to. The module's only calls on `$` are
`$.http.fetch` (the record POST, never awaited, errors ignored), `$.env.get`
(socket discovery and the tmux pane), `$.session.id` and `$.clock.every` (the
send timer). It never touches prompts, commands, tools, agents, the session
transcript, the UI, processes or files, and never reads houston's response, so
a missing or slow houston cannot affect the session.
`claude plugin validate contrib/claude-mod` lists the calls it finds, and
`claudemod/modguard_test.go` checks the source in CI.

## Delivery

A hook only builds its record and queues it; it makes no other `$` call. A
timer (`$.clock.every(200)`, started by `session.start`, or by the first record
when none runs yet) drains the queue in order. So a record posted from a
dispatch the engine abandoned (a `tool.end` after an interrupt) still goes
out. The queue holds at most 256 records; past that the oldest are evicted.

Each record belongs to the session generation it was built in (bumped at
`session.start` and after `session.end`), so a record queued before a `/clear`
carries the old session id even when it is sent after the next session began.
A record whose session id is no longer known is not sent.

## Records

`POST http://houston/v1/record` over the socket, one JSON object per event:
`{v:1, sid, boot, seq, ts, ev, ...}`. `seq` counts from 1 per `boot` (minted
when the module loads) in hook order; `ts` is milliseconds since the epoch.

| `ev` | fields |
|---|---|
| `session.start` | `cwd`, `pane`, `tmux_server` |
| `session.end` | `reason` |
| `compact` | `trigger`, `agent` |
| `turn.start` | `turn`, `agent`, `pane`, `tmux_server` |
| `turn.complete` | `turn`, `agent`, `reason` |
| `tool.start` | `tool`, `id`, `agent`, `input`; Bash `bg`; AskUserQuestion `ask: [{question, multi, options: [{label, preview}]}]` |
| `tool.end` | `tool`, `id`, `agent`, `err`; on success Bash `task`, `user_bg`; Monitor `task`, `timeout_ms` (absent when persistent); TaskStop/KillShell `stop_task` |
| `tool.check` | `tool`, `id`, `agent`, `decision` |
| `notification` | `type`, `message`, `transcript`, `cwd`, `pane`, `tmux_server` |
| `permission` | `tool`, `agent`, `input`, `suggestions` (count), `transcript`, `cwd`, `pane`, `tmux_server` |
| `stop` | `last`, `tasks: [{id, type, status, description}]`, `transcript`, `cwd`, `pane`, `tmux_server` |
| `task.end` | `tasks: [{id} \| {tool_use}]` |

- `input` is the subset of the tool's input houston turns into an activity
  hint: `description`, `command`, `file_path`, `path`, `pattern`, `url`,
  `prompt` (strings, each cut to 200 characters) and, for AskUserQuestion, the
  first question's `header` and `question`.
- `pane` is `$TMUX_PANE` and `tmux_server` the pid field of `$TMUX`, both read
  once per module load and sent only when set.
- `last` is the last 300 characters of the final assistant message, for
  houston's question-at-turn-end rule.
- `task.end` lists only task notifications whose header carries a terminal
  `<status>` (completed, failed, killed, stopped); a monitor's per-event
  notification ends nothing, and tags inside its event body are ignored.

## Privacy

The records hold, beyond ids, names, states and counts: the last 300
characters of the final assistant message; tool-input hints (up to 200
characters per field); the session's cwd and transcript path; the tmux pane id
and server pid; AskUserQuestion questions, option labels and whether an option
has a preview; background task descriptions (up to 200 characters); and the
Notification message. No prompt text and no tool output is sent. Records go
only to the local socket.

## Socket

`<dir>/claude-mod.sock`, with `<dir>` = `$HOUSTON_STATE_DIR`, else
`$HOUSTON_STATUS_DIR`, else `$HOME/.local/state/houston`, resolved at run time
(houston's `-status-dir` must match).

## Tests

`claude plugin test contrib/claude-mod` runs `hooks/register.test.ts`, driving
the timer with the kit's mocked clock. The engine writes
`.claude-plugin/types/` beside the mod (it ignores itself) each time it loads
the mod; after that `tsc -p contrib/claude-mod` type-checks it through
`tsconfig.json`.
