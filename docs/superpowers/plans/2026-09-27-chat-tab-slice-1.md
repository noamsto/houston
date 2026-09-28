# Chat tab, slice 1 — standalone ACP chat reader + claude-code, and the Chat tab

Scope: new `chat/` package, `hub/` (reader wiring + per-session update ring),
`runs/` (internal session ref on `Run`), `server/` (two chat routes),
`ui/src/fleet/` + `ui/src/api/` + `ui/src/hooks/` (Chat tab), `CLAUDE.md`.

Binding authority: `docs/superpowers/specs/2026-09-27-mobile-chat-and-dispatcher-home.md`
(slice 1, "Per-engine mapping (measured)"), which defers to
`docs/superpowers/specs/2026-09-07-houston-overhaul-design.md`.

## Problem

A run's only readable history on a phone is the Activity tab (an 8-chip trail and
a 40-line preview) or the terminal. The spec's slice 1 replaces that with a chat
view. This plan builds the standalone ACP reader package and the first reader
(claude-code), shaped so pi and codex (plan 1b) and the hook baseline (plan 1c)
drop in without touching the core, the API or the UI.

## Current state (verified against code, 2026-09-27, `main` @ f1f8869)

- `hub.SessionView` already carries `SessionID`, `TranscriptPath` and `Agent`
  for every engine (`hub/hub.go:28-49`); the envelope path fills
  `TranscriptPath` from `transcript_path` / `session_file`
  (`hook/envelope.go:80`).
- `hub.refreshTranscript` (`hub/hub.go:433`) tails the file from
  `sess.transcriptOffset` with `ReadTranscriptFrom` and folds events into
  trail/preview via `applyTranscriptEvent`. `parseLine` is Claude-only;
  `jsonlRecord` decodes none of `origin`, `isMeta`, `isSidechain`,
  `message.id`.
- The hooks layer builds runs from `SessionView`s
  (`runs/hooksource.go`, `runFromSessionView`); the run key is the pane id or
  `claude/<sid>` (`sessionKey`, `hooksource.go:343`), id via `idFor`
  (`runs/registry.go:199`). `Run` has no session field.
- SSE precedent: `server/runs_api.go` (`event: snapshot` then updates, explicit
  `Flusher`). Client precedent: `ui/src/hooks/useRuns.ts` (`new EventSource`).
- `RunDetail.tsx` has `Tab = 'activity' | 'terminal'`, routes
  `#/fleet/<id>/<tab>` (`ui/src/fleet/routes.ts`). `ActivityTab` holds the
  stick-to-bottom and "show earlier" anchoring logic worth reusing.
- `POST /api/runs/{id}/input` (text / key / image) and
  `POST /api/runs/{id}/reply` exist; nothing new is needed for writes.

## What to build

### 1. `chat/` package — standalone, ACP-shaped

The package is written to be lifted out into its own tool later
(`noamsto/sessionlog` or similar), so it has **no imports outside the standard
library** — enforced by `chat/boundary_test.go`, which runs `go list -deps`
on the package and fails on any `github.com/noamsto/houston/...` entry. It knows
nothing about runs, the hub, tmux or hookyard.

Types are the spec's "Data model" `Update` (ACP `session/update` +
`id`/`seq`/`ts`), `Content`, `Location`, and:

```go
type Reader interface {
    Engine() string
    Read(path string, from Cursor) (updates []Update, next Cursor, reset bool, err error)
    Tool(path, toolCallID string) (*Update, error) // rawInput + output/diff
}

type Cursor struct {
    Offset  int64           // bytes consumed
    Pending json.RawMessage // reader-private carry-over (open message.id, in-flight calls)
}

func For(engine string) Reader // nil = no reader (hook baseline, plan 1c)
```

- `Seq` is left zero by readers; the consumer's ring assigns it.
- Tool hints: `chat/` cannot import `hook.ToolHint`, so it gets its own
  `title.go` with the same key order (`file_path, path, command, pattern, url,
  description, prompt`) and a test that pins it to `hook.ToolHintKeys` from the
  houston side (`hub/chat_title_test.go`), so the two can't drift while they
  share a repo.
- `Kind` mapping for Claude tools: Read→read, Edit/Write/MultiEdit/NotebookEdit→edit,
  Grep/Glob→search, Bash→execute, WebFetch/WebSearch→fetch, Task/Agent→other
  (+`_meta.subagent`), anything else→other.
- A tiny `cmd/sessionlog` main (build-tagged `tools`, not shipped) prints a file
  as ACP JSONL — the seed of the future standalone tool and the easiest way to
  eyeball a real transcript's output locally.

### 2. claude-code reader (`chat/claude.go`)

Implements the spec's measured mapping, in ACP terms:

