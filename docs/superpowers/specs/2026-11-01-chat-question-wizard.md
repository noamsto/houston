# Chat: live step-by-step AskUserQuestion wizard (#330)

## Problem

Answering a multi-question `AskUserQuestion` from the Chat tab replays one
precomputed key plan for every question in a single request
(`answer.Plan` → `typeSteps`). Any check that misses mid-replay stops typing
with keys already in the pane (409 `{"partial":true}`), and the card locks with
"Part of the answer went in". The card shows every question on one long form,
with no review and no way back.

## Evidence (recorded 2026-11-01, Claude Code 2.1.310)

Recorded in an isolated QA tmux server (`scripts/qa/setup.sh --no-agents`,
`TMUX_TMPDIR=/tmp/hqa.*`), claude `--model haiku`, 60-column pane, throwaway
questions only. Two configurations were recorded: the owner's settings
(`editorMode: "vim"`, `$EDITOR` nvim), and a named session with
`--settings '{"editorMode":"normal"}'`.

Findings, each pinned to a frame:

1. **2.1.310 renders these dialogs the same as 2.1.299.** Fresh tabs,
   multi-select boxes, preview layout, review and plain dialogs match the
   2.1.299 fixtures line for line. The existing checks pass the 2.1.310 frames
   of a fresh tab (`m3-q1`, `m3-q2-after-blue`, `m3-q3`, `plain-q`). The
   version lead is refuted.
2. **Vim editor mode changes the footer once the Other field has focus.**
   With `editorMode: "vim"`, the cursor on an Other row (single-select,
   multi-select, plain) shows `-- INSERT -- · Enter to select · Tab/Arrow keys
   to navigate · ctrl+g to edit in Nvim`, with no `Esc to cancel`. `isTail`
   requires the footer to end in `Esc to cancel`, so the check after the Other
   digit fails. Reproduced through the old route: houston returned 409
   `{"partial":true}` with the cursor left in the Other field (`repro-other-q1`).
   The same check passes in normal editor mode (`nv-q1-other-cursor`).
   The 2.1.299 fixtures were recorded in normal mode, so they never covered this.
3. **Re-entering a tab marks the committed answer with a trailing `✔`.** After
   Back (`Left` or `BTab`), the tab shows the cursor on row 1 and `2. Blue ✔`
   (`m3-q1-back-left`), `3. teal ✔` for a committed Other
   (`m3-q1-back-other-answered`) and `2. Large ✔` in the preview layout
   (`m3-q3-back-from-review`). Multi-select ticks and Other text persist
   (`m3-q2-reenter-toggled`, `m3-q2-back-from-q3`). The exact-row checks reject
   every re-entered tab (probe: `m3 q1 back` = false).
4. **Navigation keys.** `Left` and `BTab` move one tab back, from a question tab
   (single-select `m3-q1-back-left`, multi-select `m3-q1-back-from-toggled`,
   preview `m3-q2-back-from-q3`, whose footer advertises only `↑/↓`) or from
   review (`m3-q3-back-from-review`). `Right` and `Tab` move one tab forward without answering.
   Every tab, fresh or re-entered, starts with the cursor on row 1. A digit on
   a single-select tab commits that option and advances, including re-pressing
   the already committed option (`v2-q2-after-repress`). Enter on the Next or
   Submit row of a multi-select tab advances. `1` on review submits.
5. **The Other row keeps its text after another option is committed**
   (`3. navy` beside `2. Blue ✔`, `m3-q1-back-blue2`). Pressing the Other digit
   on a single-select tab whose field holds text *commits that stale text and
   advances* (`m3-q1-other-reenter-cursor`). A plan must reach Other with
   `Down` and clear the field before typing.
6. **Clearing the Other field.** `C-u` clears one visual line of the field. A
   wrapped two-line text needs two presses (`v2-q1-long-cleared`,
   `v2-q1-long-cleared2`). `C-u` on an empty field changes nothing
   (`v2-q1-cu-empty`). Clearing a committed single-select Other leaves
   `Type something. ✔`. Clearing a multi-select Other unticks its box.
7. **A wrapped committed Other loses its `✔`** on re-entry
   (`v2-q1-back-long`). The tab bar's box for that tab is still ticked, which
   is the fallback signal.
