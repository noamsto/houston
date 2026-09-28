# Spec — Mobile chat view and dispatcher-first home

**Status:** draft — not yet through spec-critic.
**Binding design authority:** `docs/superpowers/specs/2026-09-07-houston-overhaul-design.md`.
**Visual reference:** the "houston mobile — dispatcher chat" design canvas
(screens 1–4 and the plumbing board). The canvas is a mockup; where it and this
spec disagree, this spec wins.

Scope is slices 1–3 of the mobile plan. Terminal polish (slice 4) and
dispatcher-authored cards (slice 5) are listed under "Later" and get their own
specs.

## Problem

On a phone, houston is a list of runs plus a terminal. The terminal is the only
place to read what an agent actually said, and a 120-column TUI at phone width is
the worst way to read it. Answering a permission prompt or a worker question
means opening the terminal, finding the prompt, and typing a digit.

Claude Code mobile shows the same session as a conversation: messages, collapsed
tool calls, and prompts as buttons. houston already has every input that view
needs — it just never assembles them into one:

| Input | Where it already lives | What it carries |
|---|---|---|
| Engine transcripts | Claude JSONL parsed in `hub/transcript.go`; for pi, codex and cursor hookyard already hands houston the file (`transcript_path` / `session_file`, `hook/envelope.go`) but nothing parses it; OpenCode serves messages over HTTP (`opencode.Client.GetMessages`, `SubscribeEvents`) | user text, assistant text, tool calls and results, tokens |
| Hook state (hookyard envelopes) | `hook/`, `runs/hooksource.go` | `waiting:permission`, turn end, tool in flight, `last_message` |
| Crew bus | `runs/crewsource.go` (`crewRecord`) | `dispatch`, `status` (state, detail, `pr_url`), `msg` records |
| Pane parser | `server/pane_ws.go` meta frames | detected `choices` for the prompt on screen |

Today the hub reduces the transcript to an 8-chip trail and a 40-line preview
(`applyTranscriptEvent`) and throws the rest away.

## Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Chat source | **Derive, don't emit** — build chat from transcript + hooks + crew bus | Works for every Claude run, solo or crew, with no change to agents or dispatcher |
| Dispatcher-authored messages | **Later, optional** (slice 5) | Only worth it once we know which summaries derived chat can't produce |
| Engines | **Engine-neutral from day one.** One `chat.Source` interface, one adapter per harness (claude-code, pi, codex, cursor, opencode), plus a hook-only baseline any engine hookyard routes gets for free | Crews already mix engines (`dispatch --engine`); a Claude-only chat would make the dispatcher screen lie about half its workers |
| Wire vocabulary | **ACP `session/update` shapes** (`user_message_chunk`, `agent_message_chunk`, `tool_call`, `tool_call_update`, `plan`; `session/request_permission` for permission cards) | A published, engine-neutral vocabulary for exactly this; lets the reader become its own tool and talk to ACP clients without a format change |
| Where the readers live | **houston `chat/` package, standalone** — no imports from the rest of houston, enforced by a test. **Not hookyard**: hookyard routes hook events per-exec on the tool-call path; reading session history is a different job with a long-running lifecycle. Extracted into its **own tool/module** when a second consumer appears (aeye's `session-backfill.sh` already reads transcripts per engine) | Single responsibility; no release coupling to the router; ACP types stay out of a hook router |
| hookyard's part | Unchanged in kind: passes `transcript_path` / `session_file` and the live events (prompt text, `pre_tool`, permission, turn end). No transcript content in the envelope | Keeps the tool-call path cheap; hooks are the fast signal, the transcript is the content |
| Unit | A `ChatItem` stream **per run**; a crew stream is a merge | Keeps the run the spine (overhaul spec) |
| Transport | Snapshot page + SSE tail, cursor = `(offset, seq)` | Mirrors `/api/runs` + `/api/runs/stream`; reconnect resumes, no reload |
| Writes | **No new write routes** | Composer → `POST /api/runs/:id/input`; worker question → `POST /api/runs/:id/reply`; dispatch → `POST /api/dispatch` |
| Terminal | Stays, as a tab beside Chat | Escape hatch for anything chat can't express |
| Mobile home | The **dispatcher's chat** when one exists, else Fleet | Matches how crews are actually driven |
| Navigation | Drawer replaces the 4-tab bar on mobile | The chat composer needs the vertical space |

## Slice 1 — Chat tab for a run

### Data model

Two layers. The `chat/` package speaks **ACP** and nothing else; houston's
server wraps its updates with the houston-only kinds.

```go
// package chat — standalone: imports only the standard library.
// One Update = one ACP session/update notification, plus the three fields a
// replayable, resumable stream needs (ID, Seq, TS).
type Update struct {
    ID  string `json:"id"`  // stable across re-reads: "<file-offset>:<n>"
    Seq uint64 `json:"seq"` // assigned by the consumer's ring
    TS  int64  `json:"ts"`  // unix ms, from the record

    SessionUpdate string `json:"sessionUpdate"` // user_message_chunk | agent_message_chunk | tool_call | tool_call_update | plan
    Content []Content `json:"content,omitempty"` // message text; tool output ("content" / "diff")

    // tool_call / tool_call_update
    ToolCallID string     `json:"toolCallId,omitempty"`
    Title      string     `json:"title,omitempty"`  // human line, e.g. hook.ToolHint
    Kind       string     `json:"kind,omitempty"`   // read|edit|delete|move|search|execute|think|fetch|switch_mode|other
    Status     string     `json:"status,omitempty"` // pending|in_progress|completed|failed
    Locations  []Location `json:"locations,omitempty"`
    RawInput   json.RawMessage `json:"rawInput,omitempty"` // omitted on the stream, served by the tool route

    Meta map[string]any `json:"_meta,omitempty"` // ACP extension point: {"phase":"commentary"}, {"subagent":"<id>"}, {"origin":"task-notification"}
}
```

Mapping decisions inside ACP's vocabulary:

- One transcript message → one `*_message_chunk` carrying the whole text
  (chunks are allowed to be whole messages; houston never has partial text
  from a file).
- Codex `phase: commentary` and Claude text between tool calls →
  `agent_message_chunk` with `_meta.phase = "commentary"` (rendered dimmer).
- Thinking/reasoning → not emitted (ACP's `agent_thought_chunk` exists; add it
  later behind a UI toggle if wanted).
- Edit/Write tool results → `tool_call_update` with a `diff` content block when
  the engine records old/new text; otherwise `content`.
- Background-task notices → `_meta.origin = "task-notification"` on a
  `user_message_chunk`; the UI renders it as a divider, not a bubble.

houston-only items (server package, not `chat/`):

| Kind | Shape |
|---|---|
| `permission` | ACP `session/request_permission` params: the pending `toolCall` + `options[{optionId, name, kind: allow_once \| allow_always \| reject_once \| reject_always}]`, each option carrying the pane key it sends |
| `question` | text + optional choices (pane) or reply target (crew) |
| `worker` | crew worker card (slice 3) |
| `event` | divider line: PR opened, worker done, compacted, session ended |

Collapsing consecutive tool calls into one row is a **UI** concern (reuse
`collapseTrail`), so the stream stays one-update-per-ACP-event.

### Engine adapters

```go
// package chat
type Reader interface {
    Engine() string
    // Updates since cursor; reset when the underlying session was replaced.
    Read(path string, from Cursor) (updates []Update, next Cursor, reset bool, err error)
    // Full tool call (rawInput, output/diff) for the expand-on-tap route.
    Tool(path, toolCallID string) (*Update, error)
}
func For(engine string) Reader // nil → hook baseline
```

Chosen per run by `run.agent`, falling back down the list:

| Tier | Engines | Gives |
|---|---|---|
| **Transcript adapter** | claude-code (exists), pi, codex, cursor — each reads the file hookyard names | full chat: user + assistant text, collapsed tools, tool detail |
| **API adapter** | opencode (HTTP messages + event stream) | full chat, no file |
| **Hook baseline** | anything hookyard routes, including an engine with no adapter yet | `user` from `prompt_submit`, `tools` from `pre_tool`/`post_tool`, `assistant` from the turn-end `last_message`, `permission` from the permission state — no intermediate assistant text |

The hook baseline is built from events houston already receives, so every
harness gets a usable chat on day one and an adapter only upgrades fidelity.
Each adapter is its own PR with fixture tests from real session files.

The Claude rules below are the reference; each adapter documents its own mapping
onto the same item kinds.

### Per-engine mapping (measured)

Measured on 2026-09-27 against real sessions in `tmp/samples/` (gitignored;
collected by `tmp/collect-samples.sh`, factify/mono excluded): 4 claude-code
transcripts + 1 subagent file, 4 codex rollouts, 4 pi sessions. No cursor
sample yet.

**Common rules, every reader:** thinking/reasoning is dropped; each tool call
is a `tool_call` (status `in_progress`) followed by a `tool_call_update`
(`completed` / `failed`) matched by the engine's call id; `rawInput` and tool
output never ride the stream — they are fetched on tap from
`GET /api/runs/:id/chat/tool/:toolCallId`.

**claude-code** (`~/.claude/projects/<slug>/<sid>.jsonl`)

- A **human prompt** is `type:"user"` with `origin.kind == "human"`
  (`promptSource:"typed"`, `turnOrigin:"human"`). This is the filter — not a
  text-prefix heuristic. Seen: 11 human prompts.
- `origin.kind == "task-notification"` (`promptSource:"system"`, text starts
  `<task-notification>`) is a background-task result fed back to the model →
  `event` item ("background task finished"), not a bubble. Seen: 17.
- `isMeta:true` user records → dropped.
- `user` records whose content holds `tool_result` blocks (always with
  `toolUseResult`) → attach to their `tools` row by `tool_use_id`.
- **One assistant message is split across several records** sharing
  `message.id` (1–4 records each; one content block per record: `thinking`,
  `text` or `tool_use`). Join text by `message.id`; `usage` repeats on each.
- `parentUuid` forks in the samples are all parallel tool calls
  (tool_use + tool_result siblings), not rewinds → **file order is the
  display order**.
- Everything else is not chat: `attachment` (token reminders, hook output,
  `queued_command`, `prompt_snapshot`, …), `system` (`turn_duration`,
  `stop_hook_summary`), `queue-operation`, `last-prompt`, `custom-title`,
  `agent-name`, `mode`, `permission-mode`, `atis-latch`,
  `file-history-*`, `cost-state` → dropped. Exception: an `attachment` of
  type `queued_command` with `commandMode:"prompt"` is a message the human
  typed mid-turn → `user` item.
- `custom-title` / `agent-name` give the session a display name for free.
- **Subagents live in separate files**,
  `<sid>/subagents/agent-<id>.jsonl`, every record `isSidechain:true` with
  `agentId`. The parent transcript never inlines them → the parent's Task
  tool row links to the subagent file; not shown inline.
- `hub/transcript.go`'s `jsonlRecord` decodes none of `origin`, `isMeta`,
  `isSidechain` or `message.id` today; the adapter adds them.

**codex** (`~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<id>.jsonl`, envelope
`{type, payload}`)

- Chat lives in `response_item`s: `message` with role `user` / `assistant`
  (`output_text`), `function_call` / `custom_tool_call` + their `*_output`
  paired by `call_id`, `reasoning` (encrypted — drop).
- **Injected user messages** are role `user` whose text starts `# AGENTS.md`
  or `<environment_context>` → dropped; role `developer` → dropped. The rest
  are human prompts.
- Assistant messages carry `phase`: `commentary` (progress narration between
  tool calls) vs `final_answer` → both `assistant`, but `commentary` renders
  in the dimmer "narration" style.
- Multi-agent: `spawn_agent` / `wait_agent` / `send_message` tool calls and
  `agent_message` items (author/recipient paths like `/root/test_fixtures`).
  A subagent has its own rollout whose `session_meta.source.subagent` names
  `parent_thread_id` → same treatment as Claude subagents.
- `event_msg` (`item_completed`, `token_count`, `task_started`,
  `task_complete` with `last_agent_message`), `token_usage_record`,
  `world_state`, `turn_context`, `session_meta` → not chat;
  `task_complete` is a turn boundary.

**pi** (`~/.pi/<profile>/sessions/<cwd-slug>/<ts>_<id>.jsonl`)

- Cleanest of the three: `type:"message"` with `message.role` in
  `user` / `assistant` / `toolResult` / `system`. User text had no injected
  content in any sample.
- Assistant `content` blocks: `text`, `thinking` (drop), `toolCall`;
  `toolResult` records pair by `toolCallId` and carry `toolName`, `isError`.
- `id` / `parentId` tree; no branch points in the samples → file order.
- `session`, `session_info` (a name), `model_change`,
  `thinking_level_change` → not chat (`session_info.name` is the title).

**cursor** — unmeasured. The one sample found was a factify session and was
excluded. Its files are `~/.cursor/projects/<slug>/agent-transcripts/<id>/<id>.jsonl`.
Until measured, cursor gets the hook baseline.

### Live items (not from the transcript)

- **permission**: while the run's hook state is `waiting:permission`, the stream
  ends with one `permission` item: the tool + hint of the pending `pre_tool`, and
  `choices` from the pane parser. It is replaced (same `id`, new `seq`) when the
  choices change and retracted when the state leaves permission.
- **question**: while `run.question != nil`, one trailing `question` item with
  `question.text`, and `choices` only when `via == "pane"` and the parser found
  them.
- Choice capture: the parser runs today only inside an open terminal WebSocket.
  The chat endpoint needs choices without one — capture the pane once when the
  run enters `blocked`, and again at most every 2 s while blocked and a chat
  stream is subscribed. Never resize (overhaul invariant).

### API

- `GET /api/runs/{id}/chat?before=<cursor>&limit=50` — a page, newest last.
- `GET /api/runs/{id}/chat/stream?after=<cursor>` — SSE: `item`, `retract`, `reset`.
  `reset` is sent when the transcript is replaced (`/clear`, a new session id on
  the same pane) and the client drops its list.
- Same auth, origin and `Host` pinning as every `/api/` route.
- Resolution ladder matches `/input`: 503 registry not started, 404 unknown run,
  409 only when the run has neither a session nor hook events (a bare tmux
  pane). Every hook-routed run gets at least the hook baseline, so the Chat tab
  is offered for every engine.

### Run → session

`Run` has no session id today. The hooks layer adds an internal
`Session string json:"-"` (the hub session id) when it publishes; the chat
handler asks the hub for that session's engine and transcript path — which the
hub already records for every engine from the envelope. OpenCode runs carry
their OpenCode session id. Never on the wire.

### Hub changes

- Keep the existing trail/preview reduction untouched (Fleet cards use it).
- Add a per-session ring of `chat.Update`s (cap 500), fed by the session's
  engine reader. The hub's existing tail (`refreshTranscript`, fsnotify-driven)
  stays the single trigger; the adapter keeps its own byte offset, because the
  current `parseLine` is Claude's and yields only preview text for other
  engines' files.
- Older pages beyond the ring re-read the file from 0 up to the requested offset.
  Acceptable because only a scroll-back triggers it.

### UI

- `RunDetail` tabs become **Chat · Terminal** (Activity removed once Chat covers
  its glance line; the Activity glance moves into the chat header).
- Rendering per the canvas, screen 2: bubbles for `user`, plain text for
  `assistant` (markdown: paragraphs, inline code, fenced code, lists — no HTML),
  a collapsed row for `tools` that expands; Edit expands to its diff via the tool
  detail route.
- Stick-to-bottom scrolling reuses `ActivityTab`'s logic (`STICK_SLOP_PX`,
  anchored "show earlier").
