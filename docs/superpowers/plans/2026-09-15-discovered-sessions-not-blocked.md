# Plan: Discovered transcript-only sessions must not claim blocked — closes #44

## Context established during research

- Root cause chain:
  - `hub/discovery.go:159-169` `synthesizeFromTranscript` classifies a discovered
    transcript's tail into `hook.StateToolRunning` (unmatched tool_use, age<30s),
    `hook.StateEnded` (unmatched tool_use, or age>12h), or — **default** —
    `hook.StateWaiting`. That default fires for any discovery-only session whose
    last assistant turn is 30s–12h old, regardless of whether the process is
    alive, idle at its prompt, or long gone. There is no liveness signal here at
    all — discovery only ever reads a transcript file.
  - `hub/hub.go:190` `seed` only registers a discovered session if hub doesn't
    already know it (checked by session ID) — a real hook write for the same
    session ID, before or after, always wins (`hub/discovery_test.go`'s
    `TestHubHookStateWinsOverDiscovery` covers this and is unaffected by this
    plan).
  - `runs/state.go:40-55` `FromHookState` maps `hook.StateWaiting` /
    `hook.StatePermission` → `runs.StateBlocked` unconditionally. It has no way
    to know whether the `hook.State` it's looking at came from a real hook
    (`hook/hook.go`, a completely separate write path that also produces
    `StateWaiting`/`StatePermission`, e.g. from a `Notification` hook) or from
    discovery's guess.
