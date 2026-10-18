# Claude Code mod as houston's primary Claude source (#303)

Follow-up of the #239 spike (`2026-10-18-claude-mod-spike-report.md`). The read-only mod
in `contrib/claude-mod/` becomes the source of Claude run **state** and **background tasks**
whenever it is live for a session; the hook + transcript path stays the fallback.
Out of scope: the nix-config change that ships the mod (the PR body says what to add),
permission-dialog option labels and answering, non-Claude engines.

## Decisions

### D1. Merge point: an overlay in the hub, not a new `runs.Source`

The mod feeds `hub.Session`, the same object the hooks layer already reads, as an overlay on
the hook state. `runs/hooksource.go` keeps consuming `hub.SessionView` unchanged.

Why not a separate `runs.Source`:
- The registry merges layers field-wise, first non-empty wins. A mod layer would have to win
  `State`, `Activity` and `Background` over hooks but lose `Trail`, `Tokens`, `Context`,
  `Session` and chat to them, and an *empty* mod background would be overwritten by the
  transcript's: exactly the double-count / stale-chip the issue warns about.
- `Question` and `blocked` derivation (`AskUserQuestion`, `?` turn ends, dialog text) lives in
  `runFromSessionView`, over one `SessionView`. Feeding the mod into that view reuses it.
- The foreign-pane guard (`paneForeign`: `tmux_server`, server start time) and the #120 disown
  (`normalize` drops coordinates of an `ended` session) run on `SessionView` in
  `HookSource`. With the overlay they apply to mod-driven sessions with no new code.
- One key per session (pane id, else `claude/<sid>`) as today, so no second card.

### D2. Joining a pane

Coordinates come from the hook state file when one exists for the session id (it has the
tmux session/window too). Otherwise from the mod itself: the mod reads `TMUX_PANE` and
`TMUX` with `$.env.get` (already an allowed read) and sends `pane` and `tmux_server` (the
pid field of `$TMUX`, indexed from the right as `hook.tmuxEnv` does) on `session.start`,
`turn.start` and every classic record,
and with `cwd` and `transcript` (from classic events: `transcript_path`, `cwd`) whenever it
learns them. A mod-only session's `UpdatedAt` is the newest record's `ts` (seconds), so
`paneForeign`'s start-time rule and `paneGone` work on it as on a hook session. The hub
creates a session for a mod-only sid (agent `claude`) and tails its transcript like any other.

### D3. When the mod is live

The mod is **live** for a session when both hold:
1. **Known state:** since houston started, the store has folded a record of the current boot
   that sets a definite main-loop state (`session.start`, `turn.start`, `tool.start`,
   `tool.end` of an in-flight id, `permission`, `turn.complete`, `compact`, `session.end`).
   A houston restart mid-session therefore uses the hook path until the next such record.
2. **Hook not ahead:** the hook state has not led the newest mod record for the whole
   `modLagGrace` (10 s): it is ahead when `hook.UpdatedAt > mod.Updated/1000 + 1` and
   `now >= hook.UpdatedAt + 10`. Every hook event houston handles has a matching mod record,
   but the mod sends from a 200 ms timer queue while the hook writes its file synchronously,
   so a hook write (a `UserPromptSubmit` after an idle spell) routinely lands a moment before
   its record; the grace gives the mod that long to answer. The `+ 1` absorbs the hook's
   whole-second `UpdatedAt` against a record of the same event a few ms earlier across a
   second boundary. A mod that has not answered a hook write within the grace stopped
   posting (unloaded, broken by an engine update, a hot reload that failed), and houston
   falls back by itself: the hub's 2 s tick recomputes every session with mod data, so the
   fallback lands within about 2 s of the grace running out, with no record traffic.

Why not "a record within N seconds": an idle session posts nothing for hours, and falling
back would restore the hook state in exactly the cases the mod exists to fix (a stale
`thinking` after the turn ended, a dismissed dialog). Why not "`session.start` seen" alone:
`/clear` starts the next session with no `session.start` (first record `turn.start`), and a
houston restart sees none.

There is no expiry without a hook write: liveness changes when a record lands, or when a
hook write has gone unanswered for the grace.

**Hook permission while live.** Where an organization manages Claude Code, a user-installed
plugin's `classic.*` hooks do not run, so the mod gets no `permission`, `Notification` or
`Stop` record while the hook path still gets the `permission_prompt` Notification (~6 s after
the dialog opened). That write has no mod record to answer it, so a dialog left open past the
grace retires the mod until its next record (`tool.end` on approval, `turn.complete` on a
dismissal); the hook path shows the same permission meanwhile. So while the mod is live and its view is
`tool-running` (a dialog can only be up while a call is in flight), a hook state of
`waiting:permission` written at or after the newest **main-loop** (no `agent`) mod record's
second wins (a parallel subagent's records must not hide the main loop's dialog) the state, tool,
hint and message. Once the mod moves on (`tool.end`, `turn.complete`), it is newer and wins
again. A hook permission while the mod reads `waiting`/`thinking` is ignored: the mod saw the
turn end or the call finish after it.

