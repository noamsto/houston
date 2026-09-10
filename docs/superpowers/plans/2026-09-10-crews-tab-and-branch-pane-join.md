# Plan — Crews tab: crew grouping, branch→pane join, answering a blocked worker

Issue #33. Spec: `docs/superpowers/specs/2026-09-10-crews-tab.md`.

The work splits into eight components, referred to below as C1-C8: the crew-ref
model (C1), registry composition (C2), crew bus semantics (C3), the branch-pane
join (C4), the reply endpoint (C5), the run card (C6), the Crews tab (C7), and
the integration gate (C8). `I1`-`I9` are the interfaces held stable across that
split, each pinned inline in the step that owns it.

Order: `C1 ∥ C3 ∥ C5 ∥ C6`, then `C1 → C2 → C4`, `C3 → C4`, `C6 → C7`, and
`{C4, C5, C7} → C8`.

Baseline before any step: Go build, vet and tests green; 61 UI tests green; the
"before" evidence for criterion 1 already captured.

Revised once after plan-critic. Six blocking findings, all verified in the code
and all adopted: three sets of existing tests the plan changed in effect but never
named, one step whose red-then-green proof cannot exist, and two acceptance
criteria whose verification could not fail. Two components' owned criteria move as
a result — the registry half of criterion 10 becomes C2's, and criterion 20 gains
a test step in C7. One boundary is widened deliberately and said so in the open:
C1 also touches `runs/tmuxsource_test.go`, because C1's own change is what turns
that test red.

## C1 — crew-ref model and the worker's colour

- [ ] **Step 1: harvest `@crew_color`.** In `tmux/options.go` add `#{@crew_color}`
      to `windowOptionsFormat`, a `CrewColor` field to `WindowOptions`, and move
      `ParseWindowOptions`'s field count 11 → 12. Update the three existing cases
      in `tmux/options_test.go` to the new width and assert `CrewColor` on the
      enriched row. Append `#{@crew_color}` **last**, as index 11, so every
      existing field index in `ParseWindowOptions` stays put and only the trailing
      separator changes in the four fixture lines.