- Decision: **do not** touch `hook.State`'s existing meaning for
  `StateWaiting`/`StatePermission` — those are written exclusively by
  `hook/hook.go` from real hook events and must keep mapping to `StateBlocked`
  (task requirement: "a hook-backed `waiting`/`permission` state must remain
  blocked exactly as today"). Instead, stop discovery from *producing*
  `hook.StateWaiting` for the guessed case. Add a new `hook.State` value that
  means "no signal, not a confirmed wait" and is used **only** by discovery's
  default branch; `runs.FromHookState` maps it explicitly to `runs.StateIdle`
  (which already exists and is exactly this "no attention needed" bucket — see
  `runs/state.go:24` and its use as the default fallback for `FromCrewState`/
  `FromClaudeStatus`).
  - This was weighed against the task's other two candidates:
    - **Liveness check (process alive for that session/cwd)** — rejected as
      the primary mechanism: houston is meant to work over Tailscale/SSH from a
      phone against a headless box: `ps`-based liveness is unreliable across
      machines, adds an OS-specific dependency, and race-prone (has to answer
      "alive" for a process one page-load away from writing its own hook state
      anyway). Not ruled out for later, but out of scope for this fix.
    - **Shorter quiet window than 24h** — rejected as insufficient alone: the
      task's own measured symptom includes sessions well inside a shorter
      window (5 min–12h) that still shouldn't claim blocked. A shorter window
      only trims the tail, it doesn't fix the mislabeling.
  - The chosen fix directly targets the mislabeling per the task's framing
    ("the fix is to stop *mislabelling* sessions as blocked, not to hide
    genuinely blocked ones") and is the smallest change that provably can't
    affect hook-confirmed state, because the two write paths
    (`hook/hook.go` vs `hub/discovery.go`) are physically separate files that
    both happen to speak the shared `hook.State` vocabulary.
- `docs/superpowers/2026-09-08-state-of-play.md`: "`blocked` is the only state
  meaning 'a human is required' ... nothing blocked is ever hidden, but only
  fresh ones count toward the badge." This plan doesn't change that invariant —
  it changes what state a discovery-only session is *assigned* before the
  freshness rule ever sees it. A genuinely blocked (hook-confirmed) session is
  untouched and still never hidden.
- `ui/src/fleet/staleness.ts`'s `FRESH_MS` rule is UI-side and out of scope; not
  contradicted by this change (no UI edits).
- Existing tests that currently assert the old default and must be updated as
  part of this fix (not preserved as pre-existing behavior):
  - `hub/discovery_test.go` `TestDiscoveryFindsWaitingSession` — asserts
    `hook.StateWaiting` for a transcript with assistant text and no pending
    tool. Must become the new idle state.
  - `hub/discovery_test.go` `TestHubSeedsDiscoveredSessionOnStart` — same
    assertion via the hub's snapshot.
- `runs/state_test.go` presumably has `FromHookState` cases; check it when
  editing and add a case for the new state (see step 3).

## Step-by-step plan

- [ ] **Step 1: add `hook.StateIdle` to the hook state vocabulary**
  - File: `hook/state.go`.
  - Add a new constant alongside the existing block, e.g.:
    ```go
    // StateIdle means no signal confirms a session is waiting on anyone — used
    // only by transcript discovery's inference, never written by a real hook.
    StateIdle State = "idle"
    ```
  - Placement: after `StatePermission`, before `StateCompacting` (keep it near
    its semantic sibling `StateWaiting`). Doc comment must say plainly that this
    is discovery's "unconfirmed" bucket, not a state a hook ever writes — so a
    future reader doesn't wire a real hook event to it.

- [ ] **Step 2: change discovery's default classification**
  - File: `hub/discovery.go`, `synthesizeFromTranscript`, the `switch` at
    lines ~160-169.
  - Change the `default:` case from `state.State = hook.StateWaiting` to
    `state.State = hook.StateIdle`.
  - Update the switch's doc/inline comments if any reference "waiting" as the
    default outcome, so the code reads consistently with the new behavior.
  - Do not touch the `unmatchedTool != nil && age < 30*time.Second` (tool
    running) or `unmatchedTool != nil, age > 12*time.Hour` (ended) branches —
    unaffected by this task.

- [ ] **Step 3: map `hook.StateIdle` explicitly in `FromHookState`**
  - File: `runs/state.go`, `FromHookState`.
  - Add an explicit case rather than relying on the `default:` fallthrough
    (both currently resolve to `runs.StateIdle`, but an explicit case
    documents the mapping and survives a future change to the default arm):
    ```go
    case hook.StateIdle:
        return StateIdle
    ```
  - Add it directly after the `hook.StateWaiting, hook.StatePermission` case so
    the two are visually adjacent for anyone diffing "waiting" vs "idle"
    semantics.
  - Check `runs/state_test.go` for existing `FromHookState` table cases; add a
    row for `hook.StateIdle → StateIdle` if the table exists, following its
    existing style.

- [ ] **Step 4: update the two existing discovery tests whose assertions are
  now wrong on purpose**
  - File: `hub/discovery_test.go`.
  - `TestDiscoveryFindsWaitingSession`: rename to
    `TestDiscoveryInfersIdleWhenNoPendingTool` (the assertion is no longer
    about "waiting"), change the assertion to
    `s.State != hook.StateIdle` and update the error message/comment
    accordingly. Keep the rest of the test (fixture, CWD/TranscriptPath
    assertions) unchanged.
  - `TestHubSeedsDiscoveredSessionOnStart`: change the final assertion from
    `hook.StateWaiting` to `hook.StateIdle`; update the error message.
  - Leave `TestHubHookStateWinsOverDiscovery`,
    `TestDiscoveryInfersToolRunningWhenRecent`,
    `TestDiscoveryMarksOldUnmatchedToolAsEnded`,
    `TestDiscoveryDatesSessionByLastEventNotMtime` (ended-state assertion) as
    they are — none of them exercise the default branch this task changes.

- [ ] **Step 5: add the acceptance-criteria regression tests**
  - File: `hub/discovery_test.go` (new table-driven test, matching the file's
    existing per-scenario style rather than introducing a new pattern).
  - New test, e.g. `TestDiscoveryDoesNotInferBlockedFromGuess`: a transcript
    whose last assistant event is ~5 minutes old (via `os.Chtimes` +
    a timestamp in the body, following `TestDiscoveryDatesSessionByLastEventNotMtime`'s
    pattern for controlling `UpdatedAt` independent of mtime) with assistant
    text and no pending tool_use → `got[0].State == hook.StateIdle`, and
    separately assert `runs.FromHookState(got[0].State) != runs.StateBlocked`
    (import `runs` in the test file if not already imported — check for an
    import cycle first: `runs` imports `hub`? No — `runs/hooksource.go`
    imports `hub`, so `hub` must NOT import `runs`. Put the
    `FromHookState(...) != StateBlocked` assertion in `runs/state_test.go`
    instead, driven by `hook.StateIdle` directly, to avoid a cycle — no need to
    round-trip through `hub` for that half of the assertion.)
  - `runs/state_test.go`: add/confirm a case exercising
    `FromHookState(hook.StateWaiting) == StateBlocked` and
    `FromHookState(hook.StatePermission) == StateBlocked` still hold (hook-path
    unaffected) alongside the new `FromHookState(hook.StateIdle) == StateIdle`
    case from step 3 — this is the acceptance criterion "a hook-state waiting
    session IS still blocked," verified without needing a live hook file.
  - Confirm (no new test needed, already covered) that
    `TestDiscoveryInfersToolRunningWhenRecent` and
    `TestDiscoveryMarksOldUnmatchedToolAsEnded` continue to pass unmodified —
    covers "recent unmatched tool_use still infers running" and "the >12h
    still ended" acceptance rows.

- [ ] **Step 6: run the full verification sweep**
  - `go build ./...`
  - `go vet ./...`
  - `go test ./...`
  - `golangci-lint run`
  - All from the devshell (`nix develop -c ...` if not already inside one).

- [ ] **Step 7: live-evidence check (manual, not a test)**
  - Build the binary (`go build -o /tmp/houston-check .` or similar, outside
    the repo tree) and run it read-only against the real state dir on a spare
    port: `houston -addr 127.0.0.1:9191` (do not touch the running
    `houston.service` on :9090).
  - Hit `/api/runs/stream` with `Authorization: Bearer $(cat
    ~/.local/state/houston/token)` (never print the token itself — pipe it
    directly into the header) and record the `sess-*` state histogram.
  - Compare against the task's documented "before" baseline (41 blocked / 23
    done out of 64 `sess-*` runs) and report the after-histogram in the PR
    body. Kill the spare-port process when done.

## Out of scope (explicit, per task doc)

- No UI changes.
- No changes to duplicate-card merging for hooked sessions.
- No hook installation changes.
- No OpenCode changes.
- No liveness/process-check mechanism (considered and rejected above, not a
  half-implementation left in the codebase).
