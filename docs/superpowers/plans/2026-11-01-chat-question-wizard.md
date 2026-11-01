# Plan: Chat live step-by-step AskUserQuestion wizard (#330)

Spec: `docs/superpowers/specs/2026-11-01-chat-question-wizard.md` (accepted).
Recorded frames: `/home/noams/git/houston/.git/crew/artifacts/feat/330-chat-live-step-by-step-askuserquestion-w/frames/` (`$FRAMES` below).
Orchestration consult: not run. The work is one route contract through three
known layers (`answer` → `server` → UI) along existing seams, so a stronger
decomposition than this plan adds nothing.

## File list

| File | Purpose |
| --- | --- |
| `answer/testdata/2.1.310/*.txt` | new: vim-mode frames (owner setup), copied from `$FRAMES` |
| `answer/testdata/2.1.310-normal/*.txt` | new: normal-mode named-session frames (`nv-*`) |
| `answer/plan.go` | `Question.Header`; `Tabbed`, `Repeats`; `validate` (+ `✔` rule); `PlanStep`, `PlanBack`, `PlanSubmit`; `answer.Plan` removed |
| `answer/frame.go` | tab reader (`ReadView` / `View`), pinned footer rule, review reader, view-equality and landing checks; `tabState` exact checks and `reviewCheck` removed |
| `answer/frame_test.go` | reader tests over the 2.1.310, 2.1.310-normal, 2.1.299, 2.1.299-named and 2.1.293 frames |
| `answer/plan_test.go` | key-sequence tests for step/back/submit (old `Plan` tests retired) |
| `server/runs_answer.go` | kinds `state`/`step`/`back`/`submit`; `question` removed; `typeSteps` returns the final capture |
| `server/runs_answer_test.go` | handler tests on scripted 2.1.310 frame transitions |
| `ui/src/api/answer.ts` (+ `answer.test.ts`) | `readQuestionState`, `answerStep`, `answerBack`, `answerSubmit`; `answerQuestion` removed |
| `ui/src/api/chat.ts` | `fetchTool` 10 s timeout |
| `ui/src/fleet/questionStage.ts` (+ test) | stage ⇄ wire/selection helpers for one question |
| `ui/src/fleet/QuestionWizard.tsx` (+ `QuestionWizard.test.tsx`) | new: step / review / plain screens, polling, per-step errors |
| `ui/src/fleet/QuestionCard.tsx` (+ test) | renders `QuestionWizard` when picking; tool-detail retry; read-only paths unchanged |
| `ui/src/fleet/fleet.css` | wizard styles (progress, chips, nav row, review rows) |
| `scripts/qa/scenarios/answer.cjs` | drive the wizard; new 3-question Back check with pane captures |
| `docs/reference/chat.md` | "Answering from Chat" rewritten to the step flow |

`ChatTab.tsx`, `PermissionBar.tsx`, `/prompt`, `choice`, peer forwarding and
the CLAUDE.md routes table stay unchanged (the contract map in the spec covers
them; their tests run in the gate).

## Interfaces (stable across steps)

```go
// answer
type Question struct { Text, Header string; MultiSelect bool; Options []Option }
type Answer struct { Options []int; Text string } // unchanged

// View is what the pane shows of one AskUserQuestion call.
type View struct {
    Step    int      // active tab index; == len(qs) on review
    Cursor  int      // tab: 1-based row (Other = n+1), 0 = multi Next/Submit row; review: 1 Submit, 2 Cancel
    Options []int    // committed (single/preview: the ✔ row) or ticked (multi) options, ascending
    Text    string   // committed Other text, wrapped lines joined by one space, ✔ removed; "" none
    Review  []string // on review: the text after → per question
    // unexported, compared by the view-equality check: tab-bar boxes, visible
    // Other row text (normalized, committed or not), Other row line count,
    // committed-Other flag.
}
func Tabbed(qs []Question) bool                // len(qs) > 1 || any MultiSelect
func Repeats(qs []Question) bool               // two questions with equal normalized text and option rows
func ReadView(qs []Question, capture string, width int) (View, bool)
func PlanStep(qs []Question, index int, a Answer, v View) ([]Step, error)
func PlanBack(qs []Question, index int, v View) ([]Step, error)
func PlanSubmit(qs []Question, review []string, v View) ([]Step, error)
```

