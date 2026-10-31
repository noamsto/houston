# Chat and answering from Chat

Read when changing the Chat tab, `chat/` readers, chat routes, or the answer/permission-prompt executor.

Moved verbatim from the root `CLAUDE.md`; section headings are unchanged.

## Chat

A run's Chat tab renders the agent's own session transcript as a
conversation. Design and measured per-engine mapping:
`docs/superpowers/specs/2026-09-27-mobile-chat-and-dispatcher-home.md`
(slice 1); what was built: `docs/superpowers/plans/2026-09-27-chat-tab-slice-1.md`.

- **`chat/` is standalone**: it imports only the standard library
  (`chat/boundary_test.go` fails otherwise) so it can move to its own module.
  It speaks ACP `session/update` shapes (`chat.Update`: `user_message_chunk`,
  `agent_message_chunk`, `tool_call`, `tool_call_update`) plus `id`/`seq`/`ts`.
  `chat.For(agent)` picks the reader; claude-code (`"claude"`,
  `"claude-code"`) and `"pi"` have one, so codex and cursor have no Chat tab yet.
  `chat.ToolTitle` mirrors `hook.ToolHint`; `hub/chat_title_test.go` pins them.
- **pi mapping** (`chat/pi.go`): `type:"message"` entries count, with
  roles user/assistant/toolResult; `system` and other roles, and other
  non-message entries (`session`, `session_info`, `model_change`,
  `thinking_level_change`), are dropped. `branch_summary` and `compaction`
  entries become a `user_message_chunk` with `_meta.origin` `pi-branch` /
  `pi-compaction` and the summary as content (a `/tree` switch without a
  summary writes no entry, so no divider). Assistant text →
  `agent_message_chunk` with `_meta.messageId` = the entry id, thinking
  blocks dropped, `toolCall` → `tool_call`; a `toolResult`
  pairs by `toolCallId` (`isError` → failed). `Tool()` builds an edit diff from
  the arguments' `edits[]`; `write` gets none. An `aborted`/`error` assistant
  message's tool calls are emitted `failed` (pi never runs them) and an
  `error` message's `errorMessage` becomes an `Error: …` agent chunk.
- **Readers must be chunking-independent**: reading a file in one call or in
  any number of incremental calls yields the same updates. They consume only
  complete lines, never replace an emitted update, and keep no per-file state
  (safe for concurrent use). That is what makes `seq` = the update's ordinal
  from byte 0: the hub ring (`hub/chat.go`, 500 per session) and a scroll-back
  re-read of the file land on the same seqs, across houston restarts too.
  The ring is built lazily: a session has none until its first chat page,
  stream, epoch or subscribe call, which reads the transcript into it (the
  tool route reads the file directly and builds nothing); from then on the
  hub's 2 s tick maintains it. A late-built ring is identical to
  one built at start, so seqs and epochs are unaffected.
