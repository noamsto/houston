# Derive Caps at composition time and evict finished runs

Closes #14. Scope: `runs/` only.

## Defect 1 — Caps derived at composition time

**Authoritative signal.** `TmuxSource` polls `ListPaneOptions` every tick (2s
default) and emits `Gone` the instant a pane disappears from that poll — so the
presence of a `"tmux"` entry in `composeLocked`'s `bySource` map for a key is
ground truth for "a live pane exists right now." `HookSource` cannot answer
this: it asserts `Caps` from a `TmuxPane` ref cached in hook state that
survives the pane's death until the whole hub session ends.

**Derivation rule**, computed in `composeLocked` from `bySource` (which
sources are currently present for this key), not from any `Caps` field a
source publishes:

- `Terminal` = `"tmux"` layer present.
- `Kill` = `"tmux"` layer present. There is no "crew kill" — killing requires
  a pane.
- `Reply` = `"tmux"` layer present **or** `"crew"` layer present. The `Caps`
  doc comment already says "pane keys or `crew reply`" — a crew-managed run
  can take a `crew reply` over the bus with no pane at all, and the `"crew"`
  layer only exists for git-crew-bus-tracked branches, which is exactly
  crew-reply-capable.

This makes `Caps` layer-presence-derived, not OR-accumulated. Once derived
this way, no source needs to assert `Caps` on its own `Delta.Run` at all —
`hooksource.go`'s and `tmuxsource.go`'s per-layer `Caps` assignments become
dead fields that nothing reads. Delete them rather than leave them as
misleading dead weight (per repo convention: delete, don't preserve).

### Steps

- [ ] **Step 1: prove the bug** — add `TestCapsTerminalStaysTrueAfterTmuxLayerGoesGone`
  and `TestCapsReplyRequiresLayerNotJustFlag` to `runs/registry_test.go`
  against the **current** code. Run `go test ./runs/... -run TestCaps` and
  confirm both fail (red) — capture the failure output.
  - Test (a): `Apply` a `"tmux"` delta (`Run{Agent:"claude"}`) and a `"hooks"`
    delta whose `Run.Caps = Caps{Terminal:true,Reply:true,Kill:true}` on the
    same key (mirrors what `hooksource.go` sets today for a pane-backed
    session). Assert `Snapshot()[0].Caps.Terminal` is true. Then `Apply` a
    `"tmux"` `Gone` for that key. Assert the run is still listed (hooks layer
    survives) but `Caps.Terminal` and `Caps.Kill` are now **false** — this is
    the part that fails against current `mergeInto`, which keeps ORing the
    hooks layer's stale `true`.
  - Test (b): `Apply` only a `"crew"` delta on key `"branch/fix/1"`
    (`Run{Agent:"claude", State: StateBlocked}`, no `Caps` set — matches what
    `crewsource.go` publishes today). Assert `Caps.Reply` is **true** and
    `Caps.Terminal`/`Caps.Kill` are false. Fails today because nothing ever
    sets `Reply` true for a crew-only key.
  **Note on test (a):** give the `"hooks"` delta `Agent: "claude"` too (not
  just the tmux delta) — `listed()` is `r.Agent != ""`, so without it the run
  is unlisted after `tmux` goes `Gone` and `Snapshot()` has nothing to assert
  on.
- [ ] **Step 1b: prove the broadcast half of the bug.** Add
  `TestCapsTransitionIsBroadcast` to `runs/registry_test.go`: `Subscribe()`,
  `Apply` a `"tmux"` delta and a `"hooks"` delta on the same key — both
  carrying `Agent: "claude"`, and **deliberately no** `Issue`/`PR`/`Crew`/
  `Activity.Task` on either, so no field other than `Caps` differs across the
  transition — drain the resulting broadcast, then `Apply` the `"tmux"`
  `Gone`. Assert a `Run` arrives on the subscriber channel with
  `Caps.Terminal == false`. This must fail against current code even after
  Step 2's `composeLocked` change alone, because `runSignature` doesn't
  include `Caps` yet — nothing about this transition changes the signature,
  so `Apply` never re-broadcasts and the subscriber sees nothing. That gap is
  exactly acceptance criterion 1's "and that transition is broadcast to
  subscribers."
- [ ] **Step 2: implement the derivation.** In `runs/registry.go`:
  - Add `func deriveCaps(bySource map[string]Run) Caps` next to
    `composeLocked`, implementing the three rules above by checking key
    presence in `bySource` (`_, hasTmux := bySource["tmux"]`, same for
    `"crew"`).
  - In `composeLocked`, after the merge loop, set `out.Caps =
    deriveCaps(bySource)` (overwriting whatever the loop produced).
  - Delete the `Caps` block from `mergeInto` (the three `if src.Caps.X`
    lines) — `Caps` is no longer a merged field.
  - In `runs/hooksource.go`, delete the `r.Caps = Caps{...}` line in
    `runFromSessionView`.
  - In `runs/tmuxsource.go`, delete the `Caps: Caps{...}` field from the
    `Run{}` literal in `deltasFromTmux`.
  - **In `runSignature` (registry.go:188-218), add `Caps` to the hashed
    fields** (e.g. `b.WriteByte('|')` then the three bools formatted). Without
    this, Step 1b's test stays red even after everything else is fixed —
    `Caps` derives correctly but a change in it alone never triggers a
    broadcast, which is precisely acceptance criterion 1's second half.
  - Note for later readers: after this, a composed run can carry a non-nil
    `Tmux` ref (from `hooksource.go`'s cached ref) alongside
    `Caps.Terminal == false` — that's intended, `Caps` is the affordance
    signal per the design doc, not `Tmux != nil`. Leave a short comment near
    `deriveCaps` saying so, so the run-detail work (plan A) doesn't branch on
    `run.Tmux` by mistake.
- [ ] **Step 3: fix the fallout in existing tests.**
  - `runs/registry_test.go`: `TestMergeIntoCoversEveryField` iterates every
    `Run` field via reflection and fails if `mergeInto` leaves it zero. Add a
    `case "Caps": continue` alongside the existing `"ID"`/`"Removed"` cases,
    with a one-line comment: derived in `composeLocked`, not merged.
  - `runs/hooksource_test.go`: `TestRunFromSessionViewKeysOnPane` (asserts
    `r.Caps.Terminal`/`r.Caps.Reply` are true) and
    `TestRunFromSessionViewFallsBackToSessionID` (asserts `r.Caps.Terminal` is
    false) both test a field `runFromSessionView` no longer sets. Delete
    those specific assertions (leave the rest of each test — `Tmux`,
    `Agent`, key shape — intact). Caps behavior is now covered by the
    registry-level tests from Steps 1/1b.
- [ ] **Step 4: confirm green, then confirm the regression bites.** Run
  `go test ./runs/...` — everything green, including the new tests. Then
  temporarily re-introduce the `Caps` OR block in `mergeInto` (or comment out
  the `out.Caps = deriveCaps(...)` line) **and** revert the `runSignature`
  addition, and re-run `go test ./runs/... -run TestCaps` to confirm all three
  new tests go red again. Revert back to the fix.

## Defect 2 — evict finished crew-sourced runs

**Rule.** Eviction is **state-based**, gated on the bus's own terminal status
(`Run.State == StateDone || Run.State == StateFailed` — `FromCrewState` already
maps `"done"`/`"failed"`/`"exited"` to these), **not** time-based on inactivity.
A purely-quiet-but-still-`StateBlocked` run (waiting hours for a human) must
never be evicted just because time passed — that's an explicit acceptance
requirement, and a plain "no message in N minutes" rule would violate it.

State-based with zero delay has its own problem: it would yank the "done" card
out from under a human looking at it the instant the worker posts done. So:
evict a terminal-state branch only once `now - Run.UpdatedAt` (already
populated from the status record's `ts`) exceeds a grace period. Pick **10
minutes** — long enough that a human glancing at Fleet right after a finish
still sees it, short enough that the measured-live "10 of 21 runs were history"
problem actually clears out during a normal session. (The hub's parallel
precedent keeps ended sessions 24h; crew-sourced runs don't need that long
because the PR/branch itself persists elsewhere — Fleet is not the only record
of a finished run.)

A terminal-state record with a missing/zero `ts` (the bus is shell-written;
`deltasFromCrewLog` already tolerates torn lines) must not be treated as
"infinitely old" — guard `UpdatedAt == 0` as not-yet-eligible, or a malformed
line evicts a run instantly, bypassing the grace period entirely. A branch
with only a `dispatch` record and no `status` record yet has `State == ""`
and `UpdatedAt == 0` — falls under the same guard, never evicted, which is
correct (nothing to evict). `StateReview` (`pr_open`) is deliberately **not**
evicted under this rule — a human may still act on an open PR — so the
"~10 of 21 were history" figure is expected to include some `review`-state
runs that this change does not touch; that's a scope choice, not a gap.

**Shape.** `CrewSource.Run()` currently loops `s.scan()` every tick and always
emits `Delta{Run: r}` per branch — never `Gone`. `scan()` (`crewsource.go:63`)
returns **nil** when `ListWindowOptions()` errors, indistinguishable from "no
branches" — so naively diffing against `nil` would emit `Gone` for every
crew-sourced run on one transient tmux hiccup, then have them reappear next
tick. `TmuxSource.Run` already guards exactly this
(`tmuxsource.go:53-55`: "A transient tmux error is not evidence that every
pane vanished: skip the tick entirely rather than emitting `Gone` for
everything seen") — `CrewSource` needs the same guard. Change `scan()`'s
signature to report success:

```go
// scan returns ok=false when ListWindowOptions failed — a transient tmux
// error, not evidence every crew-sourced branch vanished. The caller must
// skip the tick entirely rather than diff against an empty map.
func (s *CrewSource) scan() (branches map[string]Run, ok bool)
```

Extract a pure, directly testable tick function (mirrors the existing
seen-map `Gone`-diffing pattern already used by `TmuxSource.Run` and
`HookSource.Run`, and mirrors how `deltasFromCrewLog`/`deltasFromTmux` are
already pure functions tested without goroutines or channels):

```go
// tickCrewDeltas computes this cycle's deltas from the branches scan()
// currently sees, evicting anything whose bus status has been terminal for
// longer than crewEvictionGrace. seen is the previous tick's emitted key set;
// it returns the deltas to send and the new seen set.
func tickCrewDeltas(current map[string]Run, seen map[string]bool, now time.Time) ([]Delta, map[string]bool)
```

### Steps

- [ ] **Step 1: extract current behavior verbatim (no behavior change).**
  Add `tickCrewDeltas` to `runs/crewsource.go` as a faithful transcription of
  today's loop body (`crewsource.go:47-54`): for each `branch, r` in
  `current`, emit `Delta{Source:"crew", Key:"branch/"+branch, Run:r}` and add
  the key to the new seen set — **no eviction logic yet, no `Gone` emitted
  for anything**. Change `CrewSource.Run()` to call this instead of its
  inline loop, with `scan()` still returning a single map (defer the `ok`
  signature change to Step 2b). This step changes no behavior; it only makes
  the loop unit-testable. Run `go test ./runs/...` — everything still green,
  same as before this step.
- [ ] **Step 2: prove the bug** — add `TestTickCrewDeltasEvictsFinishedRunsAfterGrace`
  to `runs/crewsource_test.go` against the extraction from Step 1: a branch
  `StateDone` with `UpdatedAt` 30 minutes in the past, present in both
  `current` and the incoming `seen` set. Assert the returned deltas contain
  exactly one delta with `Gone == true`, `Key == "branch/fix/412"`, **and
  `Source == "crew"` checked explicitly** (not a whole-struct compare) — and
  that the key is absent from the returned seen set. The explicit `Source`
  check matters: `Registry.Apply` deletes `r.layers[d.Key][d.Source]`, so a
  delta with an empty `Source` deletes nothing and eviction would silently
  no-op end to end while every test here still passed. **Confirm this
  fails** against the Step-1 extraction — it does, for the real reason:
  today's shape has no eviction concept at all, so it re-emits the branch
  and never produces `Gone`. This is the actual defect-2 proof, not a
  stand-in.
- [ ] **Step 2b: implement eviction.**
  - Add `const crewEvictionGrace = 10 * time.Minute` and
    `func crewRunFinished(r Run, now time.Time) bool`: `false` if
    `r.State` is not `StateDone`/`StateFailed`, `false` if `r.UpdatedAt == 0`
    (malformed or dispatch-only record — never treat as eligible), otherwise
    `now.Sub(time.Unix(r.UpdatedAt, 0)) > crewEvictionGrace`. Spell the
    `time.Unix` conversion out explicitly — `Run.UpdatedAt` is unix
    **seconds** (`crewsource.go:207`, `rec.TS / 1000`), so comparing it
    directly against a `time.Duration` or against `now.Unix()` without the
    conversion is the kind of off-by-1000× bug that would still pass every
    test in this plan if written wrong.
  - Update `tickCrewDeltas` to skip (no emit, not added to the new seen set)
    any branch where `crewRunFinished(r, now)`; for every key in the old
    `seen` not present in the new seen set (whether skipped for that reason
    or simply absent from `current`), emit
    `Delta{Source:"crew", Key:key, Gone:true}`.
  - Change `scan()` to `(map[string]Run, bool)` per the signature above
    (`ok=false` on `ListWindowOptions` error). In `CrewSource.Run()`, when
    `ok` is false, skip the tick entirely — do not call `tickCrewDeltas`, do
    not touch `seen`, matching `tmuxsource.go:53-55`'s guard exactly.
- [ ] **Step 3: finish the regression tests.** Alongside the eviction test,
  add:
  - `TestTickCrewDeltasKeepsFreshlyFinishedRuns` — `StateDone`,
    `UpdatedAt` a minute ago → still emitted, not evicted.
  - `TestTickCrewDeltasNeverEvictsAQuietBlockedRun` — `StateBlocked`,
    `UpdatedAt` 24h ago → still emitted, never evicted. This is the test that
    directly encodes the "don't evict a human's blocked run" acceptance
    requirement.
  - `TestTickCrewDeltasKeepsADispatchedRunWithNoStatusYet` — a branch with
    only a `dispatch`-shaped `Run{}` (`State == ""`, `UpdatedAt == 0`) → still
    emitted, never evicted (guards the `UpdatedAt == 0` short-circuit against
    a future refactor of `crewRunFinished`).
  - A scan-failure test at the `CrewSource.Run()` level, or at minimum a
    comment at the `ok` check in `Run()` citing the same rationale as
    `tmuxsource.go:53`, if driving `Run()` itself proves impractical without
    a fake tmux client (`CrewSource.client` is the concrete `*tmux.Client`,
    unlike `TmuxSource`'s `lister` interface — do not introduce a new
    interface seam for this task; a well-placed comment plus the `ok`-based
    early-return is sufficient given the scope boundary).
  Run `go test ./runs/...` and confirm all green.
- [ ] **Step 4: confirm the regression bites.** Two separate mutations, since
  one alone doesn't bite both new tests:
  - Temporarily make `crewRunFinished` always return `false` → re-run →
    confirm `TestTickCrewDeltasEvictsFinishedRunsAfterGrace` goes red (the
    freshly-finished test stays green under this mutation — it asserts a
    fresh `StateDone` run is *not* evicted, which is exactly what
    always-`false` produces; that's expected, not a failure). Revert.
  - Temporarily set `crewEvictionGrace = 0` → re-run → confirm
    `TestTickCrewDeltasKeepsFreshlyFinishedRuns` goes red. Revert.
  - Quiet-blocked and dispatch-only tests stay green under both mutations —
    expected, not a failure; they exercise the state/UpdatedAt guards, not
    the grace threshold.

## Final verification

- [ ] `go test ./runs/...`, `go vet ./...`, `gofmt -l runs/` all clean.
- [ ] Skim `docs/superpowers/2026-09-08-state-of-play.md` "Decisions already
  made" once more — confirm nothing here changes the `tmux → crew → hooks`
  precedence order (it doesn't: that order still governs every other field;
  `Caps` is simply no longer a field the merge loop touches at all).