`Step{Expect, Keys}`, `Key{Special|Text}` and `Check` are unchanged. Every plan
starts with `Expect = viewIs(v)`. When `v.Cursor != 1` (tabs) or the cursor is
on Cancel (review), it then sends `Up`×k and checks the same view with the
cursor on row 1 (or Submit). Tabbed `PlanStep` and every `PlanBack` end with a
keyless landing step. A plain `PlanStep` and `PlanSubmit` end with the
submitting keys and nothing after.

Wire (`POST /api/runs/{id}/answer`):

```
{"kind":"state","toolCallId":"…"}                              → 200 state | 404 "no question"
{"kind":"step","toolCallId":"…","index":0,"answer":{"options":[1],"text":"…"}} → 200 state (tabbed) | 204 (plain)
{"kind":"back","toolCallId":"…","index":1}                     → 200 state
{"kind":"submit","toolCallId":"…","review":["Blue","…"]}       → 204
state = {"tabbed","step","count","questions":[{"question","header","multiSelect","preview","options":[{"label","description"}]}],"options"?,"text"?,"review"?}
```

## Steps

Execute rungs: default sonnet (haiku where named); `implement: opus` only on
the tagged steps. Every subagent brief carries: process authority (no
`superpowers:` process skills, no PRs, no `WORKER_PROTOCOL.md`), rule 7 (never
read `.env*`/credentials/keys or print a secret), "never run
`$CREW_LEAD_LAUNCHER` or `lead-launcher`", and "never run tmux against the
live server".

Order: 1 → 2 → 3 → 4 → (5 ∥ 6) → 7 → (8 ∥ 9) → 10 → 11 → 12. Steps 5 and 6
touch disjoint files (`server/` vs `ui/src/api/` + `ui/src/fleet/questionStage*`),
as do 8 and 9 (`scripts/qa/` vs `docs/`). Step 8 needs step 7's selectors.

- [ ] **Step 1: copy the fixtures** (haiku). Copy into `answer/testdata/2.1.310/`
  every `$FRAMES/*.txt` except `nv-*`, `scratch.txt`, `repro-opts.txt`,
  `tab2-q1.txt` and `smulti-other-typed.txt`, keeping the file names. Copy
  `$FRAMES/nv-*.txt` into `answer/testdata/2.1.310-normal/` with the `nv-`
  prefix stripped. Then hand-check:
  `grep -L 'AskUserQuestion' answer/testdata/2.1.310*/*.txt` prints nothing,
  and `grep -c '' answer/testdata/2.1.310*/*.txt | sort -t: -k2 -n | tail -3`
  shows no file over 60 lines (each frame is one throwaway prompt and its
  dialog). Proof: `ls answer/testdata/2.1.310 | wc -l` ≥ 60 and
  `ls answer/testdata/2.1.310-normal | wc -l` ≥ 10.

