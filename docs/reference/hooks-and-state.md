# Hooks, Claude mod, background tasks, context meter

Read when changing hook ingestion, the Claude mod, background-task tracking or the context meter.

Moved verbatim from the root `CLAUDE.md`; section headings are unchanged.

## Hook ingestion

`houston hook` (`hook.Dispatch`) accepts two shapes on stdin, auto-detected by
a non-empty top-level `engine` field (Claude's native payload never has one):
Claude Code's native hook payload, or hookyard's normalized envelope
(`{engine, canonical_event, native_event, session_id, cwd, tool_name,
tool_input, native}`, `native` carrying the engine's own payload). In envelope
mode the envelope's own event is authoritative and a CLI `<event>` arg is
ignored (that arg exists so wrapper scripts can name the event for a native
Claude payload; `houston hooks install` registers a bare `<binary> hook` and
relies on `hook_event_name`).

For Claude Code, the mod's fold replaces the hook state's `State`, tool, message and background list while the mod is live for the session (see "Claude mod"); the hook state is the fallback and stays the only source for pi, codex and cursor, and the transcript heal below runs only on that fallback path.

`engine: claude-code` decodes `native` as a Claude `Event`; the event is
`native.hook_event_name`, else the envelope's `native_event` (neither →
no-op), and the file records `agent: claude`. From there it runs the same
`apply` as the native path. Other engines map canonical events onto houston's
internal (Claude) event vocabulary; engine-scoped events (empty
`canonical_event`, `engine` set) are matched by engine and native name first:

| envelope | houston event | notes |
|---|---|---|
| `session_start` | SessionStart | |
| `prompt_submit` | UserPromptSubmit | Turn++ |
| `pre_tool` | PreToolUse | tool + hint from the envelope's normalized `tool_name`/`tool_input` |
| `post_tool` | PostToolUse | |
| `pre_compact` | PreCompact | |
| `turn_end` | Stop (pi: `TurnEnd`) | pi's `turn_end` is intermediate — see below |
| `""` + pi `agent_settled` | Stop | pi's run settled — the final signal, hook state `waiting` (Fleet `idle`) |
| `""` + pi `session_shutdown` | SessionEnd | `reason: reload` is a no-op instead — pi re-emits `session_start` for the same session |
| `""` + codex `SessionEnd` | SessionEnd | |
| `""` + cursor `sessionEnd` | SessionEnd | |
| anything else | — | no-op: no state-file write |

`TranscriptPath` comes from the native payload's `transcript_path`, else pi's
`session_file`.

**Notification and SubagentStop** (`hook.apply`, Claude): `permission_prompt`
and `elicitation_dialog` set `permission` (a dialog that blocks the turn).
`idle_prompt` always sets `waiting` (with its message), even when a permission
is pending: it is the only *hook* signal a denied or Esc-dismissed dialog ever
gets (an interrupt fires no `PostToolUse` or `Stop`), so it heals the card when
it comes. A dialog genuinely left open past `idle_prompt` therefore reads
`idle`, as in base. Any other or untyped type (e.g. `auth_success`) keeps the
state and replaces the message, unless a permission is pending, whose message it
does not displace. `SubagentStop` is a no-op — the main agent is still inside
the `Task` tool — so state, tool and message all stand. `waiting` is only the
hook state value: Fleet maps it to `idle` (see "Needs-you").

**Dismissed dialogs (transcript heal).** A denied or dismissed dialog usually
gets no `idle_prompt` either: Claude Code delays the `permission_prompt` ~6 s
after `PreToolUse` and fires nothing when the user answers "No" (before or after
that Notification). The hub therefore heals the hook state to `waiting` from the
transcript (`Session.applyHook`, recomputed from the raw hook state on every
state load and transcript sync, never a one-way overwrite) when the tool the
state names (`Tool`) has a `tool_use` in the transcript since the last reset and
no `tool_use` is unresolved (the dismissed tool gets an error `tool_result`):
- `waiting:permission` heals on that alone.
- `tool-running` (the Notification has not fired, or later hook events were lost
  so the file is stale) heals only on a `tool_result` newer than the state's
  `Since`: any result for `AskUserQuestion`, a result Claude Code wrote for a declined dialog (`The user doesn't want to…`) for any other tool; an ordinary failed call is not healed, since the file stays `tool-running` after it and the agent is still working.
  A result with no timestamp, or older than the state, never heals, so a call
  that just started is not mistaken for the finished earlier call of the same
  tool; a normal successful tool never reads `idle`.

A dialog with no tool behind it, or a tool still running, stays `permission`.
The tool bookkeeping resets on the hook `Turn` increment and on a transcript
path change. A first read (after a houston restart or transcript switch) folds
the whole tail in one go with no `Turn` signal, so it also drops unresolved
`tool_use`s at each real user prompt (`TranscriptEvent.Prompt`: user text that is
not `isMeta`, an interrupt marker or an injected `<…>` record), keeping resolved
outcomes; an earlier turn's interrupted tool no longer holds a dismissed dialog
`blocked`.

The heal is the fallback: while the Claude mod is live for the session it is off,
because a dismissal or interrupt arrives from the mod as `turn.complete` (see
"Claude mod").

**pi turns:** pi fires `turn_end` after every LLM response, not just the
final one, so it maps to houston's internal `TurnEnd` event and always leaves
the card `thinking`. pi's final signal is the engine-scoped `agent_settled`
(mapped to `Stop` → `waiting`, which Fleet shows as `idle` unless the turn
ended on a question), which fires once per run after retries,
compaction and queued follow-ups, and also on an aborted (Esc) run — so an
aborted run settles instead of hanging `thinking`. pi has no
Notification, so a pi run never shows a permission prompt.

The state file records `agent` (`claude`/`codex`/`cursor`/`pi`); an empty
value (legacy files, native Claude hooks, transcript-discovered sessions)
reads as `claude` — the default lives in `hub.mergeStateIntoView`, not in the
hook package.

`Dispatch` serializes its read-modify-write with a `flock` on
`<state-dir>/claude/.lock`, because hookyard fires most events (`post_tool`,
`turn_end`, `prompt_submit`, `session_start`) as detached fire-and-forget
processes that would otherwise race a Read→apply→Write and silently drop an
update. The tmux `display-message` exec stays outside the lock.

The lock serializes writes but doesn't order them — a detached event can
still land after a later one, so a late `post_tool` landing after a `turn_end`
leaves a card `thinking` until the next event corrects it. The same race
applies to pi's final `turn_end` and `agent_settled`: `apply`'s `EventTurnEnd`
case guards against this by skipping the `thinking` transition when the state
is already `waiting` — a `turn_end` after the run settled can only be a late
one.

houston only sees events hookyard's manifest subscribes it to. The
engine-scoped `pi:agent_settled`, `pi:session_shutdown`, `codex:SessionEnd`
and `cursor:sessionEnd`, and the canonical `pre_compact`, must all be named
in the manifest's `events` (the nix-config houston manifest) — otherwise a pi
run never leaves `thinking` when it settles, a pi run that exits while its
pane stays open (e.g. back to a shell) is never ended, a codex/cursor session
that ends stays `waiting` (Fleet `idle`, never ended), and `compacting` never shows.

lazytmux does not set `@claude_status` on pi (or codex/cursor) panes, so the
tmux layer never lists them on its own — they reach Fleet through the hooks
layer only, keyed by the same pane id, so the tmux layer still supplies
branch/crew enrichment and terminal caps. `runs/crewjoin.go`'s `resolvePane`
accepts agent-detect's `@agent_screen` panes too, so a pi crew worker's bus
record joins its pane (see "Crew-layer pane join").

The hub's transcript parser is Claude's: a pi session file yields token,
context and cost counts (see "Context meter and pi spend") but no trail.

`SessionState.PID` is the hook's parent process — under hookyard that is the
router, not the agent — and isn't read anywhere in houston.

## Claude mod

`contrib/claude-mod/` is a read-only Claude Code hooks module (loaded with `claude --plugin-dir contrib/claude-mod`; nix-config does not ship it yet, that change is pending). It posts session, turn, tool, permission and background-task records to houston over a Unix socket, and houston folds them (`claudemod/`) into the Claude run's state and background tasks while the mod is live for a session. Hooks plus the transcript stay the fallback and the only source for pi, codex and cursor. Design: `docs/superpowers/specs/2026-10-18-claude-mod-primary-source.md`.

- **Default on.** `-claude-mod=false` disables it. A listen failure (a live socket on that path because another houston serves the same status dir, a non-socket path) logs a warning and houston runs hooks-only, so a dev instance beside the service still starts. With no mod connected there are no records, so no session is ever live and nothing changes.
- **Socket** `<status-dir>/claude-mod.sock`: bound in a private 0700 temp dir beside it, chmod 0600, then `os.Link`ed into place, so it is never reachable at wider perms and an `EEXIST` (another houston won the race) refuses without touching the other's socket; the temp name and dir are then removed. A live socket or a non-socket path is refused, a dead socket is replaced. The mod resolves the same dir at run time (`$HOUSTON_STATE_DIR`, else `$HOUSTON_STATUS_DIR`, else `$HOME/.local/state/houston`), so `-status-dir` must match.
- **Records**: `POST /v1/record`, one JSON object `{v:1, sid, boot, seq, ts, ev, …}` of at most 16 KiB; `seq ≥ 1`, `ts ≥ 1` (ms), `boot` minted per module load (at most 64 bytes; `sid` at most 128: `Decode` rejects longer). Answers 204 (stale records included), 400, 413, 405 or 404, never with a body. Event names and fields: `contrib/claude-mod/README.md`.
- **Mod side**: a hook only builds its record and queues it (at most 256, oldest dropped); a `$.clock.every(200)` timer drains it in order with unawaited `$.http.fetch` to `http://houston.invalid/v1/record` over the socket (the `.invalid` host is never resolved), so a `tool.end` posted from a dispatch the engine abandoned (an interrupt) still goes out. `post` starts the timer with the posting hook's `$` when none runs, and restarts it from the next post once it has been silent for 2 s (a refused period ends an interval). Each record carries the session generation it was built in (bumped at `session.start` and after `session.end`), so a record queued before `/clear` is sent under the old sid. A record leaves the queue only once its sid lookup completes: a failed `$.session.id()` leaves it queued for the next tick, and one whose generation's sid is not known is dropped unsent. A drain stuck for more than 5 s is replaced (a drain run counter stops the old one sending, so nothing goes out twice). `session.end` posts its record and then flushes the queue itself, awaiting a drain within the engine's exit bound, since the timer may not outlive an exit. After 15 s (75 ticks) with nothing queued the timer posts a `heartbeat` (no fields), so an idle session still proves the mod is alive. `session.compact` posts `compact`, and `compact.end` once `next` resolves or throws. Size caps: a Notification `message` is cut to 500 and `stop` tasks to 32; a body over 15000 bytes sheds `ask`, `tasks`, `last`, `input`, `message` in that order until it fits (houston answers 413 above 16 KiB). `task.end` is parsed only at top-level `<task-notification>` boundaries, from the header (before `<summary>`/`<event>`), and only for terminal statuses.
- **Store** (`claudemod.Store`): per session a seq-ordered window (64) of unfolded records, so a late record folds where its seq puts it (`reordered`); a duplicate, a seq at or below what the window already folded, or a record of a retired boot is `stale` (a session remembers at most 16 retired boots; a late record of an older one falls to the `bootDelaySlack` rule). A new boot continues the previous boot's fold (a hot reload keeps the session) and retires it. An unseen boot whose first `ts` predates the current boot's is a delayed record of an older boot only while the current boot was first seen less than `bootDelaySlack` (5 s, houston's clock) ago; later it is a reload whose clock stepped back and replaces the current boot. Only applied records refresh the eviction clock. A session is evicted 24 h after its last applied record (ended or idle alike; the sweep runs at most once a minute inside `Apply` and from the hub's 2 s tick, `Store.Sweep`), and the store holds at most 1000 sessions (the least recently applied goes). An applied record or an eviction marks its sid dirty and signals `Store.Notify()`; the hub's `Run` loop takes `TakeDirty()`, reads each `Store.Session`, and recomputes and broadcasts that session at once, so Fleet sees a permission within milliseconds rather than on a tick.
- **Fold** (`claudemod/fold.go`) separates the main loop's phase from a pending permission. A subagent's records (`agent` set) never move the main phase: their `tool.start`/`tool.end`/`tool.check` track their own calls, and their `permission` record or `permission_prompt`/`elicitation_dialog` Notification raises the dialog.
  - Phase: `session.start` → `starting`; `turn.start` → `thinking` (turns++); `tool.start` → `tool-running` (in-flight id added); a `tool.end` of an in-flight id removes it and returns the phase to `thinking` when no main call is left; an id not in flight changes no phase (it can still clear a matching dialog and start or stop a background task), so a late `tool.end` after `turn.complete` never reopens the turn; `turn.complete` (any reason: answer, abort, refusal, error) → `waiting`; `compact` → `compacting`, remembering the phase it interrupted; `compact.end` restores that phase (`thinking`, `tool-running` or `waiting`, else `waiting`), and a late one after the phase moved changes nothing; `session.end` → `ended`, and while the phase is still `ended` the store folds the whole window into the base, so a later record at or below the highest folded seq is stale while a higher seq still applies. `heartbeat` folds nothing but counts as applied (`Updated`, the eviction clock, dirty), but not as activity (`Active` and `MainUpdated` stay). Two clocks: `Updated` (ms, the newest applied record of any kind) is liveness only; `Active` (ms, the newest applied non-heartbeat record, any agent) is what the view's age comes from, so an idle card ages instead of reading seconds old forever.
  - Permission: a `tool.check` with decision `ask` remembers `(agent, tool, id)` as the candidate (`PermissionRequest` carries no tool id); a `permission` record sets the dialog with its tool, hint and, when tool and agent match, the candidate's id; a `permission_prompt`/`elicitation_dialog` Notification sets it only when none is pending, and supplies the message only to a pending dialog of the same agent (the mod's Notification record carries the raising agent's id, so its text lands on the pending dialog of that same agent, main loop or subagent). A new dialog keeps the previous one's message only when it is the same dialog (agent, tool, id). While one is pending the view state is `permission`, else the phase. It clears on `tool.end` of its id (with no id: same agent and tool; with neither, e.g. an MCP elicitation with no `ask` candidate: a `tool.end` of the same agent that leaves that agent no call in flight, so a parallel call ending first does not clear it), `turn.start`, `turn.complete`, `compact`, `session.start`, `session.end`. `idle_prompt` and every other Notification change nothing: a dismissal arrives as `turn.complete`, so an `idle_prompt` while a dialog is still up must not hide it.
  - `stop` (main): `last` (final 300 characters of the assistant text) is kept and `stopSeen` set until the next `turn.start`.
  - Background: a main-loop `tool.start` of a `bg` Bash or a Monitor records its hint and `ts`; a non-error `tool.end` carrying `task` adds the task (`shell` for Bash, `monitor` for Monitor, whose `expires` is the end `ts` + `timeout_ms`, none when persistent); a non-error `TaskStop`/`KillShell` `tool.end` carrying `stop_task` drops it; `task.end` drops each listed task (the mod lists only notification parts with a terminal `<status>`, so a monitor's per-event line ends nothing). A main-loop `stop` carrying `tasks` is a membership snapshot: a known entry stays whole (the snapshot calls a monitor a shell) if listed `running`/`pending`, else drops; an unknown listed `shell`/`monitor` is added with no expiry; other types are ignored. This catches a UI kill at the next turn end. `tasks` absent leaves the list.
  - `Known`: a main-loop `session.start`, `turn.start`, `tool.start`, `tool.end` of an in-flight id, `permission`, `turn.complete`, `compact`, a `compact.end` that ended a compaction, or `session.end` folded since houston started. `BGKnown`: a main-loop `stop` with a `tasks` array folded since houston started (not `session.start` alone: where an organization manages Claude Code no `stop` arrives, and a UI-killed shell would never clear).
- **Liveness** (`hub.Session.modLive(now)`): live ⟺ `Known` and the mod's newest record (any kind, a `heartbeat` included; `mod.Updated`) is under `modLiveWindow` (45 s) old. The mod posts a `heartbeat` from its timer after 15 s with nothing queued, so an idle session still answers and three missed heartbeats mean it stopped (unloaded, broken by an engine update, a failed reload). The hub's 2 s tick (`tickMod`) re-evaluates every session with mod data, so a dead mod falls back to the hook path within about 47 s with no traffic. Liveness is the mod's own recency and never the hook file's: a late or fresh hook write does not decide it (hook events are detached, so a `post_tool` can land after `Stop` or a prompt before the mod's queued record). An earlier design (hook state ahead of the mod for a 10 s grace) retired a healthy mod on a late hook write and let a stream of hook writes keep a dead one alive. The one hook-side exception: a hook state `ended` written at or after the newest mod record's second ends the mod's say for that session, so a lost `session.end` cannot keep a pane-owning card alive. `session.start` alone is not the test: `/clear` begins the next session with `turn.start`, and a restart sees no `session.start`; a houston restart mid-session therefore reads the hook path until the next state-defining record. Under managed Claude Code the `permission_prompt` Notification has no mod record, so the hook-permission override below is what shows the dialog.
- **Overlay** (`hub/mod.go`; `Session.refreshView` is the one place every view recomputation goes through, so the sources switch together and are never both counted): the mod feeds the same `hub.Session` the hooks layer reads, so `runs/hooksource.go`, the foreign-pane guard, the ended-session disown and the one-key-per-session rule apply unchanged.

  | field | mod live | otherwise |
  |---|---|---|
  | `State`, `Tool`, `ToolInputHint`, `LastMessage` | mod fold (`LastMessage`: the Notification message, else `Claude needs your permission to use <tool>`, only while a dialog is pending) | hook state |
  | `Since`, `UpdatedAt` | mod's last state change / max(hook, mod `Active`) | hook |
  | `Asks` | `questionTail` of the Stop record's `last` once a main-loop `stop` was seen this turn, so it shows before the transcript tail | transcript |
  | `Background` | mod list once `BGKnown` | transcript `bgTracker` |
  | dismissed-dialog heal | off (`turn.complete` covers dismissals and interrupts) | on |

  Trail, tokens, context, spend and chat stay transcript-derived for every session (the `turn.complete` usage sums the turn's requests, so it is not the context size). A session with no hook state (mod only) gets cwd, transcript path, pane and `tmux_server` from the mod's records (`session.start`, `turn.start` and every classic record carry `pane`/`tmux_server`, read once from `$TMUX_PANE`/`$TMUX`), `UpdatedAt` from the newest non-heartbeat record's `ts` (`Active`), and `Turn` from its main `turn.start` count (which also resets the trail); the hub creates a `claude` session for it and tails its transcript, so `paneForeign`/`paneGone` work on it as on a hook session. Eviction drops the overlay, and deletes a session known only from mod records (as a removed state file is), so `HookSource` emits `Gone`.
- **Hook permission wins** while the mod reads `tool-running` and the hook state is `waiting:permission` written at or after the newest main-loop mod record's second (no `agent`, not a `heartbeat`, which advances only the liveness time; a subagent's records must not hide the main loop's dialog): where an organization manages Claude Code, a user-installed plugin's classic hooks do not run, the mod sees no `permission`/`Notification`/`Stop`, and only the `permission_prompt` Notification names the dialog. It applies only while the mod is live. Once the mod moves on (`tool.end`, `turn.complete`) it is newer and wins again. A hook permission while the mod reads `waiting` or `thinking` is ignored.
- **Residuals**: after a houston restart the first known-state record folds without the in-flight ids of earlier calls, so a `tool.end` of such a call is ignored and the phase can read `thinking` while a parallel earlier call still runs, until the next main-loop record. A mod that dies between heartbeats can be trusted for up to about 47 s after its last record. A reload of the mod continues the previous boot's fold. Where Claude Code is managed without classic hooks there is no `Stop` record, so `Asks` stays on the transcript and `BGKnown` never turns true (background stays on the transcript fold). After a permission is approved no record fires until the call's `tool.end`, so a long approved tool reads `blocked` while it runs.
- **Privacy**: beyond ids, names, states and counts, the records hold the last 300 characters of the final assistant message, tool-input hints (≤ 200 characters per field), the cwd and transcript path, the tmux pane id and server pid, AskUserQuestion questions, option labels and whether an option has a preview, background task descriptions (≤ 200) and the Notification message. No prompt text and no tool output. Records go only to the local socket.
- **Read-only guard**: every hook is `($, e, next)`, passes its own `e` to `next` and returns `next`'s result; the only calls on `$` are `$.http.fetch` (never awaited, errors swallowed, houston's answer never read), `$.env.get`, `$.session.id` and `$.clock.every`. `claudemod/modguard_test.go` scans `register.ts` and fails CI on: a forbidden API or name; a `$.noun.method` call outside those four (every allowed call must also be found, so a scan that goes blind fails); bracket access to `$` or `$` aliased or destructured; a bare `$` other than a hook-head parameter, a `$: EngineInterface` parameter, or an argument to a function the file declares with `$: EngineInterface` at that position (so `$` cannot reach an untyped helper); an `EngineInterface` parameter not named `$`; a hook not shaped `on('name'[, matcher], ($, e, next) => …)`; a `next(` argument other than `e`; a spread or property write of `e` or of a variable bound from `next(e)` (`const`/`let`/`var`, or a plain assignment, parenthesized or not, awaited or not; one level of alias through `await <that name>` is followed); a `.then`/`.catch`/`.finally` on `next(e)` or on a bound name; a comma right after `next(e)` (a sequence expression discards the result); `Object.assign`/`defineProperty`/`defineProperties` or `Reflect.set`/`defineProperty` on `e` or a `next(e)` result inside a hook; a hook returning an object literal (parenthesized or not); a hook that never calls `next(e)`. Comments are stripped and `?.`/`!.` normalised first; strings are not exempt. It is a tripwire, not a proof: it cannot see an indirect rewrite through a helper or an alias of `e`, so review keeps the contract, and the call list `claude plugin validate contrib/claude-mod` prints is authoritative.
- **CI and local check**: the CI job `mod` runs `just mod-check`, the one place the version lives (`@anthropic-ai/claude-code@2.1.302` through `npx -y`, the version the mod is written against; neither step needs credentials). The recipe runs `claude plugin validate contrib/claude-mod` and fails when its `calls:` line names any call other than `$.clock.every`, `$.env.get`, `$.http.fetch`, `$.session.id`, then `claude plugin test contrib/claude-mod`.
- `GET /api/debug/claude-mod` (registered only while the listener is up, behind the auth gate) shows the store as `{"sessions": [...]}` (state, `known`, `bg_known`, in-flight ids, `applied`/`stale`/`reordered`).

## Background tasks

A Claude Code run's outstanding background shells and monitors ride on `Run.Background` (`[{id, kind: "shell"|"monitor", hint, since}]`), shown as a `N bg` chip on the Fleet card and a list in the Chat status strip (`RunStatusStrip`). They are informational: they never change `State`. A turn that ended with a shell still running keeps its hook/tmux verdict (`idle`/`blocked`), because `State` describes the agent's turn, `blocked` is reserved for a human being required, and `running` would claim the agent is working.

- **Mod source**: while the Claude mod is live for the session and its list is known (a main-loop `stop` with `tasks` folded since houston started; `claudemod/fold.go`, see "Claude mod"), the list comes from the mod and the transcript fold below is not consulted. The two are never unioned: the switch is per view computation, so after a houston restart the transcript's list stands until the next turn end, and a UI kill clears at that `stop`.
- **Otherwise the source is the transcript**, folded by `hub/background.go`'s `bgTracker` inside the hub's existing 2 s tail. It folds from byte 0 on every houston start, so it is restart-safe with no persisted state, and needs no tmux capture. The first read of a transcript (`hub/initialread.go`) does not parse every line: it parses only a tail (256 KiB, doubled until it holds enough to determine trail, asks and token counts; once the tail would pass half the file, the whole file is read instead; a pi file, which has no `tool_use` events, is always read whole) and folds background tasks, the context peak and pi spend over the bytes before it with a pre-pass that decodes only lines containing a substring one of them must contain (`run_in_background`, `Monitor`, `TaskStop`, `KillShell`, `task-notification`, the id of a still-pending start/stop, or `"usage"`), then applies the tail's events in file order; the result equals a full replay. Hooks were rejected (no event fires on completion or on a UI/timeout kill, and the hookyard manifest would need new subscriptions); the pane's `N shell still running` text was rejected (a capture per poll, no per-task detail, nothing for monitors).
- **Start**: a `Bash` tool_use with `run_in_background: true`, or a `Monitor` tool_use, whose non-error tool_result names the task id (`with ID: <id>` / `(task <id>,`). A failed launch is never tracked.
- **End**: a `<task-notification>` carrying a `<status>` (completed/failed/killed/stopped), matched by every `<task-id>` and `<tool-use-id>` in it (a resume's orphan summary lists many ids), read from either the `user` record or the `queue-operation` enqueue copy; or a non-error `TaskStop` result. A notification with no `<status>` is a monitor's per-event line and ends nothing.
- A non-persistent `Monitor` times out (`timeout_ms`) with no transcript marker, so the tracker expires it itself (`list(now)`; the hub re-checks on every 2 s tick even when the file did not grow). Shells have no such lifetime.
- `ReadTranscriptFrom` stops at the last complete line: consuming a half-written start/end record would lose it for good.
- Claude only (`agent == claude`); dropped when the run is `done`/ended. Not covered: background `Agent`/Task subagents, and anything started inside a subagent's own transcript. A session whose Claude was killed without a notification or `SessionEnd` (while its pane stays open) can show a stale chip until the run ends; shells that outlive a `/clear` belong to the new session id and go uncounted.

## Context meter and pi spend

`Run.Context {used, limit?}` and `Run.SpendUSD` (pi only) feed the Chat status strip (`RunStatusStrip`: `142k / 200k ctx`, a percentage bar when the limit is known, `$x.xx` for pi). The Fleet card does not show them: it is already dense and the meter belongs with the session detail.

- **Source is the transcript**, in the hub's existing tail (`hub/transcript.go` `parseLine` → `applyTranscriptEvent`), no extra file pass. `used` is the latest assistant message's uncached + cached input: Claude `input_tokens + cache_read_input_tokens + cache_creation_input_tokens`, pi `usage.input + cacheRead + cacheWrite`. The latest wins (it shrinks after a compaction), never the maximum. A pi aborted message has usage but no content blocks; it still counts.
- **Limit** (`hub/context.go`): a small model-id prefix table (Claude families 200k) plus `[1m]` ids at 1M. An unknown model, or a window smaller than the session's peak use (a 1M run reported under its base id), has no limit; houston never guesses one. The limit is checked against the session's peak use, not the latest, so it does not return after a compaction. Residual: Claude Code writes the API model id with no `[1m]`, so a 1M session reads against 200k (an over-full bar) until it first passes 200k, then shows no limit.
- **Spend**: pi only, the running sum of each message's recorded `usage.cost.total`. Claude records no cost and houston keeps no price table, so Claude runs have no `spend_usd`. A first read folds the sum and the peak over the bytes before its tail (see "Background tasks"), so both are restart-safe.