**Residual after a houston restart.** The first known-state record folds without the
in-flight ids of calls started before the restart, so e.g. a `tool.end` of such a call is
ignored and the phase can read `thinking` while a parallel earlier call still runs, until the
next main-loop record corrects it.

### D4. What the overlay replaces when live

| field | live mod | otherwise |
|---|---|---|
| `State` | mod fold | hook state + transcript heal |
| `Tool`, `ToolInputHint` | mod (`hook.ToolHint` over the input subset the mod sends) | hook |
| `LastMessage` | permission: the notification message, else `Claude needs your permission to use <tool>`; nothing otherwise | hook |
| `Since`, `UpdatedAt` | mod state change / max(hook, mod) | hook |
| `Asks` | `questionTail` of the Stop record's `last` once a main-loop `stop` was seen this turn; else transcript | transcript |
| `Background` | mod, once its background list is **known** (D6) | transcript `bgTracker` |
| transcript heal | **off** (the mod's `turn.complete` covers dismissals and interrupts) | on |

Trail, tokens, context, spend and chat stay transcript-derived for every session: the
`turn.complete` `usage` is summed over the turn's requests (`TurnUsage` doc), so it is not the
context-window size houston shows, and the mod does not send it.

`Turn`: the hook's when a hook state exists, else the mod's count of main-loop `turn.start`s
(which also drives the trail reset for mod-only sessions).

### D5. The fold (claudemod)

The fold separates the main loop's **phase** from a pending **permission**:
- `session.start` → `starting` (resets inflight, permission, asks).
- `turn.start` (main) → `thinking`; clears inflight, permission, asks; `turns++`.
- `tool.start` (main) → inflight += id; `tool-running`.
- `tool.end` (any agent) whose id is in flight for its agent → removed; main phase becomes
  `thinking` when no main tool is left, else `tool-running`. An id not in flight changes
  nothing (a late `tool.end` after `turn.complete` never reopens the turn: AC2).
- `tool.check` with `decision: "ask"` → remembers `(agent, tool, id)` as the dialog's
  candidate.
- `permission` (any agent, now carrying `agent`) → permission pending with the tool, its hint
  and the candidate id when tool and agent match.
- Permission clears on: `tool.end` of its id (or, with no id, of the same agent and tool),
  `turn.start`, `turn.complete`, `session.start`, `session.end`, `compact`.
  `notification idle_prompt` changes nothing: the mod sees dismissals as `turn.complete`, so
  an idle_prompt while a dialog is still up must not hide it (the hook path's residual).
- `notification permission_prompt|elicitation_dialog` → permission pending if none (keeps the
  message for `LastMessage`).
- `turn.complete` (main) → `waiting`, clears inflight and permission. Any `reason`
  (`answer`, `aborted`, `refusal`, `error`): Esc on a dialog and Esc on a running tool both
  arrive as `turn.complete` (AC3).
- `compact` → `compacting`; `session.end` → `ended`.
- `stop` (main) → `asks = questionTail(last)` is computed in houston, records `stopSeen`.
- Subagent records (`agent` set) change no phase; only their `tool.end`/permission matter.

View state: `ended`/`compacting`/`starting`/`waiting` phases win over a stale permission only
because every one of those transitions clears it; while permission is pending the view is
`waiting:permission`, else the phase.

Seq ordering, boot retirement and the 64-record window stay as in the spike.

### D6. Background tasks from the mod

Fold entries `{id, kind, hint, since, expires}`:
- `tool.start` of a background `Bash` (`bg`) or a `Monitor` records the call's hint and `ts`.
- A non-error `tool.end` carrying `task` (Bash `backgroundTaskId`, Monitor `taskId`) adds the
  task: kind `shell` for Bash, `monitor` for Monitor; hint and since from the matching start;
  a Monitor's `expires` = end `ts` + the result's `timeout_ms` (0 = persistent: never).
- A non-error `tool.end` of `TaskStop`/`KillShell` carrying `stop_task` (the result's
  `task_id`) drops that task at once.
- `task.end` drops each listed task; the mod lists only notification parts with a terminal
  `<status>` (completed/failed/killed/stopped), parsed from the header only (before
  `<summary>`/`<event>`, as `hub/background.go` does), so a Monitor's per-event line never
  ends a task. A Monitor expiry is timed by houston from `expires` (the transcript fold's rule).
- `stop` (main) with `tasks` is a membership snapshot: an entry already in the list is kept
  whole (kind, hint, since, expires: the snapshot reports a Monitor as type `shell`, and a
  Monitor must keep its kind and expiry) if the snapshot lists its id as `running`/`pending`,
  and dropped otherwise; a listed `running`/`pending` id not yet known is added (type
  `monitor` → monitor, else shell; hint from `description`, since from the record `ts`, no
  expiry). Only `shell`/`monitor` types are added (background agents are out of scope, as
  today). This catches a UI kill at the next turn end (AC5). `tasks` absent → list stands.
- **Known:** the list is authoritative once the fold saw a main-loop `stop` with a `tasks`
  array in the current boot (since houston started). Not on `session.start` alone: where an
  organization manages Claude Code no `stop` ever arrives, and a UI-killed shell would then
  never clear; the transcript fold clears it at the next prompt. Until then a live mod still leaves `Background` to
  the transcript tracker, so a houston restart does not hide running tasks until the next turn
  end. The two sources are never unioned: the switch is per view computation (no double count).
- `list(now)` filters expired monitors; the hub's 2 s tick re-evaluates it for every session
  with mod data (also mod-only sessions with no transcript).

### D7. Mod changes (`contrib/claude-mod/hooks/register.ts`)

- **Queue + timer.** `post` only builds the record (`v`, `boot`, `seq`, `ts`, `gen`, `ev`,
  fields) and pushes it on a module-level queue; it makes no `$` call. `session.start` starts
  one `$.clock.every(200, drain)` (once per module load; a reload drops it and `session.start`
  fires again, per the hooks reference). As a fallback, `post` starts the timer with its own
  hook's `$` when none is running yet. `drain` resolves the socket path and session id and fires each record with an
  unawaited `$.http.fetch` (errors swallowed), in queue order. A `tool.end` posted from an
  abandoned `tool.call` dispatch is therefore sent by the timer's dispatch, not the dead one.
  A post before any timer exists stays queued until one starts.
- **Generation per record.** `gen` is stamped when the record is built. `session.end` stores
  its `e.sessionId` as the id of the ending generation before bumping it; any gen bump stores
  the current sid for the old gen. A record resolves its sid from its own gen's entry, else
  (only for the current gen) `$.session.id()`; an older gen with no known id is dropped.
  The wire record carries no `gen`.
- **New fields.**
  - `session.start`: `cwd`, `pane`, `tmux_server`.
  - classic events (`Notification`, `PermissionRequest`, `Stop`): `transcript`, `cwd`.
  - `tool.start`: `input`, the subset `hook.ToolHint` / `backgroundHint` read
    (`description`, `command`, `file_path`, `path`, `pattern`, `url`, `prompt`, and the first
    question's `header`/`question`), each string cut to 200 characters.
  - `permission`: `agent` and the same `input` subset of `tool_input`.
  - `tool.end`: Monitor `timeout_ms` (from the result's `timeoutMs`), TaskStop/KillShell
    `stop_task` (result `task_id`) on a non-error result.
  - `stop`: `last` (the last 300 characters of `last_assistant_message`; houston needs only
    the final line, for the `?` turn-end rule), and each task's `description`. This widens the
    mod's privacy contract (it now sends that tail of assistant text, tool-input hints and
    the cwd/transcript path); the README and CLAUDE.md say so.
  - `tool.start` of AskUserQuestion: each option as `{label, preview?: true}` (not consumed
    by houston yet; it decides the dialog's rows for a later `meta.choices` replacement).
  - `task.end`: only terminal-status parts.
- Still read-only: every hook passes its own `e` to `next` and returns `next`'s result
  unchanged; calls on `$` are only `$.http.fetch`, `$.env.get`, `$.session.id`,
  `$.clock.every`.

### D7a. Delivery to the hub (push)

`Store.Apply` marks the record's sid dirty and, after releasing its lock, does a
non-blocking send on a 1-buffered `Notify()` channel. The hub's `Run` loop selects on it,
takes the dirty set (`Store.TakeDirty()`), reads each `Store.Session(sid)` outside the hub
lock, then recomputes and broadcasts that hub session at once. A permission record therefore
reaches Fleet within the hub → registry → SSE latency (milliseconds), not a 2 s tick: the
"needs-you within 1 s" acceptance. Eviction marks the sid dirty too, so the hub drops it.

### D7b. Field names checked against Claude Code 2.1.302's mod declarations

`SessionStartInput.cwd`; `TurnCompleteInput.reason`/`agentId`; `ToolCallInput.tool_use_id`,
`agentId`; Bash result `backgroundTaskId`; Monitor input `description`/`timeout_ms`, result
`taskId`/`timeoutMs`/`persistent`; TaskStop input `task_id`/`shell_id`, result `task_id`;
classic base `session_id`/`transcript_path`/`cwd`/`agent_id`; `PermissionRequest`
`tool_name`/`tool_input`/`permission_suggestions` (no `tool_use_id`, hence the `tool.check`
candidate); `Notification` `notification_type`/`message`; `Stop`
`last_assistant_message`/`background_tasks[{id,type,status,description}]`; `$.clock.every(ms,
fn): Timer` (fn takes no arguments; timers run until cancelled or the module reloads, the
reference's prescribed way for work that outlives a dispatch). Whether `$` calls made from the
timer callback survive an abandoned `tool.call` dispatch is verified by a mod test and in the
live harness (AC8); the queue is capped at 256 records (oldest dropped).

### D8. Default on

`-claude-mod` defaults to **true**; `-claude-mod=false` disables it. The socket stays
`<status-dir>/claude-mod.sock`, 0600, bound privately and **linked** into place. A listen
failure (another houston already serves that status dir, a non-socket path) is logged as a
warning and houston runs without the listener (hook path only) instead of failing startup,
so a second instance on the same status dir (a dev run beside the service) still starts.
With no mod connected nothing changes: no records → never live → hook path (AC6).

### D9. Store bounds

Ended sessions are evicted `hub.DefaultPruneTTL` (24 h) after their last applied record,
idle ones after 24 h; the store holds at most 1000 sessions (the least recently applied goes).
The sweep runs from `Apply` and from the hub's 2 s tick (`Store.Sweep`), so eviction happens
with no record traffic too. On eviction the hub drops its overlay; a session the hub created only from mod records is
deleted from the hub (as a removed state file is) so `HookSource`'s resync emits `Gone`.

### D10. Hardening (from the spike review)

1. `Listen` binds in a private temp dir, chmods 0600 and `os.Link`s into place (EEXIST
   refuses), then removes the temp name and dir. Two houstons on one status dir: the second
   fails, the first stays reachable, and the second's failure removes nothing of the first's.
2. `Decode` rejects `ts < 1`. `lastApply` (eviction clock) is stamped only for applied
   records, so stale/retired records cannot keep a session alive. A clock step back across a
   reload must not retire the new (live) boot. A late record of a boot houston already
   replaced is caught by the `retired` set, as today. The `ts` comparison is left only for a
   boot never seen: an unseen boot whose `ts` is older than the current boot's first `ts` is
   taken for a delayed record of an older boot **only while the current boot was first seen
   (houston's monotonic clock) less than `bootDelaySlack` (5 s) ago**; deliveries take
   milliseconds (spike: max 61 ms), so later than that it is a reload whose clock stepped
   back, and it replaces the current boot. Test: boot A at ts 1000, 6 s of houston time later
   boot B at ts 900 → B is live and folds; a record of a never-seen boot C at ts 800 within
   5 s of B's first sight is stale.
3. Mod `gen` per record (D7).
4. `modguard_test.go` widened: strip `//` and `/* */` comments, normalise `?.`/`!.` to `.`,
   require every hook to be `(… $, e, next …)`-named and every `EngineInterface` parameter to
   be named `$`, require every `next(` call's argument to be exactly the hook's `e`, flag
   spreads or property writes of `e` or of a variable bound from `await next(e)`, and flag a
   hook returning an object literal. Tests: `?.`, renamed `$`, event rewrite, result rewrite.

## Testing

- claudemod fold tests: AC2 (late `tool.end` after `turn.complete`), AC3 (Esc dialog, Esc
  tool: one `turn.complete` → waiting), AC4 (permission → `waiting:permission`; `tool.end` /
  `turn.complete` clear it; subagent dialog), AC5 (bg start, completion, TaskStop target,
  Monitor expiry, UI kill cleared by the next stop), AC7 hardening.
- hub tests: overlay wins when live, no heal while live, hook-ahead fallback, mod-only session
  created and joined by pane, background switch without double count, restart (unknown bg)
  keeps transcript bg, eviction deletes a mod-only session.
- runs test: the composed run reads `idle` (not `thinking`) for AC2 and `blocked` for AC4.
- AC6: every existing hook/background/hub/runs test passes unchanged.
- Mod tests (`claude plugin test`): queue delivery after an abandoned dispatch (a `tool.end`
  posted after the call's dispatch ended is still fetched by the timer), gen per record across
  a reset, new fields, terminal-only task ends, read-only pass-through. CI runs
  `claude plugin validate` and `claude plugin test` through
  `npx -y @anthropic-ai/claude-code@2.1.302` in a separate job, provided the plan's first
  check proves both run with no credentials (`HOME` a fresh temp dir, no `~/.claude`); if
  either needs auth, the job is dropped, a `just mod-check` recipe runs them locally, and the
  PR records the limit.
- AC8: live run in the spike's isolated harness (private tmux server, real binary with
  `--plugin-dir`, bwrap binds), evidence in the PR body.