- [ ] **Step 2: red test for the footer root cause** (sonnet). In
  `answer/frame_test.go`, add `TestQuestionChecksVimFooter`, using today's
  `newSpec(...).check(tabState{...})`. It asserts that a check passes on
  `2.1.310/repro-other-q1` (2-question tab, cursor 3, touched), `2.1.310/plain-other-cursor`
  (plain, cursor 4), `2.1.310/m3-q2-other-cursor` (3-question tab 1, cursor 4,
  checked {0,2}, touched) and `2.1.310/plain-other-typed` (other `darkteal`).
  Proof: `go test ./answer -run TestQuestionChecksVimFooter` FAILS on the
  current tree (record the failing lines for the PR's Testing section). Commit
  nothing yet. Step 3 replaces this test with its `ReadView` form.

- [ ] **Step 3: tab reader, footer and review reader** (implement: opus). In
  `answer/frame.go` implement `View`, `ReadView`, the pinned footer rule
  (spec "Footer": two shapes, editor name `<E>` any run without `·`), the review
  reader, and the view-equality (`viewIs`) check. `ReadView` finds the active
  question by matching the tab text against each `qs[i]` spec (the dialog region
  rule, named-session drop and preview label cut as today). Rows must equal
  `label+desc` or `label+"✔"+desc` exactly, and the Other row is the placeholder
  or text with an optional `✔` at a line end. Committed Other: a `✔` on the
  Other row, or (finding 7) this tab's box ticked, no option `✔` and Other
  text present. Remove `tabState`, `questionSpec.check`/`matches` and
  `reviewCheck` in step 5, once nothing calls them. Until then keep them
  compiling. Add `Header` to `Question`/`ParseQuestions`, plus
  `Tabbed`/`Repeats` in `plan.go`.
  Tests in `answer/frame_test.go` (test-first: write them, see them fail, then
  implement). (a) A table `TestReadView` over named 2.1.310 frames, asserting
  `Step/Cursor/Options/Text/Review`. Rows: `m3-q1` {0,1,[],""};
  `m3-q1-back-left` {0,1,[1],""}; `m3-q1-back-btab` {0,1,[0]};
  `m3-q2-cheese-basil` {1,1,[0,2]}; `m3-q2-other-typed` {1,4,[0,2],"extra pepper"};
  `m3-q2-other-next` {1,0,[0,2],"extra pepper"}; `m3-q3` {2,1,[]};
  `m3-q3-large` {2,2,[]}; `m3-q3-back-from-review` {2,1,[1]};
  `m3-q1-back-other-answered` {0,1,[],"teal"}; `m3-q1-back-blue2` {0,1,[1],""}
  (retained `navy` is not committed); `m3-review` {3,1, review
  ["Red","Cheese, Basil, extra pepper","Large"]}; `v3-review-cancel` {2,2};
  `v2-q1-back-long` {0,1,[],"a fairly long custom colour answer that keeps
  going well past the edge of this narrow pane"} (finding 7: no `✔`, box
  ticked); `plain-q`/`plain-other-typed` (plain, cursor 1/4);
  `smulti-review` {1, review ["Olives, ham"]}; `prev-q-large`; the `2.1.310-normal`
  frames `q1`, `q1-other-typed`, `q1-back`, `review`, with the named-session
  rule dropped. (b) `TestReadViewRefuses`: `stale-negative`,
  `scrollback-negative`, `plain-done`, `perm-bash` (legacy dir) and a
  `v2-q1-down-other` copy with the footer rewritten to `-- NORMAL -- · …`
  return `ok=false`. (c) Every frame under `testdata/2.1.299`,
  `testdata/2.1.299-named`, `testdata/` (2.1.293) and `testdata/2.1.310*` whose
  name is a question tab or review reads `ok=true` for its known questions (keep
  the existing per-dir fixture loops, re-pointed at `ReadView`). (d) Replace
  step 2's test with `TestReadViewVimFooter` over the same four frames. Proof:
  `go test ./answer` passes, and `go vet ./answer` is clean.

- [ ] **Step 4: step/back/submit plans** (implement: opus). In `answer/plan.go`
  implement `PlanStep`, `PlanBack`, `PlanSubmit` with the spec's key sequences:
  the cursor prelude; single digit; preview digit → cursor check → `Enter`;
  single Other `Down`×n → check → `C-u`×(2·lines+1) only when the field holds
  text → placeholder check → text → text check → `Enter`; multi toggles for
  (ticked ⊕ wanted) one digit each → check → Down path → Next/Submit check →
  `Enter`; back `Left`; submit (`Up` if on Cancel) → `1`. Landing checks: the
  next tab with cursor 1 and this tab's box ticked, or review, or for back tab
  `index-1` cursor 1. `validate` gains: text may not contain `✔`. Index range
  and `Repeats` are checked by the caller. `PlanStep`/`PlanBack` return an
  error when `v.Step != index` (back: `index`, or `count` on review), and the
  server checks that before planning (409 `prompt changed`, nothing typed).
  View equality (`viewIs`) compares every field except the Other row's line
  count, which changes with focus (finding 9). The C-u count comes from the
  view the plan was built from. Keep `Plan` and the old checks compiling: the
  server still calls `Plan` until step 5, which deletes them. Tests in `answer/plan_test.go` (test-first): a key-log
  table spelled like the old tests (`"2"`, `"Down"`, `"C-u"`, `"text:teal"`,
  `"Enter"`, `"Left"`, `"Up"`), each row also replaying its plan against the
  recorded frames with a tiny in-test executor (first Expect on the start
  frame, each later Expect on the frame the recording shows after the
  previous batch). Rows:
  - single option on fresh `m3-q1` → `["2"]`, landing passes on `m3-q2-after-blue`;
  - re-selection after Back: from `m3-q1-back-left` (Blue ✔) choose Red →
    `["1"]`, landing on `m3-q2-after-red`, and from `m3-q1-back-btab` the view
    shows exactly `[0]` (one selection, not two);
  - single Other with retained text: from `m3-q1-back-other-answered` (teal ✔)
    answer `navy` → `["Down","Down", <check m3-q1-down-to-other-answered>,
    "C-u","C-u","C-u", <check m3-q1-other-cleared>, "text:navy",
    <check m3-q1-other-navy>, "Enter"]`, landing on `m3-q2-after-navy`. Assert
    the Other digit `"3"` never appears;
  - single Other fresh vim: `v2-q1` → `Down Down` → `v2-q1-down-other` → text
    (the long string) → `v2-q1-other-long` → `Enter`. No landing frame was
    recorded for this row, so assert its keys and leave the landing check to
    the m3 rows;
  - plain preview: `prev-q` choose Large → `["2", check prev-q-large, "Enter"]`, no landing;
  - wrapped Other cleared: `v2-q1-back-long` (lines 2) answer `teal` sends 5
    `C-u` and its placeholder check passes on `v2-q1-long-cleared2`;
  - multi toggles diff: from `m3-q2-reenter-toggled` (ticked {0,2}) want {2}
    → `["1"]` only;
  - multi Other: `m3-q2-cheese-basil` want {0,2}+"extra pepper" →
    `["Down"×3, check m3-q2-other-cursor, "text:extra pepper", check
    m3-q2-other-typed, "Down", check m3-q2-other-next, "Enter"]`, landing `m3-q3`;
  - multi retype after Back: `m3-q2-back-from-q3` want {0,2}+"no onions" →
    `Down`×3, `C-u`×3, `text:no onions`, `Down`, `Enter`. Checks pass on
    `m3-q2-back-other-cursor`, `m3-q2-other-cu`, `m3-q2-other-retyped`;
  - multi empty Other: `v2-q2-olives` → `Down`×4 → check `v2-q2-next-empty-other` → `Enter`;
  - preview: `m3-q3` choose Large → `["2", check m3-q3-large, "Enter"]` landing `m3-review`;
  - cursor prelude: `v3-q1-up-from-other` (cursor 2) step option 1 → `["Up", check v3-q1-up2, "1"]`;
  - back: `m3-q2-after-blue` index 1 → `["Left"]` landing `m3-q1-back-left`;
    from review `m3-review` index 3 → landing `m3-q3-back-from-review`;
  - submit: `v3-review-cancel` → `["Up", check v3-review-up, "1"]`; submit with
    a review string that differs → the first Expect fails;
  - plain: `plain-q` option 2 → `["2"]` (no landing); plain Other →
    `Down`×4, check `plain-other-cursor`, text, check `plain-other-typed`, `Enter`;
  - mismatched tab: `PlanStep(index 1)` built from a view, then the first
    Expect on `m3-q1` → false.
  Proof: `go test ./answer` passes; `golangci-lint run ./answer/...` is clean.

- [ ] **Step 5: server kinds** (implement: opus). In `server/runs_answer.go`:
  extend `answerRequest` with `Index int`, `Answer *answerEntry`, and `Review
  []string`; drop `Answers`. Add the kinds `state`, `step`, `back`, `submit`
  per the spec table, with a shared `questionCall(session, toolCallID)`
  (the transcript ladder plus 422 `questions repeat`). Factor `toAnswers`
  into a per-entry `toAnswer(q, e)` that adds the `✔` rule, and validate
  `review` (exact count, ≤ 2000 runes each, no `Cc`). `typeSteps` returns
  `(answerOutcome, capture string, width int)` and skips `paneTakesKeys` for a
  keyless step. `state` captures without the lock and logs at Debug (Warn on a
  capture failure). `step`/`back`/`submit` take the lock and log at Info as
  today (kind, step count, status). Build the 200 body from `ReadView` on the
  returned capture. Questions JSON omits previews. Delete the `question` kind,
  and in `answer/` delete `Plan`, `tabState`, `questionSpec.check`/`matches`
  and `reviewCheck` (and their tests), now that nothing calls them.
  Tests in `server/runs_answer_test.go` (test-first), rewritten over `2.1.310`
  frames with `screen` transitions taken from the recordings:
  `TestRunAnswerState` (tab, review, plain, 404 on `plain-done`, Debug-only);
  `TestRunAnswerStepSingle` (204 vs 200 body `{"step":1,...}`);
  `TestRunAnswerStepOtherLands` (single, multi and plain Other under vim
  frames; key log includes the text; 200/204); `TestRunAnswerBackThenChange`
  (back from tab 1 → state shows `options:[1]`; then step Red → landing; the
  final view of tab 0 reads exactly `[0]`); `TestRunAnswerStepWrongTab` (index
  1 while the pane shows `m3-q1` → 409 `prompt changed`, `keyLog` empty);
  `TestRunAnswerStepPartial` (the screen stalls after `Down Down` → 409
  `{"partial":true}`); `TestRunAnswerSubmit` (204 with `1`; a review mismatch →
  409 with nothing typed); `TestRunAnswerStepNamedSession` (`2.1.310-normal`).
  Port the generic tests (busy 429, releases lock, send failure 500/502,
  capture failure 503, copy mode 409, bad bodies 400 incl. `✔` text and an
  unknown kind, too large 413, refusals incl. `questions repeat` 422 and
  `already answered` 409, non-Claude 422) to `step`. Keep `choice`/`prompt`
  tests untouched. Proof: `go test ./server -run 'RunAnswer|RunPrompt'` and
  `go test -race ./server -run RunAnswer` pass.

- [ ] **Step 6: UI API + stage helpers** (sonnet). `ui/src/api/answer.ts`: types
  `StateQuestion`, `QuestionState`, and `readQuestionState(runId, callId)`
  (POST state; 404 → null; other failures throw; 10 s timeout),
  `answerStep(runId, callId, index, answer)` → `{ok:true, state?}` (200 parses
  state; 204 → no state), `answerBack`, `answerSubmit`. Keep the 409/copy-mode/
  partial/timeout mapping of `post`. Remove `answerQuestion`. `ui/src/api/chat.ts`:
  `fetchTool` gets `AbortSignal.timeout(10000)`. `questionStage.ts`:
  `stageFromSelection(options, text)`, `toAnswer(stage)` (one question), and
  `isStageComplete` exported. Leave `answerQuestion` and `toWire` in place:
  `QuestionCard.tsx` imports them until step 7, which deletes them. Tests (test-first) in
  `answer.test.ts` and `questionStage.test.ts` for each mapping. Proof:
  `cd ui && npx vitest run src/api/answer.test.ts src/fleet/questionStage.test.ts`
  passes.

- [ ] **Step 7: wizard component** (sonnet). New `ui/src/fleet/QuestionWizard.tsx`,
  rendered by `QuestionCard` in place of the picker list when `picking`. Props:
  `runId`, `call`, `fallbackQuestions` (parsed from the tool detail, may be
  empty), `onLayout`. On mount and every 2 s while idle and
  `document.visibilityState === 'visible'`, call `readQuestionState`. The
  screens: **step** (progress `N of M`, header chips with the current one
  marked, question text, `PickerBlock` for that one question, a bottom nav row
  with Back (hidden on step 0) and Next, ≥ 44 px, Next disabled until the stage
  is complete); **review** (one row per question: header/question and the
  pane's review string, tap → sequential `answerBack` until that step; Submit
  sends `answerSubmit(review shown)`); **plain** (the existing single-question
  picker layout and the "Send answer" button `.chat-question-send-btn`, posting
  `answerStep(0)`). Selection seeding follows the spec (pane selection, or the
  client's exact text when it normalizes equal; a poll never overwrites a step
  the user has touched). Errors: moved (non-partial 409) → notice "The terminal
  moved on — nothing was typed" and an immediate state read; partial → "Step N
  didn't finish — check the Terminal", a Terminal link and a Re-check button
  (`readQuestionState`); other errors show their message with the Terminal
  link. No automatic re-send, ever. Sent (204 from submit or plain) → "Sent —
  waiting for Claude" until the call status changes. Move `PickerBlock` into an
  exported helper both files use. Delete `answerQuestion` (`answer.ts`) and
  `toWire` (`questionStage.ts`) with their tests. In `QuestionCard.tsx`: retry `fetchTool` every
  3 s while `!done` and the last fetch failed (clear on unmount). While the
  detail is missing but the call is pending and answerable, still render the
  wizard (it renders from state). Styles in `fleet.css` under
  `.chat-question-wizard*`, so a typical step fits 390×844. Tests (test-first,
  `QuestionWizard.test.tsx` with `fetch` mocked like `QuestionCard.test.tsx`):
  step navigation (Next posts `step` with the staged answer and moves to the
  returned step); Back posts `back` and shows the pane-reported selection
  (`options:[1]` ticks Blue); review lists the pane strings, and tapping row 1
  of 3 posts back twice and lands on step 1; a per-step partial error shows the
  step name, a Terminal link and Re-check, with no second POST; a moved 409
  triggers one state read; a single-question plain dialog renders the old
  picker with "Send answer" and posts `step` index 0; polling stops while a
  request is in flight. In `QuestionCard.test.tsx`: `fetchTool` rejecting
  once then resolving shows the card (fake timers); `inputOmitted` detail plus
  a state answer renders the wizard. Proof: `cd ui && npx vitest run src/fleet`
  passes, and `npx tsc -b` and `npx eslint .` are clean.

- [ ] **Step 8: QA scenario** (sonnet). In `scripts/qa/scenarios/answer.cjs`:
  `ask-single` keeps working (plain screen, `.chat-question-send-btn`). Rewrite
  the multi check to the wizard (Next per step, review, Submit). Add
  `ask-wizard`: prompt the 3-question mixed call (single Color Red/Blue, multi
  Toppings Cheese/Olives/Basil, single Size Small/Large). Step 1 picks Blue →
  Next. Step 2 ticks Cheese and types Other `extra pepper` → Next. Then Back
  (to step 2) and Back (to step 1), and change Blue → Red → Next. Step 2
  (pane-reported selection must show Cheese + Other text) → Next. Step 3 picks
  Large → Next. Review → Submit. Assert the tool_result maps Color→Red,
  Toppings→Cheese + extra pepper, Size→Large. Before and after every
  step/back/submit, write `ctx.capture(pane, {lines: 60})` to
  `$S/shots/wizard-<n>-<before|after>.txt`. Add `ask-wizard` to both
  fixed-id skip lists (the `ask-single, ask-multi, permission, stale-409`
  arrays). Proof: `node --check
  scripts/qa/scenarios/answer.cjs` passes (the live run is step 11).

- [ ] **Step 9: docs** (sonnet). Rewrite `docs/reference/chat.md` "Answering
  from Chat" (Question picker, the `POST …/answer` bullet, Executor key
  sequences, dialog region rule, UI result handling, Drift) to the spec's
  contract, keys, footer shapes, cursor prelude, polling and fixture sets
  (`2.1.310/`, `2.1.310-normal/`). Keep the permission-prompt bullets and
  `answeredHere`. Proof: `rg -n '"kind":"question"|Send answer|answer\.Plan'
  docs/reference/chat.md` finds only the plain-dialog "Send answer" mention.