- [ ] **Step 2: `tmuxColorToHex`.** New `runs/color.go`. `colour168` → `#d75f87`
      by the standard xterm cube (16–231) and greyscale (232–255) formulas;
      `#rrggbb` passes through; `colour0`–`colour15`, a colour name, `default`,
      empty and anything unparseable → `""`. Comment states why 0–15 are excluded
      (terminal-theme-dependent, and crew's own palette starts at `colour25`) —
      that reason must be true, not plausible. `runs/color_test.go` covers every
      branch including `colour16`, `colour231`, `colour232`, `colour255`.
- [ ] **Step 3: split `CrewRef`.** In `runs/run.go` apply `I1`'s struct verbatim,
      including the doc comments saying which source owns each field and that
      `Name` keeps its non-`omitempty` tag. In `runs/tmuxsource.go` change only the
      `r.Crew = …` assignment to `&CrewRef{Codename: w.CrewName, Color:
      tmuxColorToHex(w.CrewColor)}` — `Name` is no longer tmux's to set. Keep the
      existing `if w.CrewName != ""` guard, so a window with a colour but no
      codename still publishes no `Crew` — lazytmux writes the pair together, and
      that guard is what keeps `TestDeltasFromTmuxOmitsAbsentEnrichment` true.
- [ ] **Step 3a: fix the test this breaks.** `runs/tmuxsource_test.go:42` asserts
      `Crew.Name == "mauve"`, which is exactly the meaning Step 3 removes. Add
      `CrewColor: "colour168"` to the window fixture and assert
      `Crew.Codename == "mauve"`, `Crew.Name == ""` and `Crew.Color == "#d75f87"`.
      **This widens C1's boundary** to `runs/tmuxsource_test.go`, which its
      `may touch` list omits: C1's own change is what turns the test red, so no
      other component can own the fix without inheriting a broken build.
- [ ] **Step 4: verify C1.** `go build ./...`, `go vet`, `go test ./runs/... ./tmux/...`.

## C3 — what the bus says about a branch

- [ ] **Step 5: make reply records readable.** In `runs/crewsource.go` change
      `crewRecord.Body` to `json.RawMessage` and decode it into the status struct
      only when `Kind == "status"`; add `To string \`json:"to"\``. Verified live:
      a `msg` body is a JSON string while a status body is an object, so today's
      struct fails to unmarshal every reply line and `deltasFromCrewLog` skips it
      whole — which is why replies are invisible.
      **This change is not observable on its own.** A dispatcher `msg` carries
      `from: "dispatcher:…"`, so `branchFromWorker` returns `""` and
      `deltasFromCrewLog` skips the record after the fix exactly as it did before;
      the returned map is byte-identical. Do not try to write a red test here. The
      first observable consequence is Step 7, and that is where the red-then-green
      demonstration belongs.
- [ ] **Step 6: a detailless `blocked` still carries a question.** Add a package
      const for the wording (houston's own description, never a quote) and set it
      when a `blocked` status has an empty detail, with `Via: "crew"`. Test both
      the detailed and detailless cases.
- [ ] **Step 7: retire an answered question.** Per `I5`: if a branch's latest
      status is `blocked` at `T` and a `kind: "msg"` record from a `dispatcher:`
      sender whose `branchFromWorker(To)` is that branch has `TS > T`, publish
      `State == ""` and `Question == nil`, keeping `Branch`, `Crew` and `Agent`.
      Order within the log is by `ts`, not by line order. Tests: answered,
      unanswered, reply older than the block, reply addressed to a different
      branch, and a reply from a non-dispatcher sender.
      The early guard is in the way and must be restructured: today a record whose
      branch resolves to neither `rec.Branch` nor `branchFromWorker(rec.From)` is
      `continue`d, which drops a dispatcher reply even after the `Body` fix. A
      `kind: "msg"` record from a `dispatcher:` sender resolves its branch from
      `To` instead.
      This step carries the proof for Step 5 as well:
      `TestDeltasFromCrewLogRetiresAnAnsweredQuestion` must be red before the
      `Body`/`To` change (composing `blocked`, question intact) and green after.
      A bus holds a single shared `events.jsonl` rather than one file per crew, so
      a reply and its blocking status are always in the same file and `scanRoots`'
      per-file fold cannot separate them. Confirm, but do not be surprised.
- [ ] **Step 8: verify C3.** `go test ./runs/...`; then prove Steps 6 and 7 bite by
      reverting each in turn and watching its test go red.

## C5 — the reply endpoint (security-sensitive)

- [ ] **Step 9: the runner seam** (implement: opus). New `server/runs_reply.go`
      with `replyExec`, `replyResult` and `replyRunner` exactly as `I4` fixes them,
      plus the real `execCrewReply`: `exec.CommandContext` with argv
      `{"crew","reply","worker:"+branch,text}` — an argv slice, never a shell, and
      **no `--`**, because `crew reply` takes its operands positionally and would
      consume `--` as the recipient (verified). `Dir` is the run's `Worktree`;
      `Env` is `os.Environ()` plus `CREW_ID=<Crew.Name>`. Add the `replyRunner`
      field to `Server` and default it in `New`.
- [ ] **Step 10: the handler** (implement: opus). `handleRunReply` resolves the run
      from `s.runs.Snapshot()` by `{id}` — never from a client-supplied branch or
      path — then checks every precondition in `I3`'s table and returns 4xx
      **before** touching the runner: unknown id, no `Crew`/`Branch`/`Worktree`,
      `Question` nil or `Via != "crew"`, empty text. Body is bounded by
      `http.MaxBytesReader` and text by `maxReplyText`; the command by
      `replyTimeout`. Map outcomes to 204/400/404/409/413/502/504 exactly as `I3`
      says, with crew's stderr verbatim on 409. Log run id, branch and outcome.
- [ ] **Step 11: register it** (implement: opus). One line in `server/server.go`:
      `apiMux.HandleFunc("POST /api/runs/{id}/reply", s.handleRunReply)` — on
      `apiMux`, so it inherits the token, origin and `Host` gates unchanged, and a
      `GET` is refused by the mux before reaching the handler.
- [ ] **Step 12: prove the gates hold** (implement: opus). Through `s.Handler()`,
      not the handler directly: unauthenticated → 401, cross-origin → 403, rebound
      `Host` → 421, `GET` → not 2xx — each asserting the stub recorded **zero**
      invocations. This is the falsifiable question that caught a critical hole
      last time: is there a residual path to a state change without a valid token?
- [ ] **Step 13: prove the argv is literal** (implement: opus). A stub capturing
      `replyExec`: an answer containing `; rm -rf /`, backticks, `$(…)` and a
      leading dash arrives as exactly one argv element, with the recipient still
      `worker:<branch>`. Plus each outcome row of `I3` and the precondition
      refusals, all asserting call counts. Drive these through `s.Handler()`, or
      call `req.SetPathValue("id", …)`: a request built by `httptest.NewRequest`
      and handed straight to the handler has no path value, so `PathValue("id")`
      is `""` and every case returns 404 for the wrong reason.
- [ ] **Step 14: end to end against the real `crew`** (implement: opus). New
      `server/runs_reply_e2e_test.go`, skipping with an explicit message when
      `crew` is not on `PATH`. A scratch git repo with its own bus and a live
      blocked worker, driven through the handler with the real runner: the answer
      lands on that bus addressed to that worker and no other bus is touched. A
      second case with a terminal session returns 409 carrying crew's own refusal
      text. This is the only test that can falsify the working-directory and
      `CREW_ID` choices.

## C2 — registry composition

- [ ] **Step 15: field-wise `CrewRef` merge.** In `mergeInto`, allocate `dst.Crew`
      on the first non-nil `src.Crew` and copy `Name`, `Codename`, `Color`, `Tier`
      individually when non-empty. Every other pointer ref stays wholesale. Update
      `TestPointerRefsReplaceWholesale`, whose comment names `Crew` as
      wholesale-owned — that stops being true and the comment must not outlive it.
- [ ] **Step 16: signature fields.** Add `Crew.Codename`, `Crew.Color` and
      `Question.Via` to `runSignature`. Without this a change to any of them never
      reaches a subscriber — the defect class the state-of-play records for `Caps`.
- [ ] **Step 17: the `Question ⇒ blocked` invariant.** In `composeLocked`, after
      merging: a non-nil `Question` forces `State = StateBlocked`. Derived at
      composition time in one place, exactly as `deriveCaps` is. Precedence is
      untouched — `TestCrewBeatsTmuxButNotHooks` still passes, because its crew
      layer carries no `Question`, and the comment must say so.
- [ ] **Step 18: the `crew/` id.** `idFor` gains the `crew/` case from `I2` and
      drops `branch/`, which no source emits after C4. The test that actually goes
      red is `TestIdForIsURLPathSafe` (`runs/registry_test.go:194`), whose table
      asserts `idFor("branch/fix/412") == "branch-" + base64url("fix/412")`.
      Replace that row with
      `{"crew/" + busDir + "/fix/412", "crew-" + base64.RawURLEncoding.EncodeToString([]byte(busDir + "/fix/412"))}`,
      keeping the `%307` and `claude/` rows and the path-safety assertion.
      `TestCapsReplyRequiresLayerNotJustFlag` (which keys on `branch/fix/1`) and
      `TestPointerRefsReplaceWholesale` (whose comment calls `Crew` wholesale-owned)
      both still pass — they are comment and key hygiene, not failures, and are
      updated so neither outlives the truth it states.
- [ ] **Step 18a: the composition half of criterion 10.** In
      `runs/registry_test.go`, compose tmux(`%1`, agent + running) + hooks(`%1`,
      tool-running) + crew(`%1`, `State: ""`, `Question: nil`) and assert the
      snapshot run is `running` with no question; contrast with the same layers
      where the crew layer still carries its question, which must compose as
      `blocked`. This is the half that shows the badge actually goes dark once a
      question is answered — the regression R5 exists to prevent — and C3 cannot
      own it, because its boundaries forbid `runs/registry.go`.
- [ ] **Step 19: prove Steps 15–17 bite.** Revert each in turn and watch its test go
      red; a test that passes against the bug proves nothing.

## C4 — the branch↔pane join (widest blast radius)

- [ ] **Step 20: share the tmux index** (implement: opus). Extract
      `deltasFromTmux`'s inline `byTarget` map into `windowsByTarget` per `I6` and
      use it in both sources. Narrow `CrewSource.client` from `*tmux.Client` to the
      existing `lister` interface — `*tmux.Client` still satisfies it, so
      `server.go`'s call site does not change — which is what makes the join
      fakeable in tests.
- [ ] **Step 21: the pure resolution function** (implement: opus). `resolvePane`
      per `I6`, returning the pane id and the candidate count. A candidate is a pane
      whose window's `@branch` matches, whose window's `@git_root` maps to the same
      bus, and whose `@claude_status` is non-empty — that last clause is
      `TmuxSource`'s own test for an agent pane, and without it a crew layer would
      promote a shell into a listed run. Exactly one candidate joins. Tests: one,
      zero, two, a shell-only window, and two buses sharing a branch name.
- [ ] **Step 22: group the scan by bus** (implement: opus). `scanRoots` returns
      `busDir → branch → Run` instead of folding every root into one map, so two
      repos holding a `main` record no longer collide. `scan` builds the window and
      pane lists once and passes them down; a tmux error on **either** query skips
      the whole tick, as today.
- [ ] **Step 23: publish under the resolved key and carry `Worktree`**
      (implement: opus). For each `(bus, branch)`: key on the pane id when exactly
      one candidate exists, otherwise `crew/<bus>/<branch>`; `Worktree` is the
      lexicographically smallest `@git_root` among windows on that branch in that
      bus, or `""`. No fallback root when no window matches — an unmatched branch
      is refused a reply rather than handed another worker's checkout, which since
      `WORKER_TASK.md` outranks `CREW_ID` (measured) would execute under the wrong
      crew id. `slog.Debug` records bus, branch and candidate count so the
      no-join path is observable. `tickCrewDeltas` keeps emitting all updates
      before all `Gone`s; add a test pinning that order and the resulting
      `Removed` payload.
- [ ] **Step 23a: migrate the five existing crewsource tests** (implement: opus).
      These break on signature, not assertion, so the package test build dies until
      they move:
      - `TestScanFindsBusFromInsideAWorktree:96` calls `s.scanRoots([]string{wt})`
        and indexes `got["feat/x"]`. `scanRoots` now returns `busDir → branch → Run`,
        so index `got[busDir]["feat/x"]` and assert the bus key is the main
        checkout's `crewDir`. This is a **compile error** first, not a red assertion.
      - `TestTickCrewDeltasEvictsFinishedRunsAfterGrace:107`,
        `…KeepsFreshlyFinishedRuns:136`, `…NeverEvictsAQuietBlockedRun:152` and
        `…KeepsADispatchedRunWithNoStatusYet:168` key `current` by bare branch
        and expect `branch/fix/412`. `tickCrewDeltas` now receives final registry
        keys, so key the fixtures by the resolved key — `%307` for a joined case,
        `crew/<bus>/fix/412` for an unjoined one — and assert the `Gone` key comes
        back verbatim. Only the `scanRoots` test is a compile error (and it has a
        second one: `keysOf(got)` at `:100` takes `map[string]Run`); the tick tests
        break on assertions, and the eviction one may pass unchanged. Migrate all
        five anyway, but do not use "the build is red" as the signal for the four.
- [ ] **Step 24: verify C4.** `go test ./runs/...`. Confirm criterion 5 by
      asserting a shell-only pane never becomes a listed run; criterion 11 by
      asserting `Worktree` equals the matching window's `@git_root` on a joined key
      and `""` with no matching window, since that string becomes a command's
      working directory; and criterion 3's second half by asserting two
      `crew/<bus>/main` layers from different buses coexist as two runs in
      `Registry.Snapshot()`.

## C6 — the card shows the worker

- [ ] **Step 25: TS mirror.** Add `codename` to `CrewRef` in `ui/src/api/runs.ts`
      per `I1`, with the comment that `name` is `''` — not `undefined` — when only
      tmux knows the run.
- [ ] **Step 26: `RunCard`.** Show `crew.codename` in place of today's crew-id
      chip, add a tier chip when `crew.tier` is set, and set
      `style={{ ['--run-accent']: run.crew.color }}` only when `crew.color` is
      non-empty, written once, as
      `style={{ '--run-accent': run.crew.color } as React.CSSProperties}` —
      `React.CSSProperties` has no index signature, so an unasserted custom
      property fails `tsc -b`, and the literal `--run-accent` must survive into the
      bundle for Step 33's grep. In `fleet.css` add `.run-chip.tier`,
      `.run-chip.codename` and a `.run-card` rule consuming `var(--run-accent, …)`
      — those two class literals are what Step 33 greps for, so spell them exactly.
      An accented card that is also `.attention` must still show the attention
      border; the accent may not replace it. New `RunCard.test.tsx` covers all four.
      While here, note in `ui/src/api/runs.ts` that `RunState` can now be `''` on
      the wire, since R5 makes a crew layer publish no state opinion; runtime
      already tolerates it through `var(--state-${run.state}, var(--text-faint))`.
      That comment is a second region of `ui/src/api/runs.ts`, whose C6 boundary
      names the `CrewRef` interface only; it cannot collide with C7's `replyRun`
      region, and it is called out here rather than made silently.

## C7 — the Crews tab

- [ ] **Step 27: the client.** `replyRun` and `ReplyOutcome` in
      `ui/src/api/runs.ts` per `I3`, keeping `refused` (the worker's own words) and
      `failed` (houston's fault) distinct — collapsing them is what makes
      "surface the failure honestly" untestable.
- [ ] **Step 28: `ReplyComposer`.** A sibling of the card, never inside it —
      `RunCard` is a `<button>`, so a nested field would be invalid HTML and every
      keystroke would bubble to `onOpen`. Disabled while in flight, cleared only on
      success; on 409 it shows crew's refusal verbatim. Its root class is
      `.crew-reply` and its outcome line `.crew-reply-status`, both spelled exactly
      as Step 33 greps for them. After a success it says the
      answer is waiting to be picked up, not that the worker resumed. Tests cover
      the double-tap guard and each outcome.
- [ ] **Step 29: `CrewsView`.** Group by `Crew.Name`; a run with no crew or an
      empty crew id appears in no group and is not shown, with an empty state
      saying so. Group header: shortened crew id with the full id as `title`, member
      count, blocked count. Members render through `RunCard`, sorted blocked-first
      then most recent; crews sorted by any-blocked then most recent member. A
      composer renders only for `question.via === 'crew'`. Root class `.crews`,
      group header `.crews-group` — again the literals Step 33 greps for.
- [ ] **Step 29a: `CrewsView.test.tsx`.** Criterion 20 otherwise ships with a
      verification that cannot fail, since `npx vitest run` passes trivially when
      the file does not exist. Four cases: (a) two runs with different `crew.name`
      render two `.crews-group` headers with the right member and blocked counts;
      (b) a run with no `crew`, and one with `crew.name === ''`, appear in no group,
      and the empty state shows when they are the only runs; (c) a member with
      `crew.tier` renders `.run-chip.tier` and one with a `pr` renders the PR chip;
      (d) a composer renders for `question.via === 'crew'` and not for
      `via === 'pane'`.
- [ ] **Step 30: wire it into `Shell.tsx`.** Narrow `PLACEHOLDER` to
      `Record<Exclude<Tab, 'fleet' | 'crews'>, string>`, drop its `crews` entry,
      render `CrewsView` inside `<div hidden={tab !== 'crews'}>` so a half-typed
      answer survives a tab switch, and leave the placeholder branch for the
      remaining two tabs, and pass `CrewsView` the same
      `onOpen={(r) => { window.location.hash = `#/fleet/${r.id}/activity` }}`
      handler `FleetView` gets, or every crew card is inert. Nothing else.
- [ ] **Step 31: verify C6 + C7.** `npx tsc -b`, `npx eslint .`, `npx vitest run`.

## C8 — the proofs no component can give

- [ ] **Step 32: full gate.** Go build, vet, test; UI typecheck, lint, tests;
      `gofmt -l` clean on touched files.
- [ ] **Step 33: bundle greps.** `npm run build`, then grep the built CSS for a
      declaration of each of `.crews`, `.crews-group`, `.crew-reply`,
      `.crew-reply-status`, `.run-chip.tier`, `.run-chip.codename`, and the built JS
      for the literal `--run-accent`. A passing build proves nothing here — the
      whole Mocha token file once shipped unimported behind a green build.
- [ ] **Step 34: live before/after.** Run the built binary against the live tmux
      and crew bus on a spare loopback port with a scratch state dir. Confirm each
      dispatcher worker is now **one** run carrying both the crew question and the
      `Tmux` ref, that `crew.color` is non-empty, and record the per-branch
      candidate counts. Compare against the captured "before".
- [ ] **Step 35: PR body.** Security section for the reply endpoint answering the
      falsifiable question directly; the plain statement that no browser and no
      display exist here, so the rendering is unverified; the accepted consequences
      and the residual risks from the spec.
