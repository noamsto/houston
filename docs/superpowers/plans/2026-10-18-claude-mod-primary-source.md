# Plan: Claude Code mod as primary Claude source (#303)

Spec: `docs/superpowers/specs/2026-10-18-claude-mod-primary-source.md` (D1–D10, D7a, D7b).
Orchestration consult: not tripped. Four seams (mod ↔ claudemod ↔ hub ↔ server) with one
owner each; the spec already fixes the interfaces below, so an opus plan suffices.

Every step: Go steps run `gofmt -l`, `go vet ./<pkg>/...`, `go test ./<pkg>/...` and
`go test -race ./<pkg>/...` for the touched packages; TS steps run
`claude plugin test contrib/claude-mod` and `claude plugin validate contrib/claude-mod` with
the real binary (`/nix/store/z2snd5wsm40g28xi68aqsiipxjw3walb-claude-code-2.1.302/bin/claude`;
the PATH `claude` wrapper refuses `plugin test`).

## Interfaces (fixed for all steps)

```go
// claudemod/record.go — added to Record (all omitempty)
CWD        string     `json:"cwd"`
Transcript string     `json:"transcript"`
Pane       string     `json:"pane"`
TmuxServer string     `json:"tmux_server"`
Input      *ToolInput `json:"input"`
Last       string     `json:"last"`
StopTask   string     `json:"stop_task"`
TimeoutMS  int64      `json:"timeout_ms"`
// Task gains Description string `json:"description"` and ToolUse string `json:"tool_use"`.
// Ask.Options becomes []AskOption{Label string `json:"label"`; Preview bool `json:"preview"`}.
// Explicit lowercase tags: hook.ToolHint reads a map[string]any by these exact keys.
type ToolInput struct {
    Description string        `json:"description,omitempty"`
    Command     string        `json:"command,omitempty"`
    FilePath    string        `json:"file_path,omitempty"`
    Path        string        `json:"path,omitempty"`
    Pattern     string        `json:"pattern,omitempty"`
    URL         string        `json:"url,omitempty"`
    Prompt      string        `json:"prompt,omitempty"`
    Questions   []AskQuestion `json:"questions,omitempty"` // only [0] is sent
}
type AskQuestion struct {
    Header   string `json:"header,omitempty"`
    Question string `json:"question,omitempty"`
}

// claudemod/store.go
type BGTask struct {
    ID, Kind, Hint string // Kind: "shell" | "monitor"
    Since   int64          // unix seconds
    Expires int64          // unix seconds; 0 = never
}
type Session struct { // the view; existing debug fields kept
    SID string; State hook.State; Tool, Hint, Message string
    Since int64        // unix s of the last state change
    Updated int64      // ms, newest applied record's ts (any agent)
    MainUpdated int64  // ms, newest applied main-loop record's ts
    Known bool         // a state-defining main-loop record folded this boot
    Turns int          // main-loop turn.start count
    Last string        // Stop's last_assistant_message tail
    StopSeen bool      // a main-loop stop since the last turn.start
    Background []BGTask; BGKnown bool
    CWD, Transcript, Pane, TmuxServer string
    Boot string; LastSeq int64; InFlight []string; Applied, Stale, Reordered int
}
func (s Session) BackgroundAt(now time.Time) []BGTask // drops expired monitors
func NewStore() *Store
func (s *Store) Apply(rec Record) bool
func (s *Store) Session(sid string) (Session, bool)
func (s *Store) Snapshot() []Session
func (s *Store) Notify() <-chan struct{} // 1-buffered; non-blocking send after unlock
func (s *Store) TakeDirty() []string
func (s *Store) Sweep()                  // eviction; evicted sids become dirty

// hub
type Options struct { /* existing */ Mod *claudemod.Store }
const modLagGrace = 10 // seconds
```

## Steps

Ordering: `S1 → S2 → S3 → S4 → (S5 ∥ S6)`; `S7 ∥ S1..S6`; `S8` after `S7`; `S9`, `S10` last;
`S11` (live harness) after everything, by the lead.