- [ ] **Step 10: deterministic gate + counterfactuals** (lead). Run the CI list
  (acceptance below). Counterfactuals in a scratch copy
  (`git worktree add $SCRATCH HEAD` after committing): (i) restore the old
  `isTail` body (footer must end `Esc to cancel`), then
  `go test ./answer -run 'TestReadViewVimFooter|TestPlanStep'` and
  `go test ./server -run TestRunAnswerStepOtherLands` must each FAIL; (ii) change the single Other plan to the Other digit, and the
  "Other digit never appears" row must FAIL; (iii) remove the `fetchTool`
  retry, and the QuestionCard retry test must FAIL. Record each failing
  assertion line.

- [ ] **Step 11: end-to-end** (lead). In the isolated QA env (`/tmp/hqa.*`,
  houston on 17470–17499, never 9090): rebuild houston from the branch
  (`scripts/qa/houston.sh S restart` after `just ui-build` + `go build -o
  $S/houston .`). Run `SCENARIOS=answer node scripts/qa/qa.cjs` against a
  vim-mode claude 2.1.310 window. `ask-wizard`, `ask-single` and the multi
  check must pass. Attach the `wizard-*` captures to the PR ledger.

- [ ] **Step 12: screenshots** (lead). Playwright Chromium (`$S/pw`
  playwright-core), 390×844, `hasTouch`, `isMobile`, with `/api/mode`,
  `/api/runs*`, the chat page, the tool route and `POST …/answer` (state) mocked
  via `page.route`. Shoot the step screen (3-question wizard, step 2 multi) and
  the review screen. Check that the step screen has no vertical scroll inside
  the viewport (`document.scrollingElement.scrollHeight <= 844`, or the card's
  bounding box fits). WebKit and iOS Safari are listed as unverified.

