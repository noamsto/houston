# houston Claude Code mod

A read-only Claude Code mod (hooks module, Claude Code 2.1.302) that posts
session, turn, tool, permission and background-task events to houston over a
local Unix socket. It is houston's primary source of Claude run state and
background tasks whenever it is live for a session; the hook and transcript
path stays the fallback. Design:
`docs/superpowers/specs/2026-10-18-claude-mod-primary-source.md`.

## Install

Not shipped by nix-config yet: it is meant to be loaded with `--plugin-dir`
pointing at this folder, next to hookyard, and that nix-config change is
pending. houston listens on its socket by default (`-claude-mod=false` turns
that off). To load it now, start Claude Code from the repo root with:

```fish
claude --plugin-dir contrib/claude-mod
```

## Read-only contract

Every hook is `($, e, next)`, passes its own `e` to `next` unmodified and
returns what `next` resolved to. The module's only calls on `$` are
`$.http.fetch` (the record POST, never awaited, errors ignored), `$.env.get`
(socket discovery and the tmux pane), `$.session.id` and `$.clock.every` (the
send timer). Every helper that receives `$` declares it `$: EngineInterface`. It never touches prompts, commands, tools, agents, the session
transcript, the UI, processes or files, and never reads houston's response, so
a missing or slow houston cannot affect the session.
`claude plugin validate contrib/claude-mod` lists the calls it finds, and
`claudemod/modguard_test.go` checks the source in CI.

## Delivery

A hook builds its record and queues it; its only other `$` call is
`$.clock.every`, made to (re)start the timer when none runs. The timer
(`$.clock.every(200)`) drains the queue in order, so a record posted from a
dispatch the engine abandoned (a `tool.end` after an interrupt) still goes
out. `session.end` also flushes the queue itself before calling `next`,
because the timer may not outlive an exit: the end record, and in a `-p` run
the final `turn.complete` and `stop`, go out within the hook. The queue holds
at most 256 records; past that the oldest are evicted.

- A record leaves the queue only once its session id is known: a failed
  `$.session.id()` leaves it at the head for the next tick.
- A refused period ends a `$.clock.every` interval, so a record queued when
  the timer has not ticked for 2 s starts a new one. A drain stuck on a host
  call for over 5 s is given up on: the next tick starts a fresh one, and the
  stuck one sends nothing once it resumes.
- When nothing has been queued for 75 ticks (15 s of a running timer), the
  timer posts a `heartbeat`, so houston can judge liveness by recency. The
  first may go out 15 s after the timer starts.

Each record belongs to the session generation it was built in (bumped at
`session.start` and after `session.end`), so a record queued before a `/clear`
carries the old session id even when it is sent after the session ended. A
`/clear` fires no `session.start`: records after it take the new id from
`$.session.id()`. A record whose session id is no longer known is not sent.

## Records

`POST http://houston.invalid/v1/record` over the socket (the `.invalid` host
never resolves, so a host that ignored `socketPath` could not send it
anywhere), one JSON object per event: `{v:1, sid, boot, seq, ts, ev, ...}`.
`seq` counts from 1 per `boot` (minted when the module loads) in hook order;
`ts` is milliseconds since the epoch.

| `ev` | fields |
|---|---|
| `session.start` | `cwd`, `pane`, `tmux_server` |
| `session.end` | `reason` |
| `compact` | `trigger`, `agent` |
| `compact.end` | `agent` (posted once the compaction resolves or fails) |
| `turn.start` | `turn`, `agent`, `pane`, `tmux_server` |
| `turn.complete` | `turn`, `agent`, `reason` |
| `tool.start` | `tool`, `id`, `agent`, `input`; Bash `bg`; AskUserQuestion `ask: [{question, multi, options: [{label, preview}]}]` |
| `tool.end` | `tool`, `id`, `agent`, `err`; on success Bash `task`, `user_bg`; Monitor `task`, `timeout_ms` (absent when persistent); TaskStop/KillShell `stop_task` |
| `tool.check` | `tool`, `id`, `agent`, `decision` |
| `notification` | `type`, `message`, `transcript`, `cwd`, `pane`, `tmux_server` |
| `permission` | `tool`, `agent`, `input`, `suggestions` (count), `transcript`, `cwd`, `pane`, `tmux_server` |
| `stop` | `last`, `tasks: [{id, type, status, description}]`, `transcript`, `cwd`, `pane`, `tmux_server` |
| `task.end` | `tasks: [{id} \| {tool_use}]` |
| `heartbeat` | none |

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
  notification ends nothing, and tags inside its event body are ignored,
  including an opening `<task-notification` tag.
- Size: houston answers a body over 16 KiB with 413, so the `notification`
  `message` is cut to 500 characters and `stop` lists its first 32 `tasks`. A
  body still over 15000 bytes (UTF-8) loses, in order, `ask`, `tasks`, `last`,
  `input` and `message` until it fits; `v`, `sid`, `boot`, `seq`, `ts`, `ev`
  and the state fields always stay.

## Privacy

The records hold, beyond ids, names, states and counts: the last 300
characters of the final assistant message; tool-input hints (up to 200
characters per field); the session's cwd and transcript path; the tmux pane id
and server pid; AskUserQuestion questions, option labels and whether an option
has a preview; background task descriptions (up to 200 characters); and the
Notification message (up to 500 characters). No prompt text and no tool output is sent. Records go
only to the local socket.

## Socket

`<dir>/claude-mod.sock`, with `<dir>` = `$HOUSTON_STATE_DIR`, else
`$HOUSTON_STATUS_DIR`, else `$HOME/.local/state/houston`, resolved at run time
(houston's `-status-dir` must match).

## Tests

`claude plugin test contrib/claude-mod` runs `hooks/register.test.ts`, driving
the timer with the kit's mocked clock. The stalled-timer and stuck-drain checks
read wall time, which the mocked clock does not move, so those two tests spin
for about 2 s and 5 s. The engine writes
`.claude-plugin/types/` beside the mod (it ignores itself) each time it loads
the mod; after that `tsc -p contrib/claude-mod` type-checks it through
`tsconfig.json`.