8. **Keys in one `send-keys` call can be dropped.** `send-keys 1 3` toggles
   nothing, while one tmux exec per key toggles both, even back to back
   (`fast.sh`, 6/6). The executor already sends one exec per key. Keep that.
9. **`Up` returns the cursor to row 1 from anywhere on a tab.** From a focused
   vim Other field holding text, `Up` leaves the field to the row above
   (`v3-q1-up-from-other`), and a second `Up` reaches row 1 (`v3-q1-up2`).
   A focused *wrapped* field also exits on the first `Up`
   (`v3-q1-wrapped-up1`). It wraps a few columns narrower while focused
   (`v3-q1-wrapped-focused` vs `-up1`), so a line count read from an unfocused
   row can undercount the focused field by one. On review, `Up` moves the cursor
   from `2. Cancel` to `1. Submit answers` (`v3-review-up`).
   From a multi-select Submit row, `Up`×(n+1) reaches row 1 (`v3-q2-up4`). The
   old route itself answered 204 after two `Up`s cleared a stranded Other
   cursor (`repro-opts`).

## Root causes of the observed failures

| Observed failure | Root cause | Test that would have caught it |
| --- | --- | --- |
| 409 / "Part of the answer went in" | (a) The footer check rejects the vim `-- INSERT --` footer (finding 2), so every Other answer under vim mode fails right after its first key. (b) The whole wizard replays in one request, so any later miss strands keys already sent. | `answer` fixture tests over the `2.1.310/` vim frames for every Other step. Step-scoped plans mean a miss strands at most one tab's keys. |
| Card missing or unusable | Every path that hides the picker today, with what the recorded API responses (`card-evidence.txt`: a live 2.1.310 wizard, dialog open) say about it. **Ruled out for this setup:** `answerable` false (the run row reads `agent:"claude"`, `state:"blocked"`, `caps.terminal:true`); the call not classified as a pending question (the chat page shows `tool_call` `AskUserQuestion` `in_progress`); `inputOmitted` or zero parsed questions (the tool route answers 200, `inputOmitted:null`, all questions present). **Remaining causes:** (a) the detail fetch: `QuestionCard` fetches once per call status, with no timeout and no retry. A failed fetch (phone resume, network blip) shows "Can't show this question here — Open Terminal" until the status changes, which never happens while the dialog is open. A hung fetch shows a bare row with no picker and no link. (b) After a 409 `{"partial":true}` the picker locks for the same lifetime: "unusable". `inputOmitted` (input over 16 KiB, e.g. long previews) is still a latent way to lose the picker. The new design closes it by serving the parsed questions from `state` (no cap), so the wizard no longer depends on the capped tool detail. | UI tests: a first `fetchTool` rejection followed by success shows the card. A wizard renders from `state` questions when the tool detail is `inputOmitted`. A per-step error leaves the card usable, with Re-check. |
| Wrong answer lands | Inferred from frames, not reproduced end to end. After a mid-replay stop, the dialog is left with the cursor inside the focused Other field (`repro-other-q1`). Any later keys typed into the pane go into that field and submit as the answer: a composer reply, quick keys, or a terminal tap. Finding 5 is a second path: an Other digit pressed over retained text commits the stale text. | The vim fixture tests (no stop after the Other digit). Plan tests asserting that Other is reached with `Down` + `C-u`, never the Other digit. |
| Other free text ends nowhere | Same check as the first row: the plan stops after the Other digit, before the text is typed. | Fixture test: `Other` on single, multi and plain tabs passes every check under the vim footer. Go test: text lands and is verified. |
| Hard to use on a phone | Design: one long form, no review, no Back. | UI tests for step navigation, review and jump-to-step. Mobile screenshots. |

## Design

### Server contract: `POST /api/runs/{id}/answer`

The route, its body cap, unknown-field rejection, UTF-8 check, `runPane`
ladder, Claude-only 422, per-pane `TryLock` (429) and logging stay as they are.
The `question` kind (whole-wizard replay) is removed. `choice` (permission
prompt) is unchanged. New kinds, each self-contained, with the pane as the
source of truth:

| Kind | Body | Pane must show (else 409 `prompt changed`, nothing typed) | Keys | Success |
| --- | --- | --- | --- | --- |
| `state` | `{kind, toolCallId}` | a tab or review of this call (any cursor) | none | 200 state; 404 `no question` when the pane shows none of this call |
| `step` | `{kind, toolCallId, index, answer:{options, text?}}` | tab `index`, cursor on any row | `Up`×k to row 1 (if needed), then that tab's keys only | tabbed: 200 state of the tab it landed on. Plain: 204 (submitted). |
| `back` | `{kind, toolCallId, index}` | tab `index` (`index` ≥ 1), any cursor, or review when `index` = question count | `Up`×k to row 1 (tabs only), then `Left` | 200 state of tab `index-1` |
| `submit` | `{kind, toolCallId, review:[string…]}` | review, every box ticked, each answer equal (normalized) to `review[i]`, cursor on either choice | `Up` if on `2. Cancel`, check it is on `1. Submit answers`, then `1` | 204 |

**Cursor normalization.** A step or back starts from wherever the cursor is
on the tab (finding 9). `k` is the read cursor row minus one, or n+1 from a
multi-select Next/Submit row. After the `Up`s, the pane must show row 1 with
the tab otherwise unchanged before anything else is typed. A partial step
leaves the cursor mid-tab (`repro-other-q1`). The next step, Back or Re-check
therefore never sees a state it cannot act on, and nothing needs a separate
reset. This is the user's own request, verified key by key, not an automatic
retry.

Every kind reads the questions from the transcript (`ChatTool`) and applies the
`already answered` / `no such tool call` / `not a question` / `cannot read the
questions` / `questions repeat` ladder. `step`, `back` and `submit` hold the
pane lock for their whole run (429 when busy). `state` types nothing and takes
no lock, so the card's poll never makes a step answer 429. It logs at Debug
(Warn on a capture failure), since the card polls it. A state read during
a step may see a mid-step screen, so the UI ignores polls while a request is in
flight.

State JSON:

```json
{"tabbed": true, "step": 1, "count": 3, "questions": [...], "options": [0, 2], "text": "no onions"}
{"tabbed": true, "step": 3, "count": 3, "questions": [...], "review": ["Blue", "Basil, no onions", "Small"]}
```

- `questions` is the server's own parse of the call:
  `[{question, header, multiSelect, preview, options:[{label, description}]}]`,
  previews omitted, so it has no size cap. The wizard renders from it and
  sends indexes into it, so the UI and the server can never disagree on an
  index.