## Acceptance

- [ ] 2.1.310 fixtures (single, multi, preview tabs, Other on single/multi/plain, Back tab 2→1, re-selection, review, plain): step 1 listing + step 3/4 tests read them. 2.1.299 fixtures still pass (step 3c). Retired: the whole-wizard `Plan` tests (reason: the `question` kind is gone).
- [ ] Go tests drive step/back/submit on those fixtures, incl. a mismatched tab → 409 with nothing typed, Other text landing verified, and Back then a change leaving exactly the new selection: steps 4–5, `go test ./answer ./server`.
- [ ] Each observed failure has a named root cause (spec table) and a test (steps 3d/4/5/7), with counterfactual red evidence from step 10.
- [ ] UI tests: step navigation, Back restores the pane selection, review jump, per-step error with Terminal link, plain dialog unchanged: step 7, `npm test`.
- [ ] E2E 3-question mixed wizard with Back + change on real 2.1.310 in the isolated tmux: step 11 captures.
- [ ] Mobile Chromium screenshots of step + review at 390×844 with `hasTouch`; iOS Safari unverified: step 12.
- [ ] `docs/reference/chat.md` updated: step 9.
- [ ] CI: `nix build --no-link -L .#default`; `nix develop -c just build`; `nix develop -c go vet ./...`; `nix develop -c go test ./...`; `nix develop -c gofmt -l .` (empty); `nix develop -c golangci-lint run`; `nix develop -c nilaway -include-pkgs=github.com/noamsto/houston ./...`; in `ui/`: `npx eslint .`, `npx tsc -b`, `npm test`; `nix develop -c go test -race ./...`; `nix develop -c just mod-check`: step 10.