- Composer: text → `input {type:"text"}`; image → `input {type:"image"}`; an
  `esc` chip → `input {type:"key", key:"Escape"}`. Disabled with a reason when
  `caps.terminal` is false.

### Feels live

No engine writes partial text to its session file — in the samples, Claude
writes a record per finished content block, codex per finished item, pi per
finished message. The chat still feels live at three levels:

1. **Instant placeholders from hooks.** The run already carries the in-flight
   tool (`activity.tool` / `hint`, from `pre_tool`) and state. The UI shows a
   provisional `tool_call` row ("Edit · useTouchGestures.ts ⋯") and a
   "working…" line the moment the hook fires; the transcript's `tool_call` with
   the same tool replaces it when it lands. A composer send shows the user
   bubble optimistically and reconciles on the transcript's
   `user_message_chunk`.
2. **Typewriter reveal.** New `agent_message_chunk` text is revealed
   progressively, finishing within ~1 s regardless of length. Only for updates
   that arrive live — never on page load, scroll-back or reconnect catch-up.
3. **Real streaming from the pane (later, optional, per engine).** While a run
   is `thinking`, houston's control-mode stream already carries what the TUI is
   drawing. Extracting the in-progress reply gives a ghost bubble that the
   transcript record replaces. True token streaming, but screen-scraping —
   its own plan, Claude first, off by default.