| Record | Update |
|---|---|
| `type:user`, `origin.kind=="human"` | `user_message_chunk` |
| `type:user`, `origin.kind=="task-notification"` | `user_message_chunk` + `_meta.origin="task-notification"` (UI: divider) |
| `type:user`, `isMeta` | dropped |
| `type:user` with `tool_result` blocks | `tool_call_update` → `completed` / `failed` (`is_error`), matched by `tool_use_id` |
| `type:assistant` `text` blocks | `agent_message_chunk`, text joined across records sharing `message.id`; `_meta.phase="commentary"` when the same message also holds a `tool_use` |
| `type:assistant` `tool_use` | `tool_call` (`in_progress`, `title`, `kind`, `locations` from `file_path`/`path`) |
| `type:assistant` `thinking` | dropped |
| `attachment` `queued_command` with `commandMode:"prompt"` | `user_message_chunk` |
| everything else (see spec list) | dropped |

- A Task/Agent `tool_call` gets `_meta.subagent = <agent id>` when
  `<sid>/subagents/agent-*.jsonl` exists; nothing inline.
- Unknown `origin.kind` values: dropped **and** logged once per value (the
  reader takes an optional `func(string)` logger, still stdlib-only).
- Records without `origin` (older Claude Code): fall back to "role user,
  string content, not `isMeta`, not starting with `<`" — cover with a fixture.
- `Pending` carries the open `message.id` and in-flight call ids so a read that
  stops mid-message resumes correctly.

**Fixtures:** hand-written minimal JSONL in `chat/testdata/claude/`, one per
mapping row (split `message.id`, parallel tool calls, task-notification,
isMeta, queued prompt, subagent Task, legacy no-origin). Golden files are the
expected ACP JSONL. **Never copy real transcripts into the repo** —
`tmp/samples/` stays gitignored and is only for verification.

### 3. Hub wiring