### S1. Record fields and `ts` validation — `claudemod/record.go`, `claudemod/record_test.go`
implement: haiku
- Add the Record/Task/Ask fields above, `ToolInput`, `AskQuestion`, `AskOption`.
- `Decode` rejects `rec.TS < 1` (`invalid ts %d`).
- Tests: `ts` 0 and negative rejected (table row in the existing Decode test); a record with
  every new field round-trips through `Decode`.

### S2. Socket hardening — `claudemod/listen.go`, `claudemod/listen_test.go`
implement: sonnet
- `listenPrivate`: after chmod, `os.Link(tmpSock, path)` (EEXIST → error
  `%s: already exists`), then `os.Remove(tmpSock)`; temp dir still removed by the defer.
  `SetUnlinkOnClose(false)` stays (Close removes `path`).
- Tests (AC7.1):
  - `TestSecondListenerRefusesAndFirstStaysReachable`: Listen A on path; Listen B on the same
    path fails; a POST over path still reaches A's store; Close of nothing from B touched it.
  - `TestLinkRefusesAnExistingPath`: create a live socket at path by hand (bound listener),
    call `listenPrivate(path)` directly (bypassing `clearStale`, the race window) → error, and
    path is still the original socket (dial succeeds to the original listener).
  - `TestListenLeavesOnlyTheSocket` keeps passing.