- `tabbed` is `count > 1 || any multiSelect` (the server's predicate). `step`
  is the active tab's index; `step == count` is review. A plain dialog is
  `tabbed:false, step:0, count:1` and has no review. A one-question
  multi-select is tabbed and has a review (`smulti-review`).
- `options` (ascending, 0-based) and `text` are the tab's committed selection
  as the pane shows it: single-select, the row marked `✔` (or the Other row);
  multi-select, the ticked boxes and a ticked Other row's text; preview, the
  `✔` row. A wrapped Other that lost its `✔` counts as committed when that
  tab's bar box is ticked, no option row has `✔` and the Other row holds text
  (finding 7). `text` is the Other row's visible text with wrapped lines joined
  by one space and the `✔` mark removed. `options: []` and no `text` means
  nothing committed.
- `review[i]` is the text after `→` for question `i`, wrapped lines joined by
  one space.

Validation (400, never quoting client text): `index` in range, `options` in
range and unique, `text` trimmed, ≤ 500 runes, no `Cc` rune, and no `✔`
(U+2714, the pane's commit mark). Single-select takes one option xor text.
Multi-select takes any options plus optional text, not neither. Preview takes
one option, no text (422, as before). `review` needs exactly `count` strings,
each ≤ 2000 runes with no `Cc`. A call whose questions repeat (identical
normalized text and option rows) is 422 `questions repeat`: the pane identifies
a tab by its text, so look-alike tabs could not be told apart.

Failure after keys went in: 409 `{"partial":true}` (a check mismatched or timed
out, copy mode, cancel), 502/503 `{"partial":true}` as today. These failures
are now scoped to one step.

### `answer` package

- **Tab reading** (`frame.go`): one parser reads a question tab into a
  `TabView`: active question index (by spec match), cursor row (1-based, 0 =
  Next/Submit), ticked boxes, committed option (`✔`), Other text (normalized
  and raw), Other line count, tab-bar boxes. It applies the dialog region rule,
  named-session rule drop and preview label-column cut as today. Each row must
  be exactly `label+description` or `label+✔+description`. The Other row is the
  placeholder or free text, with an optional `✔`. Every check is "read the tab,
  compare the fields this expectation names", so a check still fails closed on
  anything it cannot parse.
- **Footer**: the tail is the `Chat about this` row, then 1–4 footer lines.
  Normalized and joined, they must be one of two pinned shapes. Normal:
  `Enter to select · <nav>[ · n to add notes][ · Tab to switch questions][ ·
  ctrl+g to edit in <E>] · Esc to cancel`. Vim insert (Other field focused):
  `-- INSERT -- · Enter to select · <nav>[ · ctrl+g to edit in <E>]`, with
  nothing after. `<nav>` is `Tab/Arrow keys to navigate` or `↑/↓ to navigate`.
  `<E>` is one editor name with no `·`. Any other footer fails, so a dialog
  showing an unknown mode (e.g. vim `-- NORMAL --`) fails closed.
- **Review reading**: `ReadReview(qs, capture, width)` returns the per-question
  answer strings, requiring the tab bar with every box ticked, `Review your
  answers`, each `● question` in order, `Ready to submit your answers?` and the
  cursor on `1. Submit answers`.
- **Plans**: `PlanStep(qs, index, answer, view)`, `PlanBack(qs, index, view)`
  and `PlanSubmit(qs, review)` return `[]Step`. The server captures the pane,
  reads the view, plans from it, and hands the plan to the executor.
  `steps[0].Expect` is "the pane shows exactly this view", which the executor
  checks on its own fresh first capture (else 409 `prompt changed`, nothing
  typed), so a change between the read and the first key cannot slip through.
  A tabbed step or back ends with a **landing** step: an Expect with no keys
  (the next tab with the cursor on row 1 and this tab's box ticked, review, or
  for back tab `index-1` on row 1). The executor (`typeSteps`) changes in two
  ways only. It returns the capture and width that passed the last Expect, and
  the server builds the 200 state from that capture. It skips the copy-mode
  probe for a step with no keys. Polling, timeouts, per-batch copy-mode probes
  and partial classification are unchanged. Every plan opens with the cursor
  prelude when the view's cursor is not on row 1: `Up`×k, then a check of row
  1 with the same view. Key sequences from the frames:
  - single option `k`: digit `k`. Then the landing check. A plain dialog
    submits, with nothing checked after.
  - preview option `k`: digit `k`, check the cursor is on `k`, `Enter`, then as above.
  - single Other: `Down`×n, check the cursor is on Other showing the viewed
    text. Then `C-u`×(2·lines+1) if the field held text (extra presses on an
    empty field change nothing, finding 6; the focused field can wrap to more
    lines, finding 9), check the placeholder.
    Type the text, check it, `Enter`, then as above.
  - multi: one digit per box in (ticked ⊕ wanted), check boxes and cursor 1. If
    the field and the wanted text are both empty: `Down`×(n+1). Otherwise:
    `Down`×n, check Other, `C-u`×(2·lines+1) when it held text, check the
    placeholder, type the text if any, check it, `Down`. Then check the cursor
    is on Next/Submit with the wanted boxes, `Enter`, then as above.
  - back: `Left`, then tab `index-1` with the cursor on row 1.
  - submit: check review against the strings, then `1`.
- `answer.Plan`, `reviewCheck` and the `tabState` exact-row checks go away,
  replaced by the above. `PermissionPrompt` is untouched.

### UI (`QuestionCard`)

- Pending + answerable: on mount the card posts `state` and shows that step,
  rendering from `state.questions`. While the card is idle (no request in
  flight) and the page is visible, it re-reads `state` every 2 s, so a change
  made in the Terminal shows up in Chat. A poll that reports another step or
  review moves the card. A poll on the same step updates the shown selection
  only if the user has not touched that step since it was shown. A failed poll
  keeps the last state. If `state` fails (404, 503) on mount, the card shows the
  questions read-only from the tool detail (if available) with the Terminal
  link, and keeps polling.
  Steps: progress "2 of 4", the headers as chips, the question, the options
  (radio/checkbox), Other (not for preview), and Back / Next as a bottom row
  of ≥ 44 px buttons. Next is enabled when the stage is complete. The last tab's
  Next leads to review.
- A step screen opens with the pane-reported selection. If the pane's Other
  text normalizes equal to the client's remembered text, the client's exact
  text is shown.
- Next posts `step`. Back posts `back`. Each response's state moves the card.
  Review lists each question with the pane's review string. Tapping a row posts
  `back` repeatedly (one verified request per tab) until that step. Submit
  posts `submit` with the strings shown. Success shows "Sent — waiting for
  Claude", tied to the call status.
- Plain dialog (one single-select question): one screen with the old picker
  layout and a "Send answer" button that posts `step` index 0 (204 = sent).
- Errors: a 409 with nothing typed re-reads `state` (a read, never a key) and
  moves the card to what the pane shows, with the notice "The terminal moved
  on — nothing was typed". A partial failure stays on the step with "Step N
  didn't finish — check the Terminal", a Terminal link and Re-check (posts
  `state`). 429, copy mode, 502/503 and timeouts show their messages with the
  Terminal link. No request is ever retried automatically.
- Tool detail: `fetchTool` gets a 10 s timeout. While the call is pending and
  the detail failed, the card retries every 3 s (GET only) and shows the
  fallback with the Terminal link meanwhile.
- Mobile: a typical step (≤ 4 options) fits 390×844 without scrolling.

### Fixtures

The recorded frames land, hand-trimmed to the throwaway prompt and the dialog,
in `answer/testdata/2.1.310/` (vim editor mode, the owner's setup) and
`answer/testdata/2.1.310-normal/` (normal editor mode, named session). The
2.1.293 (`testdata/`) and 2.1.299 sets stay. They are rendered alike, so they
keep running as tab-reader, review-reader and named-session tests. Tests that
only exercised the removed `answer.Plan` whole-wizard replay are retired, and
the PR says so.

### Docs

`docs/reference/chat.md` "Answering from Chat" is rewritten to the step
contract, key sequences, footer rule and fixture sets. The CLAUDE.md routes
table keeps its row (the route is unchanged). The QA `answer` scenario
(`scripts/qa/scenarios/answer.cjs`) drives the wizard.

## Contract map

| Edge | Disposition |
| --- | --- |
| UI `answer.ts` → `POST /answer` | changed: `state`/`step`/`back`/`submit` replace `question` |
| `server/runs_answer.go` handler, `toAnswers` | changed |
| `answer` plans/checks | changed: tab reader, step plans, review reader |
| `typeSteps` | changed: returns the capture that passed the last Expect; no copy-mode probe before a keyless step |
| `awaitStep`/`paneTakesKeys`/`sendAnswerKey` | compatible, reused |
| `choice` kind + `PermissionBar` + `/prompt` | compatible, untouched; tests keep passing |
| Peer forwarding (`fwdAnswer`, 64 KiB cap) | compatible: bodies are forwarded verbatim. An older peer answers 400 `unknown answer kind`, which the card shows with the Terminal link. |
| QA `answer.cjs` scenario | changed |
| `ChatTab` `questionPending`/`answeredHere` | compatible, unchanged |
| `dispatcher_restart.go` (imports `answer`) | compatible: uses `PermissionPrompt` only |

## Invariants kept

`runPane` point-of-use server check; dialog region rule (unindented full-width
rule, 200-line capture, `#{pane_width}`); named-session rule drop; copy-mode
probe before every batch; per-pane lock; fail closed with 409 and nothing typed
on a first-check miss; no user text in tmux argv beyond the existing
`send-keys -l --` path; logs carry pane, kind, step count and status only.

## Non-goals

Codex/cursor/pi answering; permission-prompt changes; queuing concurrent
answers; auto-retrying keys; parsing the active tab from tab-bar highlight
colour.