- `hub.Session` gains a ring of `chat.Update`s (cap 500; the ring lives in
  `hub/`, not `chat/` — sequencing is the consumer's job) and a `chat.Cursor`.
- `refreshTranscript` calls `chat.For(sess.view.Agent)`; when non-nil, reads
  from `chatCursor` and appends to the ring. Existing trail/preview code is
  untouched — Fleet cards must not change.
- The ring publishes to per-session subscribers (same pattern as `Subscribe`, but
  keyed by session id and only created while a chat stream is open).
- `Hub.Chat(sessionID, before Seq, limit)` for pages; beyond the ring it
  re-reads the file from 0 via the reader (no caching; scroll-back only).
- `Agent` normalization: `SessionView.Agent` is `"claude"` for the native path
  (`hook.AgentClaude`) — register the claude reader under that value, and
  under `"claude-code"`; add a test that both resolve.

### 4. Run → session

- `runs.Run` gains `Session string \`json:"-"\`` set by the hooks layer from
  `v.SessionID` in `runFromSessionView`. Never serialized; add a test that the
  runs JSON has no session field.
- Composition: first non-empty wins (hooks is the only layer that sets it).

### 5. Routes (`server/runs_chat.go`)

- `GET /api/runs/{id}/chat?before=<seq>&limit=<n≤100>` → `{updates, more}` (houston-wrapped `chat.Update`s).
- `GET /api/runs/{id}/chat/stream?after=<seq>` → SSE: `event: updates`
  (batch), `event: reset` (session id behind the run changed — e.g. a new
  session in the same pane), keep-alive comments every 25 s.
- `GET /api/runs/{id}/chat/tool/{callId}` → `{name, input, output (≤16 KB), error}`.
- Ladder: 503 registry not started · 404 unknown run · 404 `no chat` when the
  run has no `Session` or its engine has no reader (UI hides the tab).
- Behind the existing auth middleware (it is under `/api/`); add a test that
  an unauthenticated request is refused like `/api/runs`.

### 6. UI

- `ui/src/api/chat.ts`: the ACP `Update` type mirroring §1 (+ houston's
  `permission`/`question`/`event` wrappers for later slices), `fetchChatPage`,
  `fetchTool`.
- `ui/src/hooks/useRunChat.ts`: page + `EventSource`, dedupe by `id`,
  reconnect with `after=<last seq>`, `reset` clears. Unmount closes the stream
  (test with the DOM test utility from the 2026-09-08 lifecycle plan).
- `RunDetail`: `Tab = 'chat' | 'activity' | 'terminal'`. Chat is offered iff a
  first page returns 200; it becomes the default tab when offered. Activity
  stays for runs without chat (removed in a later plan once 1b/1c land).
- `ChatTab.tsx` renders per the canvas screen 2: `user` bubble right,
  `assistant` plain text (markdown subset: paragraphs, inline code, fenced
  code, lists — no raw HTML), `_meta.phase=="commentary"` dimmer. Consecutive
  `tool_call`s collapse client-side into one row ("Read ×2 · Edit · Bash") with
  `collapseTrail`'s rules, expanding to per-call rows; an `edit` row expands to
  its diff via `fetchTool`. `_meta.origin=="task-notification"` = centred
  divider.
- **Feels live** (spec §"Feels live", levels 1–2):
  - Provisional tool row from `run.activity.tool`/`hint` while the run is
    working, replaced when the transcript's `tool_call` for the same tool
    arrives; a "working…" line while `thinking`/`running`.
  - Optimistic user bubble on send, reconciled with the transcript's
    `user_message_chunk` (match on text, 30 s window; unmatched → shown with a
    "not confirmed" mark, never silently dropped).
  - Typewriter reveal for `agent_message_chunk`s that arrive over the live
    stream only (never page load, scroll-back or reconnect catch-up); finishes
    within ~1 s; respects `prefers-reduced-motion`.
- Composer: text → `input {type:"text"}`, `esc` chip → `input {type:"key",
  key:"Escape"}`, image → existing image path. Disabled with a reason when
  `caps.terminal` is false.
- Reuse `ActivityTab`'s stick-to-bottom and anchored "show earlier" logic —
  extract it to `useStickyScroll` first, in its own commit, with Activity
  still passing its tests.

### 7. Docs

`CLAUDE.md`: a "Chat" section — the ACP update model, the reader registry and package boundary, the
routes and ladder, and the measured claude-code mapping (short; point to the
spec for the rest).

## Non-goals

pi / codex readers (plan 1b), hook baseline (plan 1c), pane ghost-streaming
(spec "Feels live" level 3), extracting `chat/` into its own repo, question / permission
cards (slice 2), crew stream and dispatcher home (slice 3), terminal changes,
removing Activity, desktop console changes beyond the tab appearing.

## Verification (binding — a green build proves nothing here)

1. `go test ./chat/... ./hub/... ./runs/... ./server/...` and `-race` on
   `chat/` and `hub/`.
2. **Real-file check, not committed:** a throwaway test (build tag
   `samples`, reads `tmp/samples/claude-code/*.jsonl` if present, skips
   otherwise) asserts for every sample: `user_message_chunk`s without
   `_meta.origin` == records with `origin.kind=="human"` + queued prompts; no
   message text starts with `<`; every `tool_use` id yields exactly one
   `tool_call` and at most one terminal `tool_call_update`; nothing emitted for
   any `isSidechain` record. Paste the counts in the PR, not the content.
3. Live: start a Claude session in a houston worktree pane, open its run on a
   phone over Tailscale against the production build (`just build`), send a
   prompt from the composer, watch assistant text and the tools row arrive
   without reload; kill Wi-Fi for 10 s mid-turn and confirm no duplicates or
   gaps after reconnect.
4. Screenshot the Chat tab on the phone in the PR.

## Acceptance criteria

- Chat tab appears for every live claude-code run and for no other engine yet.
- Updates match the measured mapping on all four samples (verification 2).
- `chat/` imports nothing outside the standard library (boundary test).
- Reconnect after a dropped stream shows no duplicate and no missing item.
- Fleet cards, Activity and Terminal behave exactly as before.
- No transcript content, real or excerpted, is committed.

## Steps

1. `chat/`: types, `boundary_test.go`, `title.go` + kind mapping.
2. `chat/claude.go` + fixtures and goldens, one per mapping row, test-first.
3. `cmd/sessionlog` (tools tag) + `samples`-tagged real-file test; run both on
   `tmp/samples/`; record counts.
4. `runs.Run.Session` + hooks layer + JSON-omission test.
5. Hub ring, reader wiring, `Hub.Chat`, per-session subscribe,
   `hub/chat_title_test.go`; tests with a temp transcript appended in steps.
6. `server/runs_chat.go` + ladder, auth and SSE resume tests.
7. Extract `useStickyScroll` from `ActivityTab` (own commit).
8. `api/chat.ts`, `useRunChat` + lifecycle test, `ChatTab`, tab wiring.
9. Feels-live: provisional rows, optimistic send, typewriter.
10. `CLAUDE.md`.
11. Live verification (3–4), PR.

## Follow-on plans

- **1b — pi + codex readers.** Pure `chat/` work against the measured
  mappings; codex `phase: commentary` → `_meta.phase`; codex subagent rollouts
  via `session_meta.source.subagent`.
- **1c — hook baseline.** Needs the hook to keep a small per-session event log
  (prompt text from `UserPromptSubmit.prompt`, tool pre/post, turn-end
  message): today the state file only holds the latest state, so a baseline
  can't be derived after the fact.
- **cursor reader** once a non-factify cursor session is sampled.
- **Pane ghost-streaming** (spec "Feels live" level 3), Claude first.
- **Extract `chat/` into its own tool** when a second consumer appears (aeye's
  `session-backfill.sh` is the likely first): move the package and
  `cmd/sessionlog` to their own module; houston imports it. The boundary test
  is what makes this a move, not a rewrite.
