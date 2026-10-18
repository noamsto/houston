# houston Claude Code mod (spike)

A read-only Claude Code mod (hooks module, Claude Code 2.1.302) that posts
session, turn, tool, permission and background-task events to houston over a
local Unix socket. houston ingests them with `-claude-mod`; nothing reads them
into Fleet yet.

**Not for global install.** This is a spike: see
`docs/superpowers/specs/2026-10-18-claude-mod-spike-report.md`.

## Read-only contract

Every hook passes the event it received to `next` unmodified and returns what
`next` resolved to. The module's only calls on `$` are `$.http.fetch` (the
record POST, never awaited, errors ignored), `$.env.get` (socket discovery) and
`$.session.id`. It never touches prompts, commands, tools, agents, the session
transcript, the UI, processes or files, and never reads houston's response, so
a missing or slow houston cannot affect the session.
`claude plugin validate contrib/claude-mod` lists the calls it finds.

## Records

`POST http://houston/v1/record` over the socket, one JSON object per event:
`{v:1, sid, boot, seq, ts, ev, ...}`. `seq` counts from 1 per `boot` (minted
when the module registers) in hook order. Events: `session.start`,
`session.end`, `compact`, `turn.start`, `turn.complete`, `tool.start`,
`tool.end`, `tool.check`, `notification`, `permission`, `stop`, `task.end`.
No prompt, assistant or tool text is sent, except AskUserQuestion questions and
labels and the Notification message.

## Socket

`<dir>/claude-mod.sock`, with `<dir>` = `$HOUSTON_STATE_DIR`, else
`$HOUSTON_STATUS_DIR`, else `$HOME/.local/state/houston`, resolved at run time
(houston's `-status-dir` must match).

## Load it for one session

```fish
houston -claude-mod
claude --plugin-dir contrib/claude-mod
```

`claude plugin test contrib/claude-mod` runs `hooks/register.test.ts`. The
engine writes `.claude-plugin/types/` beside the mod; do not commit it.