## Slice 2 — Answering from chat

- `permission` and pane-`question` cards render one button per choice, max 9;
  tapping sends `input {type:"key", key:"<n>"}`. More than 9 choices: first 9 plus
  "Open terminal" (the key allowlist stops at `9`).
- Crew `question` (`via == "crew"`) renders the existing `ReplyComposer`
  inline in the card → `POST /api/runs/:id/reply`.
- Optimistic state: after a tap the card shows "Sent — waiting for the agent"
  until the item is retracted or replaced. No local state change to the run.
- The needs-you badge and drawer section count exactly what `blocked` counts
  today (`FRESH_MS` rule unchanged).

## Slice 3 — Dispatcher home and crew stream

### Crew → dispatcher join

The crew id is not on the dispatcher's own run (CLAUDE.md, Project and role).
`<commonDir>/crew/crews/<id>/pane` names the dispatcher's pane
(`server/dispatch.go`, `dispatchHomeCrews`). The crew source reads it and stamps
`Crew.Name` onto the run keyed by that pane id. This also removes the reason the
`N workers · M blocked` line hides when two dispatchers share a project.

### Crew stream

`GET /api/crews/{id}/chat[/stream]` = the dispatcher run's items, merged by `ts`
with items derived from that crew's bus records:

| Bus record | Item |
|---|---|
| `dispatch` | `worker` (title, codename, tier, model, branch; `ref` = worker run id once joined) |
| `status` done / failed | `event` ("lynx finished", "otter failed: <detail>") |
| `status` with `pr_url` | `event` with `ref` = PR url |
| `status` blocked with a question | not an item — the worker's own `question` item is surfaced (see below) |
| `msg` | not shown (it is already in the dispatcher's transcript as a tool call) |

Worker questions and permissions appear in the crew stream as the worker's own
`question` / `permission` item, re-keyed with the worker as `ref`, so they can
be answered from the dispatcher screen (canvas screen 1).

A houston-minted crew has no dispatcher run; its stream is bus items only and the
composer is replaced by "+ Dispatch".

### UI

- Mobile home (`#/`) = the most recently active live dispatcher's crew chat;
  none → Fleet.
- Header: project · "dispatcher", crew id, switcher to other dispatchers.
- Worker strip: one chip per worker, needs-you first, tap → that worker's run
  detail (Chat tab).
- Drawer (canvas screen 4): Needs you · Dispatchers · Solo sessions · All tmux
  panes · Fleet list. Replaces the mobile tab bar. Desktop console shell is
  unchanged.
- Quick-action chips above the composer are static in this slice ("+ Dispatch",
  "Status of all" sends that literal text). Context-derived chips are later.

## Acceptance criteria

1. A Claude run's Chat tab shows every user and assistant message from its
   transcript in order, with no injected/meta text, verified against at least
   three real transcripts including one with `/clear`. Each further adapter
   meets the same bar against its own engine's files.
1a. A run on an engine with no adapter shows the hook baseline: prompts, tool
   rows and final replies, in order.
1b. A crew with workers on two different engines renders both in the dispatcher
   stream with the same card shapes.
2. Scrolling back 200 items on a phone over Tailscale loads pages without
   jumping the viewport.
3. Killing the SSE connection mid-turn and reconnecting shows no duplicated and
   no missing items.
4. A `waiting:permission` run shows a permission card whose buttons match the
   pane's prompt; tapping 1 resolves the prompt and the card retracts within 2 s.
5. A crew-blocked worker's question is answerable from the dispatcher screen and
   from the worker's own chat, and the reply lands on the bus.
6. With two live dispatchers in one project, each crew stream shows only its own
   workers.
7. No endpoint ever resizes a pane; `refresh-client -f ignore-size` behaviour is
   unchanged.
8. All of the above checked on a real phone, not only in tests (state-of-play:
   "#8 has never been looked at").

## Open questions

Resolved by measurement (see "Per-engine mapping"): the Claude meta-record
filter (`origin.kind`), Claude sidechains (separate files), codex injected
context, pi's shape. Resolved by decision: readers live in a standalone
`chat/` package in houston, not hookyard, and become their own tool later.

Still open:

- **`/clear`, `/resume`, compaction.** No sample contained one. Need a Claude
  session with `/clear` and one that auto-compacted, to define `reset`.
- **cursor transcript shape.** Needs a non-factify cursor session.
- **Choice capture cost.** Is a 2 s capture-pane while blocked acceptable
  alongside control mode, or should choices ride the existing control-mode
  output for that pane?
- **Hook baseline gaps.** Does every engine's `prompt_submit` envelope carry the
  prompt text, and every `turn_end` a final message?

## Later

- **Slice 4 — terminal polish** (plan A in the state-of-play doc): font-size zoom
  9–24px, Readable/Fit, follow-cursor, choice bar in the terminal, fix
  `useTouchGestures.ts` line-height.
- **Slice 5 — dispatcher cards:** a `card` crew-bus record kind (title, body,
  actions) the dispatcher writes on dispatch / merge / decision.
- Context-derived quick actions ("Merge #172").
- Push notification when a run enters `blocked` (M3 seam in the overhaul spec).