### S3. Fold and store — `claudemod/store.go`, `claudemod/store_test.go`, `claudemod/compare_test.go`
implement: opus
- Fold per spec D5/D6: main-loop `phase`, per-agent inflight (`id → {tool, hint}`, so the
  `tool-running` view shows the newest main-loop call's hint), pending
  permission `{agent, tool, hint, id, message}` with the `tool.check` `ask` candidate,
  `turns`, `last`, `stopSeen`, background entries `{BGTask, toolUse}` plus pending starts
  (`id → {hint, ts}` for a background Bash / Monitor `tool.start`), coords (`cwd`,
  `transcript`, `pane`, `tmux_server`: last non-empty wins), `known`, `bgKnown`.
  - Hints: `hook.ToolHint(tool, json.Marshal(input))` for `Hint`; background hint = a local
    `bgHint`: description, else command, trimmed and cut to 120 runes with `…` (the rule of
    `hub.backgroundHint`).
  - `Message`: the permission's notification message when one arrived, else
    `Claude needs your permission to use <tool>`; empty when no permission.
  - `Since` (unix s) = `ts/1000` of the record that last changed the view state.
  - Records with `agent` set never change `phase`, `turns`, `stopSeen`, background or
    `known`; their `tool.start`/`tool.end`/`tool.check`/`permission` only touch their own
    inflight and the permission.
  - `notification idle_prompt`: no-op. Permission clear rules exactly as D5.
  - `stop` (main): `last`, `stopSeen = true`; `tasks != nil` → membership snapshot (D6),
    `bgKnown = true`. `turn.start` resets `stopSeen`, `last`.
  - `task.end`: drop by `id` or by `tool_use` (the entry's starting tool_use id).
  - `tool.end` non-error with `stop_task` → drop that task; with `task` → add (D6), Monitor
    `expires = ts/1000 + timeout_ms/1000` when `timeout_ms > 0`.
- Store:
  - `newBoot` keeps the base fold (phase, background, `known`, `bgKnown`): a hot reload
    continues the previous boot's session, and `session.start` fires again on reload anyway.
    CLAUDE.md says so (S10).
  - Boot rule D10.2: `session` gains `bootSeen time.Time` (houston monotonic, when the current
    boot's first record arrived); an unseen boot with `ts < bootTS` is retired only when
    `now.Sub(bootSeen) < bootDelaySlack` (5 s); otherwise `newBoot`.
  - `lastApply` stamped only on applied records (the stale/retired paths return before it).
  - `updated`/`mainUpdated` from applied records' `ts`.
  - TTLs: `endedTTL = idleTTL = 24h`; cap `maxSessions = 1000` (evict least recent
    `lastApply` on insert of a new sid over the cap). `sweep` stays rate-limited inside
    `Apply`; `Sweep()` is the exported unthrottled entry for the hub tick.
  - Dirty set + `Notify()`: `Apply` (applied records only) and eviction add the sid; signal
    after `mu.Unlock` (restructure `Apply` so the defer does not hold the lock across the
    send: compute under lock, send after).
  - `Session(sid)` returns `view(sid)`; `view` fills every Session field; `Background`
    sorted by `Since`, then `ID`; `BackgroundAt(now)` filters `Expires != 0 && Expires <= now`.
  - Keep `rec.Message` only for `notification` records (permission message); still drop `Ask`.
- Tests (rewrite spike tests whose rule changed; keep the rest):
  - AC2 `TestLateToolEndAfterTurnCompleteStaysWaiting`: turn.start, tool.start(a),
    turn.complete, then tool.end(a) with a lower seq arriving last (and a variant arriving
    after with a higher seq) → `waiting`, `Reordered` as expected.
  - AC3 `TestEscOnDialogReadsWaiting` (tool.start, tool.check ask, permission, turn.complete
    reason answer → waiting, no permission) and `TestEscOnRunningToolReadsWaiting`
    (tool.start, turn.complete reason aborted → waiting); each asserts the state flips on the
    single `turn.complete` record.
  - AC4 `TestPermissionBlocksAndToolEndClears` (permission → `waiting:permission`,
    Tool/Hint/Message set; tool.end of the candidate id → `thinking`), `…TurnCompleteClears`,
    `TestSubagentPermissionClearsOnItsToolEnd` (agent-scoped), `TestIdlePromptKeepsDialog`.
  - AC5 `TestBackgroundFromMod`: bg Bash start+end → listed with hint/since; `task.end`
    completed → gone; TaskStop `stop_task` → gone; Monitor with `timeout_ms` → `BackgroundAt`
    hides it after expiry; stop snapshot without a UI-killed id → gone; snapshot reporting a
    known Monitor as `shell` keeps kind and expiry; `BGKnown` false until a stop with tasks.
  - AC7.2 `TestDecodeRejectsMissingTS` (in record_test), `TestClockStepBackAcrossReloadKeepsNewBoot`
    (boot A ts 1000; advance the fake houston clock 6 s; boot B ts 900 → B live and folds),
    `TestUnseenOlderBootWithinSlackIsStale`, `TestStaleRecordsDoNotRefreshEviction`.
  - `TestNotifyAfterApply` (dirty sid returned once, channel signalled, no signal for a stale
    record), `TestSweepEvictsAndMarksDirty`, `TestSessionCap`.
  - `TestKnownOnlyAfterStateRecord` (a lone tool.end of an unknown id leaves `Known` false).
  - `compare_test.go` keeps passing (adapt to the new view fields only).

### S4. Hub overlay — `hub/hub.go`, `hub/mod.go` (new), `hub/mod_test.go` (new)
implement: opus
- `Options.Mod`; `Hub.mod`. In `Run`, before the loop:
  `var modCh <-chan struct{}; if h.mod != nil { modCh = h.mod.Notify() }` (a nil channel never
  fires; never call a method on a nil store). In the select:
  `case <-modCh: h.applyModDirty()`. On the 2 s tick, after `h.refreshAllTranscripts()`:
  `if h.mod != nil { h.mod.Sweep(); h.tickMod(time.Now()) }` (`tickMod` recomputes the view of
  every session with `hasMod` and broadcasts if changed).
- `Session` gains `mod claudemod.Session`, `hasMod`, `modOnly` (created from mod data),
  `lastModTurn`.
- `applyModDirty`: for each `TakeDirty` sid read `Session(sid)` outside `h.mu`, then under it:
  - missing in store: `hasMod = false`; a `modOnly` session with no hook state is deleted from
    `h.sessions` (closing chat subs as `handleFSEvent` does); others recompute.
  - present: create the hub session if absent **only when `Known`** (`modOnly = true`);
    store `mod`, `hasMod = true`; set the transcript path (below); for a session with no hook
    state, a `Turns` increase over `lastModTurn` resets the trail (as the hook Turn does);
    recompute; broadcast if changed; refresh the transcript if the path is new.
- Transcript path: `effectivePath = hookState.TranscriptPath` when non-empty, else
  `mod.Transcript`; one helper `setTranscriptPath(p)` holds the existing reset (offset, spend,
  ctx, tools, chat path) and is used by `loadStateFile` and `applyModDirty`.
- `applyHook` (name kept) becomes:
  1. hook state present → `mergeStateIntoView` (as today); else `hasMod` → `mergeModCoords`
     (SessionID, CWD, TmuxPane, TmuxServer, TranscriptPath, Agent `claude`, UpdatedAt =
     `mod.Updated/1000`, Turn = `mod.Turns`).
  2. `modLive()` (D3: `hasMod && mod.Known && !(hook present && hookState.UpdatedAt >
     mod.Updated/1000 + modLagGrace)`) → overlay State, Tool, ToolInputHint (= `mod.Hint`),
     LastMessage (= `mod.Message`), Since, UpdatedAt = max(hook, mod/1000); then the hook
     permission override (D3: mod `tool-running` and `hookState.State == StatePermission` and
     `hookState.UpdatedAt >= mod.MainUpdated/1000` → hook's State/Tool/Hint/LastMessage);
     **return before the heal**.
  3. otherwise today's heal unchanged (hook present only).
- Background and asks are **not** inside `applyHook` (it returns early with no hook state or
  no tool). A new `(s *Session) refreshView(now)` is the one entry every caller uses
  (`loadStateFile`, `syncTranscriptView`, `applyModDirty`, `tickMod`, `expireBackground`):
  it runs the merge/overlay/heal above (`applyHook`, early returns intact), then
  **unconditionally** sets `view.Background = s.background(now)` (live mod with `BGKnown` →
  `mod.BackgroundAt(now)` converted to `[]BackgroundTask{ID, Kind, Hint, Since}`; else
  `s.bg.list(now)`) and `view.Asks = s.asksNow()` (live mod with `StopSeen` →
  `questionTail(mod.Last)`; else `s.asks`). Background stays Claude-only/`done`-dropped in
  `runFromSessionView` as today.
- Tests (`hub/mod_test.go`, temp state dir + `writeState`, a real `claudemod.Store`, records
  applied directly; wait with `waitUntil` on `findSession`):
  - `TestModOverlayWinsWhileLive` (hook `thinking` stuck after a late PostToolUse; mod reads
    waiting → view `waiting`).
  - `TestModLiveSkipsTranscriptHeal` (AC3 at hub level: hook `tool-running`, mod
    `turn.complete` → `waiting` with an empty transcript).
  - `TestHookAheadFallsBack` (hook UpdatedAt 11 s past the newest mod record → hook state wins).
  - `TestHookPermissionWinsWhileModToolRunning` and `…IgnoredAfterModTurnComplete`.
  - `TestModOnlySessionCreatedAndJoinedByPane` (no state file; mod `session.start` with pane,
    tmux_server, cwd, transcript → hub session with those coords, agent `claude`).
  - `TestModOnlySessionNotCreatedUntilKnown`.
  - `TestBackgroundSwitchesWithoutDoubleCount` (transcript has bg shell A; mod live without
    `BGKnown` → A from transcript; after a stop with tasks [B] → only B; mod goes non-live →
    A again).
  - `TestMonitorExpiresOnTick` (mod-only session, Monitor with timeout; `tickMod` with a later
    `now` drops it).
  - `TestEvictionDeletesModOnlySession`.
  - `TestModAsksFromStopLast` (live mod: stop `last` ending in `?` then turn.complete →
    view `Asks` set immediately with an empty transcript; turn.start clears it).
  - `TestDiscoverySeededSessionGetsModBackground` (no hook state, discovery-seeded, mod live
    with `BGKnown` → mod background shows: the no-hook-state path refreshes it).
  - AC6: no existing hub test changes; run `go test ./hub/ -count=1`.

### S5. Composed run state — `runs/hooksource_test.go`
implement: haiku
- `TestModViewLateToolEndReadsIdle` (AC2) and `TestModPermissionViewReadsBlocked` (AC4): build
  the `hub.SessionView` the overlay produces (state `waiting`, resp. `waiting:permission` with
  the synthesized message) and assert `runFromSessionView` yields `StateIdle`, resp.
  `StateBlocked` with a `dialog` Question; plus a full-stack variant through a real hub +
  store + `HookSource` channel, reusing `hooksource_test.go`'s existing hub-backed harness,
  asserting the emitted Delta's `Run.State`.

### S6. Wiring and default — `main.go`, `server/server.go`, `server/claudemod.go`
implement: sonnet
- `-claude-mod` default `true`, usage `listen on <status-dir>/claude-mod.sock for the
  read-only Claude Code mod's records (default on; -claude-mod=false disables)`.
- `server.New`: the hub is built in the `Server` struct literal (server.go ~283), before
  today's mod block (~393). Move the mod setup ahead of the literal: when
  `ClaudeModSocket != ""`, create the store, `Listen`; on
  error `slog.Warn("claude mod socket unavailable, using hooks only", "error", err)` and drop
  the store; on success pass it as `hub.Options{Mod: store}` (switch `hub.New` to
  `hub.NewWithOptions(cfg.StatusDir, hub.Options{Mod: store}, slog.Default())`) and keep
  `/api/debug/claude-mod`.
- Test in `server/` (existing server tests construct `Config`): `TestClaudeModSocketTakenStillStarts`
  — a listener already on `<status>/claude-mod.sock`; `New` succeeds; the debug route is 404.

### S7. Mod — `contrib/claude-mod/hooks/register.ts`, `register.test.ts`, `README.md`, `.claude-plugin/plugin.json`
implement: opus
- No identifier, string or property named `deny` or `drop` anywhere in the file (the guard's
  whole-file word ban); e.g. name the cap eviction `evictOldest`.
- Queue/timer/gen per spec D7 (cap 256; `drain` guarded against re-entry; sid per gen map;
  socket path resolved in `drain` once; `$.clock.every(200, …)` started in `session.start`,
  fallback start in `post` when none).
- Fields per D7/D7b; `inputHint(x)` picks the subset, strings cut to 200;
  `parseTaskEnds` cuts each part before `<summary>`/`<event>`, keeps only terminal statuses,
  emits `{id}` per `<task-id>` and `{tool_use}` per `<tool-use-id>`; nothing posted when no
  terminal part.
- `pane`/`tmux_server` (pid field of `$TMUX`, split on `,`, second from the right) are read
  once per module load in `drain` and added to `session.start`, `turn.start` and every
  classic record, so a mod-only session after `/clear` (no `session.start`) or a houston
  restart still joins its pane.
- Tests (mock.clock to drive the drain): delivery only after the clock advances;
  `tool.end` posted after `next` resolves arrives via the timer; gen across a session reset
  (a record queued before `session.end` keeps `sid-1` even when drained after the next
  session started); queue cap; terminal-only `task.end` (per-event part ignored,
  `<event>`-embedded tags ignored); new fields (session.start coords via `mock.env`
  `TMUX`/`TMUX_PANE`; Monitor `timeout_ms`; TaskStop `stop_task`; permission `agent` +
  `input`; stop `last` cut to 300 and task `description`); events/results pass through
  unchanged.
- README: drop "spike" wording, list the new fields and the widened privacy contract
  (assistant-text tail, input hints, cwd/transcript path, tmux pane/pid), the timer.
  plugin.json description: drop "(spike)", version 0.2.0.

### S8. Guard — `claudemod/modguard_test.go`
implement: sonnet
- Per D10.4, with every hook-body rule scoped to the hook bodies, not the whole file (helpers
  such as `drain`/`inputHint` may return objects): a small scanner that skips string and
  template-literal contents strips comments (`//…` to EOL, `/*…*/`) and finds each
  `on(` call's argument span by paren matching;
  normalise `?.`/`!.`; allowed calls `$.http.fetch`, `$.env.get`, `$.session.id`,
  `$.clock.every`; drop `clock` from the forbidden nouns; hook param check
  (`on\(\s*'[^']+'\s*,\s*(async\s*)?\(([^)]*)\)` → params exactly `$`, `e`, `next`);
  `([\w$]+)\s*:\s*EngineInterface` → name `$` (whole file; `\w` alone misses `$`); every
  `\bnext\s*\(([^)]*)\)` argument is `e` (whole file); inside hook spans flag
  `\.\.\.\s*e\b`, `\be\s*\.\s*\w+\s*=[^=]`, spreads/property writes of a name bound by
  `(const|let)\s+(\w+)\s*=\s*await\s+next\(e\)`, and `return\s*\{`. S8 runs after S7 and
  `TestModIsReadOnly` must pass on S7's file.
- Tests (AC7.4): rows for `$?.prompt?.submit(…)`, `(api, e, next) => api.prompt.submit`,
  `next({ ...e, text: 'x' })`, `return { ...r, decision: 'allow' }`, `r.decision = 'allow'`,
  a commented-out forbidden call accepted, and `TestModIsReadOnly` on the real file.

### S9. CI and recipe — `.github/workflows/ci.yml`, `justfile`
implement: haiku
- Precondition, already proven by the lead: with an empty process environment (only PATH),
  a fresh HOME and no `~/.claude`, `npx -y @anthropic-ai/claude-code@2.1.302 plugin
  validate <copy>` passed and `plugin test` ran 7 tests, 7 pass, with no credentials. If the
  job still fails in CI for auth reasons, drop the job, keep `just mod-check`, and record the
  limit in the PR.
- New job `mod` (ubuntu-latest, 10 min): checkout, nix installer (same pinned action),
  `nix develop -c npx -y @anthropic-ai/claude-code@2.1.302 plugin validate contrib/claude-mod`
  and `… plugin test contrib/claude-mod`.
- `just mod-check`: the same two commands via `npx`.

### S10. Docs — `CLAUDE.md`
implement: sonnet
- Rewrite "Claude mod (spike)" as "Claude mod" (source, liveness D3, overlay D4, permission
  override, fallback, default on, socket, store bounds, guard), and update "Hook ingestion"
  (Claude: the mod wins while live), "Background tasks" (mod source and the switch rule),
  "Needs-you" (permission from the mod record; no transcript heal while live; idle_prompt), the
  architecture diagram/project structure line for `claudemod/` and `hub/mod.go`. Keep the
  file's tone; no task/PR references.

### S11. Live harness (lead) — AC8
- Per the spike appendix: private tmux server (`env -i …`, `@crewtmux 1`), houston from this
  branch (`-status-dir <dir>/state -mode tmux -no-opencode -no-auth -debug -addr
  127.0.0.1:17481`), real claude binary with `--plugin-dir contrib/claude-mod
  --setting-sources project,local --settings <dir>/settings.json --model haiku
  --permission-mode default` under `bwrap` binds over `~/.claude/projects`,
  `~/.claude/history.jsonl`, `~/.claude/file-history`. Poll `/api/runs` and
  `/api/debug/claude-mod`; drive: a prompt (thinking → idle), a permission dialog (needs-you,
  timing), Esc on it and a `tool.end` after deny (delivery verified), a background shell
  (chip start → clear). Record timings and an event log excerpt with no session content.
- Cleanup gated on `@crewtmux 1`; confirm pids gone.

## Acceptance mapping

AC1 CI · AC2 S3+S5 · AC3 S3+S4 · AC4 S3+S5 · AC5 S3+S4 · AC6 S4 (existing tests unchanged) ·
AC7 S2, S1/S3, S7, S8 · AC8 S11.