- **Epoch**: every cursor is `<epoch>.<seq>`, epoch = a hash of (session id,
  transcript path, hash of the file's first line, ring generation); the raw
  session id never reaches the wire. The generation bumps when the reader
  reports a reset (the file shrank, or the bytes before the cursor changed —
  `chat.Cursor.Pending` fingerprints them) or the path changes; it restarts at
  0 with houston, which is why the file identity is in the hash. A brand-new
  session whose file has no complete line yet resets once when its first line
  lands. A new session in the same pane is a new run `Session`, hence a new
  epoch.
- **`_meta` keys**: `messageId` (on text chunks and tool calls), `tool` (tool
  name — `title` is the human hint), `origin` (`task-notification`,
  `pi-branch`, `pi-compaction`; rendered as a divider), `subagent` (on the
  Agent/Task call's update). The claude reader
  never sets `phase`: the UI treats a text chunk as commentary when
  `_meta.phase == "commentary"` or a later `tool_call` shares its `messageId`.
  Tool input and output never ride the stream; the tool route serves them.
- **Run → session**: `runs.Run.Session` (`json:"-"`) is set by the hooks layer.
  `caps.chat` is true when the key's hooks layer names a Session whose agent
  has a reader, or its crew layer names one (`engine_session`, see "Project and role") for an engine with a reader (`deriveCaps`); the UI offers the tab from it (no probe), and it
  is the default tab when the route names none (`#/fleet/<id>`), except on a joined dispatcher in dispatcher mode, whose default is the Crew tab (see "Navigation").
- **Routes** (`server/runs_chat.go`, behind the `/api/` auth gate; none touches
  tmux): `GET /api/runs/{id}/chat?before=<seq>&limit=<≤100>` →
  `{epoch, updates, more}`; `GET /api/runs/{id}/chat/stream?after=<epoch>.<seq>`
  → SSE `updates` batches with `id: <epoch>.<last seq>` (`Last-Event-ID` wins
  over `after`, so a native EventSource reconnect resumes with no gap), `reset`
  then EOF on an epoch mismatch, a cursor the ring can't serve, the run's
  Session changing (checked every 2 s), or the session going away; the first pull after every connect always sends an `updates` event, `[]` when nothing is missed, so the UI can tell the catch-up batch from live ticks (only the newest update of the catch-up is marked live and animates, `useRunChat`); an idle
  stream gets a named `ping` event every 25 s (see "Stream liveness");
  `GET /api/runs/{id}/chat/tool/{callId}` → name/input/output/diff, each of
  output/oldText/newText capped at 16 KiB (`truncated`), an input over 16 KiB
  omitted rather than cut (`inputOmitted`, since a cut would no longer be
  JSON); a
  failed call carries its error output and no diff. Ladder: 503 no registry ·
  404 `no such run` · 404 `no chat`.
- **Real-transcript check**: `HOUSTON_SAMPLES=<dir> go test -tags samples ./chat/`
  replays `<dir>/claude-code/*.jsonl` and `<dir>/pi/*.jsonl` and logs counts
  only; it skips when unset. Never commit transcript content — fixtures under `chat/testdata/` are
  hand-written. `go run -tags tools ./cmd/sessionlog <file>` prints a file as
  ACP JSONL for eyeballing.


### Answering from Chat

Claude runs only (`run.agent` `claude`/`claude-code`; `isClaudeAgent` on both sides). On another engine's live run the card stays read-only with "answer in the Terminal tab". Code: `answer/` (pure text in, keys out), `server/runs_answer.go`, `ui/src/api/answer.ts`, `QuestionCard.tsx` + `questionStage.ts`, `PermissionBar.tsx`.

- **Question picker**: a pending `AskUserQuestion` card (run live with a terminal, Claude) shows every question as radio (single-select) or checkbox (multi-select) rows plus an Other row with a text field (not for a preview-layout question, see below). Taps only *stage* (`questionStage.ts`; single-select is exclusive and never unselects); nothing is sent until the one "Send answer" button, enabled when every question has an option or non-blank Other text and no question has Other switched on with blank text. Single-select rows are a radiogroup with a roving tabindex (one tab stop, arrow keys move and select; arrowing onto Other keeps focus on the radio, only a tap moves it into the text field). If the detail cannot be shown (fetch error, omitted or unreadable input) a pending question on a live run with a terminal shows a hint and an "Open Terminal" link instead, and the card calls `onLayout` when its detail loads or the picker appears so the sticky timeline stays pinned. A completed or failed call, or a run that is not live (no terminal, `done`, `failed`), renders read-only.
- **`POST /api/runs/{id}/answer`** (both modes). Body, unknown fields 400, max 64 KiB (413): `{"kind":"question","toolCallId","answers":[{"question":i,"options":[0-based…],"text"?}]}` (one entry per question, in order) or `{"kind":"choice","ordinal":1..9,"frame":"<64 hex>"}`. Same resolution ladder as `/input` (503/404/409/409/409/503, via `runPane`), then body (invalid UTF-8 → 400 before decoding, since `encoding/json` would turn it into U+FFFD), then 422 `only a Claude run can be answered`. Free text is trimmed, may hold no `Cc` rune, ≤ 500 runes; `Cf` (ZWJ, bidi marks) is allowed. Single-select takes exactly one option xor text; multi-select takes any options plus optional text, not neither. Statuses: 204 typed; 400 invalid body/answer/`tool call is not a question`; 404 `no chat` / `no such tool call`; 409 `already answered` (transcript says the call finished), `prompt changed` (first check failed, nothing typed), `pane is in copy mode` (nothing typed), or 409 `{"partial":true}` (a later check mismatched or timed out, the pane entered copy mode, or the request was cancelled, after keys went in); 413; 422 unreadable questions or a plan the answer cannot fit (≤ 8 options per question keeps the Other digit a single key); 429 another answer is in progress for that pane (per-pane `TryLock`, no queueing); 500 chat read or first-key send failed; 502 `{"partial":true}` a send failed after keys went in; 503 `tmux unavailable` (the first capture or mode probe failed, nothing typed) or 503 `{"partial":true}` (a capture or mode probe failed after keys went in). Logs carry pane, kind, step count and status, never text, labels or frames.
- **Executor** (`typeSteps`): the questions come from the transcript (`ChatTool` on `toolCallId`), never from the client; the client supplies only indexes and text, validated against them. `answer.Plan` returns steps of `{Expect, Keys}`. The pane is captured (`capture-pane -p -e`, 200 lines) and must pass `steps[0].Expect` before the first key; after every key batch the executor polls (100 ms, 3 s timeout) until the next step's `Expect` passes. Before the first key and before every later key batch it also probes `#{pane_in_mode}` (`PaneInMode`): `capture-pane` shows the live screen under copy mode, so every check would pass while copy mode swallowed the keys. Any failure — mismatch, timeout, capture or probe error, copy mode, cancel — stops typing; nothing after the last batch is checked. Key sequences (Claude Code 2.1.293 and 2.1.299, which render these dialogs alike):
  - Single-select, one question (plain dialog): the option's digit selects and submits.
  - Single-select Other: digit `n+1` (Other row), then the text literally, then Enter.
  - Preview layout: a single-select question where any option has a non-blank `preview` renders side by side in Claude Code 2.1.293 and 2.1.299 (`Question.PreviewLayout`): labels on the left, the focused option's preview box on the right, no description lines, no "Type something" Other row, an unnumbered `Chat about this` row, a `Notes: press n to add notes` hint, and `n to add notes` in the footer. Multi-select questions ignore previews; in a tabbed dialog each tab renders per its own question. Checks compare only the label column (each line cut at the preview box's left border); the preview itself is never compared (it can be clipped), and typed notes fail the check. The digit only moves the cursor here, so the plan is digit, a check that the cursor landed, then Enter (submits a plain dialog, advances a tabbed one). Other text on such a question is 422 (no Other row), and `QuestionCard` hides the Other row for it (same predicate).
  - Multiple questions or any multi-select makes a *tabbed* dialog: a digit on a single-select advances to the next tab; on a multi-select a digit toggles its box without moving the cursor; Down moves to Other (type text, then Down) and on to the Next/Submit row; Enter confirms. After the last question the review tab is checked against every question and answer as Claude summarises it, then `1` submits.
- **Dialog region rule**: checks run only inside the active dialog at the bottom of the pane — the lines after the last `─` rule that is directly followed by a tab bar or `☐`/`☒` header, where only an unindented (column 0) rule line counts, so a `────` line inside an indented command or description cannot move the anchor; the rule must span the pane's full width (`#{pane_width}`, read in the same tmux call as the capture, `tmux.Client.CapturePaneWidth`), so a short column-0 `──` spilled into a command never anchors. A capture with no full-width rule (a dialog taller than the 200 captured lines) holds no dialog: `/prompt` is 404, an answer whose first check sees none is 409 `prompt changed` with nothing typed, and a later step that loses it times out as 409 `{"partial":true}`; an unreadable `#{pane_width}` is a capture failure (503 `tmux unavailable`). This applies to question and permission dialogs alike. A named session (`claude --name`, as the dispatcher launcher starts every dispatcher) keeps its input box's top rule, carrying the name, as the pane's last line under a question dialog (blank rows between): `─…─ <name> ─`, or ` <name, cut with …> ─` when the pane is narrow. Exactly that one line is dropped before the question and review checks, only when it is the capture's last non-blank line and it starts in column 0 (a `─` or one space) and ends in ` ─`; a name it cannot recognise (e.g. one starting with `─`) is not dropped, so the dialog fails closed as 409. Permission dialogs draw no such line. Checks never run against scrollback, which repeats the question in the user's prompt and in earlier answer summaries. Text is compared normalized (`answer.Normalize`: leading `│` dropped, all whitespace deleted; ANSI and OSC 8 are stripped earlier, in `captureLines`) so Claude's hard wrapping cannot matter. A tab must show exactly this question, its options with the expected cursor row — each option row must equal the label followed by its description exactly (no prefix match; descriptions come from the transcript's tool input) — ticked boxes and Other text, the `Chat about this` row and an `Esc to cancel` footer; the tab bar's per-question boxes must be ticked before this tab and clear after it, so look-alike questions in another position do not match. A permission dialog never parses as a question dialog and vice versa.
- **`GET /api/runs/{id}/prompt`** (both modes): the permission-style dialog at the bottom of a Claude run's pane (`answer.PermissionPrompt`: after the last unindented (column 0) full-width `─` rule, a line ending in `?`, consecutive choices `1..m` (2 ≤ m ≤ 9) with exactly one `❯` cursor, then an `Esc to cancel` footer). 200 `{question, choices[], detail (≤ 2 KiB, ending in `… (truncated — open the Terminal tab)` when cut), frame}`; 404 `no prompt`; ladder as above, 422 non-Claude. `frame` is a sha256 of the whole dialog, command included: leading `│` dropped, non-blank lines joined by line breaks, words within a line joined by single spaces (so `rm -rf /tmp/x` and `rm -rf / tmp/x` differ, and a line break that moves changes the frame), and `─`/`╌` runs collapsed to one. A rewrap after a resize therefore also changes the frame, and the UI refetches after the 409. `{"kind":"choice"}` re-captures, requires the same `frame` (else 409 `prompt changed`, nothing typed), 400 `no such choice` past the last choice, probes for copy mode (409 `pane is in copy mode`), and presses the ordinal as one key. The hash covers the command's words and line breaks, so an approval cannot land on a command that differs from the one the user saw. A dialog with `Type something` / `Chat about this` rows is a question dialog, not a permission prompt.
- **`PermissionBar`** (`ui/src/fleet/PermissionBar.tsx`): polls the prompt route every 2 s while the run is Claude, has a terminal and its `state` is `blocked` *or `idle`* — `idle_prompt` demotes a still-open dialog to `idle`, and the route stays the authority on whether one exists. A failed poll keeps the last value; polls never overlap and each times out after 10 s. One tap sends one ordinal with the frame it displayed; success hides that frame until a poll shows none or another, or shows it again after 3 polls that still return it. A 409 shows the notice "The session moved on — refresh" (a ≥44 px dismiss button) and refetches; it lives outside the dialog, so it survives the dialog unmounting, and stays until dismissed, a later answer is sent, or the bar deactivates (the run leaves `blocked`/`idle`). Only polls started after a successful answer count toward the 3-poll re-show; an in-flight poll from before the tap is ignored.
- **`answeredHere`** (`RunQuestion`, `RunStatusStrip.tsx`): `ChatTab.tsx` sets it when the Chat tab already offers the answer (a permission bar is shown, or an answerable `AskUserQuestion` is pending); it hides the Chat header's "Reply in Terminal" button so the two affordances do not compete.
- **UI result handling** (`ui/src/api/answer.ts`): 409 → `moved` (`partial` from the JSON body), except `pane is in copy mode`, which is an error telling the user to exit copy mode and retry ("The terminal is scrolled back (copy mode) — exit it, then retry"). Only a 409 `{"partial":true}` locks the question picker and links to the Terminal tab ("Part of the answer went in"); the moved (non-partial 409) and error states link to the Terminal tab too; a 502/503 `{"partial":true}` shows "may have been partly sent — check the agent before resending" and leaves the picker unlocked; a 15 s client timeout reports the answer may be partly sent. Sent state is tied to the call status, so the card unlocks when the status changes.
- **Drift fails closed.** Fixtures are hand-written captures, recorded against Claude Code 2.1.293 (`answer/testdata/`) and 2.1.299 (`answer/testdata/2.1.299/`, and `answer/testdata/2.1.299-named/` for a named session); a Claude Code UI change that breaks a check answers 409 and types nothing (or stops at the first key batch whose result no longer matches), never guesses. When Claude Code's dialogs change, re-record the fixtures and update the parsers together.


