# Claude Code mod spike: report (#239)

**Recommendation: promote.** Make the read-only mod houston's primary source for Claude
**turn state** and **background tasks**. Keep the pane read for **permission dialogs** and for
**answering** anything. Keep the hook path as a fallback for Claude sessions that run without
the mod. hookyard stays the source for pi, codex and cursor. A ready-to-file follow-up issue is
at the end.

| item | result |
|---|---|
| 1. Stale `thinking` after a late hook event | **Fixed.** Records carry a per-load sequence number, so a late record folds in its place. The mod also gets a turn end for interrupts and dismissed dialogs, where the hook path gets nothing. |
| 2. Pane scraping for numbered choices | **Partial.** AskUserQuestion is fully covered. For a permission dialog the mod knows the tool, the input and that a dialog is up (6 s before the hook path does), but not the option labels Claude Code draws. Answering still needs the pane check. |
| 3. Transcript fold for background tasks | **Mostly.** Start, natural completion and Monitor expiry arrive as events. A TaskStop or UI kill sends no event; it is only seen in the next turn end's `background_tasks` snapshot, which is no worse than the transcript today. |

Setup: Claude Code 2.1.302, the mod in `contrib/claude-mod/`, houston with `-claude-mod`
(`claudemod/`). Every live run was a throwaway session in an isolated harness (appendix), driven
with hand-written prompts on haiku. The hook path and the mod path observed the **same session**:
a `--settings` file registered `houston hook` for the classic events.

## How the mod path works

- The mod hooks `session.start/end/compact`, `turn.start/complete`, `tool.call` (before and after
  `next`), `tool.check`, `prompt.submit` (task notifications only), and `classic.Notification`,
  `classic.PermissionRequest` and `classic.Stop`.
- Each hook posts one compact JSON record to `<state-dir>/claude-mod.sock` with
  `$.http.fetch(…, { socketPath })`. The post is not awaited and errors are swallowed, so
  houston being down never touches the session.
- Every hook hands `next` the event it received and returns what `next` resolved to. The module's
  only `$` calls are `$.http.fetch`, `$.env.get` and `$.session.id` (`claude plugin validate`).
  houston's reply is never read.
- Each record carries `boot` (minted per module load) and `seq` (per-load counter, taken in hook
  order). houston keeps a seq-ordered window of 64 records per session, so a record that arrives
  late folds where its seq puts it. It counts `stale` (duplicate or older than the window) and
  `reordered` (arrived below the highest seq seen). This replaces the spec's original
  "drop a lower seq" rule: unawaited fetches are separate HTTP requests and could genuinely
  arrive out of order. In the runs below none did.
- Delivery latency, record `ts` to listener arrival, over 52 raw records: min 1 ms, p50 6 ms,
  p95 15 ms, max 61 ms. The max was a `session.start`, which waits for the socket path and
  session id to resolve. Arrival order equalled seq order in every live run (houston:
  `reordered 0`, `stale 0` over the 62 records that arrived of seq 1–63; the missing one is the loss described under item 1).

## 1. Stale `thinking` after a late hook event

**Deterministic test.** `claudemod/compare_test.go` (`TestLateToolEndHookVersusMod`):

- **Hook path.** `hook.Dispatch` gets `UserPromptSubmit`, `PreToolUse`, `Stop`, then a late
  `PostToolUse`, and ends `thinking`.
- **Mod path.** The same turn's records 1, 2 and 4 arrive, then the late `tool.end` (seq 3).
  The store ends `waiting` with `reordered: 1`.

**Live repro.** The settings `PostToolUse` hook was a wrapper that detaches. It waits until the
session's hook state file reads `waiting` (the `Stop` landed), then delivers, imitating a
hookyard fire-and-forget event losing the race. The reorder happened in every turn with a tool.
One-tool turn (`echo hello`), states polled once a second:

```
ms              hook path      mod path
1792328825004   thinking       thinking
1792328826014   tool-running   thinking
1792328827025   waiting        waiting
1792328827166   (late PostToolUse delivered; state before: waiting)
1792328828036   thinking       waiting      <- hook path stuck until the next event
```

houston's record log for that turn (`-debug`; session and boot ids omitted):

```
16:07:04.057 seq=2 ev=turn.start                   state=thinking
16:07:05.614 seq=3 ev=tool.start tool=Bash         state=tool-running
16:07:05.643 seq=4 ev=tool.check tool=Bash         state=tool-running
16:07:05.713 seq=5 ev=tool.end   tool=Bash         state=thinking
16:07:06.654 seq=6 ev=stop                         state=thinking
16:07:06.672 seq=7 ev=turn.complete                state=waiting
```

The hook state file stayed `thinking` (`turn: 1`).

**Beyond the race, two more cases** where the hook path is wrong and the mod path is right:

| case | hook path | mod path |
|---|---|---|
| Esc on a permission dialog before its `permission_prompt` Notification (which comes ~6 s after the dialog) | `tool-running`, indefinitely: no Notification, Stop or PostToolUse fires | `turn.complete` `reason: answer` → `waiting` |
| Esc while an approved tool runs | `tool-running`, indefinitely | `turn.complete` `reason: aborted` → `waiting` |
| "No" on a permission dialog | `waiting:permission` until something else fires | `turn.complete` → `waiting` |

Today the hub heals some of these from the transcript (CLAUDE.md "Dismissed dialogs"). The mod
gets them from an event.

**Earlier dialog detection.** `classic.PermissionRequest` fires when the dialog opens. The
Notification the hook path relies on came 6.0 s later in every dialog: AskUserQuestion
16:08:02.595 vs 16:08:08.593; Bash 16:08:47.958 vs 16:08:53.957. So the mod path shows
`waiting:permission` about 6 s sooner.

**Defect found: records from an abandoned dispatch are lost.** After a denied dialog or an Esc
interrupt, the `tool.end` the mod posts once `next` resolves never arrived. That was 4 of 4
cases (seq 21, 77, 104, 110); the gap in `seq` shows the post was made. The `tool.call` dispatch
had been abandoned, and per the API notes a `$` call is tied to its dispatch's signal. The
folded state is unaffected (`turn.complete` clears in-flight tools), but a production mod should
send from a session-scoped queue drained by a `$.clock` timer started in `session.start`,
rather than from inside a gating dispatch. The follow-up must verify that this delivers.

**Residual (same as the hook path).** After a dialog is approved nothing fires until the tool
ends, so a long approved tool reads `waiting:permission` while it runs. `tool.check` and
`PermissionRequest` both precede the answer; no event marks the approval itself.

**Session end.** `session.end` arrived on `/clear` (`reason: clear`; the next session's first
record is `turn.start`, since no `session.start` fires after a clear), on `/exit`
(`prompt_input_exit`) and on SIGTERM (`other`), despite the hook's 1.5 s budget.

## 2. Pane scraping for numbered choices

Split into the two jobs the pane read does today.

**Display coverage: what the mod sees.**

- **AskUserQuestion: full.** The `tool.call` input carries every question, its options and
  `multiSelect`. Descriptions and previews are there too; the spike sent labels only. Raw record:

  ```json
  {"ev":"tool.start","tool":"AskUserQuestion","id":"toolu_01Wd…",
   "ask":[{"question":"Which fruit do you prefer?","options":["Apple","Pear"]}]}
  {"ev":"tool.check","tool":"AskUserQuestion","decision":"ask"}
  {"ev":"permission","tool":"AskUserQuestion","suggestions":0}
  {"ev":"notification","type":"permission_prompt","message":"Claude needs your permission"}
  ```

  Pane for the same kind of dialog: `1. Red` / `2. Blue` / `3. Type something.` /
  `4. Chat about this`. Rows 3 and 4 are fixed engine rows, derivable from the input. So the mod
  can replace `meta.choices` for AskUserQuestion.
- **Permission dialog: question yes, options no.**
  - The mod gets the tool and its input (`classic.PermissionRequest`'s `tool_name` and
    `tool_input`; the spike sent only the tool name), `tool.check` `decision: "ask"`, and
    `permission_suggestions` (count 2 for `touch spike-y.txt`, 0 for a `find`).
  - The pane drew four options: `1. Yes`, `2. Yes, and always allow access to <dir> from this
    project`, `3. Yes, and switch to auto mode`, `4. No`. A `find` got three: Yes, auto mode, No.
  - The option list and its order are the engine's own. Option 3 has no counterpart in any
    event. The declarations say outright that "the permission dialog is drawn by the engine
    alone, since its answer authorises an action": it is not a `ui.render` component, so no hook
    sees its rows.
  - So the mod can show *that* a dialog is up, for which tool and command, 6 s sooner. It cannot
    list the numbered choices.
- **Notification message.** Generic (`"Claude needs your permission"`); it adds nothing.

**Answer verification: what stays on the pane.** Answering types keys into the pane. The
`answer/` executor checks the captured dialog before every key batch, and `/prompt` hashes the
dialog's frame so an approval cannot land on a different command. That check is about what is on
screen at the moment keys go in, so a read-only event stream cannot replace it, even for
AskUserQuestion. A mod that answered dialogs would no longer be read-only, and is out of scope by
design (dispatcher authorization model).

**Result.** The mod path removes the scrape for AskUserQuestion display. Permission-dialog
choices and every answer still need the pane.

## 3. Transcript fold for background tasks

Live records (raw capture; ids shortened):

```
bg start   tool.start Bash bg:true
           tool.end   Bash task:"brkhxly2n"                      <- id from Bash's backgroundTaskId
           stop       tasks:[{id:"brkhxly2n",type:"shell",status:"running"}]
UI kill    (x in the task view at ~+9 s)                         <- no record at all
next turn  stop       tasks:[{id:"bp06vj2zb",…}]  (brkhxly2n gone) <- only here does it disappear
Monitor    tool.end   Monitor task:"bp06vj2zb"                   <- id from Monitor's taskId
           stop       tasks:[{id:"bp06vj2zb",type:"shell",status:"running"}]
expiry     task.end   tasks:[{id:"bp06vj2zb"}]   (+3.0 s, no <status>)
           turn.start / stop tasks:[] / turn.complete           <- the notification starts a turn
```

| task event | mod signal | latency | today (`hub/background.go`) |
|---|---|---|---|
| shell start | `tool.end.task` (+ `bg` on start) | immediate | tool_result text in transcript (≤ 2 s tick) |
| shell completes | `task.end` with `status` | immediate | `<task-notification>` in transcript |
| Monitor start | `tool.end.task` | immediate | tool_result text |
| Monitor expiry (`timeout_ms`) | `task.end` **without** `status` | immediate | tracker expires it itself from `timeout_ms` |
| TaskStop | none; next `stop` snapshot drops it | at that turn's end (same turn) | non-error TaskStop result |
| UI kill (task view `x`) | none; next `stop` snapshot drops it | **next turn end** (a quiet session keeps it) | notification row appears with the next prompt ("was stopped by the user") |
| any (snapshot) | `stop.tasks` = `background_tasks` | every turn end | — |

Notes:

- In this build a Monitor expiry *does* deliver a notification, though one without `<status>`.
  The current tracker treats a status-less notification as a per-event line that ends nothing.
  The mod parses only ids and status, so it cannot tell an expiry from a per-event line. The
  spike removes the task on either, which is wrong for a long-lived Monitor that emits events.
  The follow-up must carry enough of the notification (or the Monitor's `timeout_ms`) to tell
  them apart.
- `stop.tasks` (`background_tasks` on the Stop hook) is the strongest signal: an authoritative
  in-flight list at every turn end, kills included. It reports a Monitor of a command as type
  `shell`.
- A UI kill reaches neither path until the next turn. The mod is no worse than the transcript
  here.

**Result.** The mod covers start and end for everything the transcript fold covers, with no
file tailing, a restart-free in-memory model and an authoritative snapshot per turn. The
UI-kill latency and the Monitor event-vs-expiry ambiguity remain.

## Event coverage vs what houston needs

| houston need | today (Claude) | mod event | gap |
|---|---|---|---|
| session started / ended | SessionStart / SessionEnd hooks | `session.start`, `session.end` (`clear` included; SIGTERM arrives) | none seen |
| turn running / ended | UserPromptSubmit / Stop (lost on interrupt) | `turn.start`, `turn.complete` (`answer`/`aborted`/`refusal`/`error`) | none: fires on interrupts |
| current tool | Pre/PostToolUse | `tool.start` / `tool.end` (parallel ids tracked) | `tool.end` lost when the dispatch is abandoned |
| subagent vs main loop | `agent_id` on hooks | `agentId` on turn and tool events | none |
| dialog up | `permission_prompt` Notification (~6 s late) | `classic.PermissionRequest` (immediate), `tool.check` `ask` | no "approved" event |
| AskUserQuestion content | transcript tool input / pane | `tool.call` input | none |
| permission dialog options | pane capture | — | not exposed (engine-drawn) |
| compaction | PreCompact | `session.compact` | not exercised live |
| background tasks | transcript fold | `tool.end.task`, `task.end`, `stop.tasks` | UI kill latency; Monitor expiry vs event |
| last assistant text (`?` asks) | transcript tail | not sent (Stop has `last_assistant_message`) | the follow-up can add it |
| transcript path, cwd | hook payload | classic events carry it | not sent by the spike |
| context and token counts | transcript | `turn.complete.usage` | not sent by the spike |

## Risks

- **Version drift.** The API is marked early access: "this surface may change between releases
  without notice". The mod pins nothing, so a Claude Code update can break it silently: a hook
  that fails is skipped, fail-open, so records just stop coming. Mitigations:
  - `claude plugin validate` and `claude plugin test` in CI, against the installed version.
  - houston treats "no records" as "no mod" and keeps the hook path as fallback.
  - the record carries `v: 1`; a version bump is a houston change.
- **Mod loading.**
  - `--plugin-dir`, or a marketplace install. On this machine the `claude` wrapper already adds
    `--plugin-dir`s (hookyard among them), so shipping the mod is one more entry there.
  - An organization-managed Claude Code skips `classic.*` hooks in a user-installed plugin
    (validator note); `session.compact` likewise. There the mod loses Notification,
    PermissionRequest and Stop: no early dialog signal and no task snapshot. Turn and tool events
    remain.
  - A hot reload re-runs `register` (new `boot`, fresh `seq`). The store starts a new window per
    boot, retires the old boot, and drops a retired boot's late records as stale.
- **Trust.**
  - Any process of the same uid can write to the 0600 socket and spoof records. That only skews
    houston's display; houston must never act on a record.
  - The mod must stay read-only. Today a test plus a grep enforce it: no `$.prompt.*`,
    `$.command.*`, `$.tool.*`, `$.agent.*`, `$.session.append`, `$.env.set`, `$.ui.*`,
    `$.process.*` or `$.fs.*`, and no refusal or rewrite answers.
  - `prompt.submit` and `tool.call` are gating sites. An observer there that throws is skipped
    and the prompt or tool proceeds (fail-open), which is the right failure for an observer.
  - The mod sends tool names, ids, AskUserQuestion questions and labels, and the Notification
    message. It sends no prompt, assistant text, command or tool output.
- **Loss on abandoned dispatches.** See item 1. Folded state is fine; per-tool history is not.

## Follow-up issue (ready to file)

> **Title:** Make the read-only Claude Code mod houston's primary Claude source
>
> The #239 spike (`docs/superpowers/specs/2026-10-18-claude-mod-spike-report.md`) showed that a
> read-only Claude Code mod posting session, turn and tool events over a Unix socket fixes the
> stale-`thinking` race and the dismissed-dialog and interrupt cases, detects permission dialogs
> ~6 s earlier, and covers background tasks at least as well as the transcript fold. Promote it
> from spike to source for Claude runs. hookyard stays for pi, codex and cursor; the Claude hook
> path stays as a fallback.
>
> **Scope**
> - Mod (`contrib/claude-mod/`):
>   - Send from a session-scoped queue drained by a `$.clock` timer started in `session.start`
>     instead of inside gating dispatches; verify a `tool.end` after a denied dialog and after an
>     Esc interrupt is delivered.
>   - Add the fields houston needs: `transcript_path` and `cwd` (from classic events),
>     `last_assistant_message` (Stop), `usage` (`turn.complete`), the PermissionRequest
>     `tool_input` hint (through `hook.ToolHint` rules), and for task notifications enough to
>     tell a Monitor expiry from a per-event line.
>   - Ship it next to hookyard's `--plugin-dir` in nix-config. Run `claude plugin validate` and
>     `claude plugin test` in CI.
> - houston:
>   - A `runs.Source` (or a hooks-layer merge) fed by `claudemod.Store`, keyed by session id and
>     joined to the pane like the hooks layer (`TMUX_PANE` via a classic event's environment, or
>     the existing hook state's pane for the same session id).
>   - Mod state wins over hook state for Claude sessions whose mod is live, meaning a record
>     within N seconds or a `session.start` seen. Otherwise fall back to the hook path.
>   - Map `waiting:permission` from `permission`; clear it on `tool.end` or `turn.complete`.
>   - Background tasks from `stop.tasks` + `tool.end.task` + `task.end`, replacing
>     `hub/background.go` for Claude when the mod is live.
>   - Make `-claude-mod` default on, or always on behind an env opt-out. Bound the store
>     (evict ended sessions after the prune TTL).
> - Out of scope: permission-dialog option labels and answering (the engine draws the dialog; the
>   pane read and the `answer/` checks stay), and non-Claude engines.
>
> **Acceptance**
> - A late or out-of-order record never leaves a Claude card `thinking` after its turn ended.
> - An Esc-dismissed dialog and an Esc-interrupted tool read idle within one record, with no
>   transcript heal.
> - A permission dialog reads needs-you within 1 s of opening.
> - A background shell's chip appears on start and clears on completion, TaskStop, Monitor expiry,
>   and, at the latest, the next turn end after a UI kill.
> - With the mod absent, behaviour equals today's (hook path).

## Appendix: harness and isolation

- **Runtime dir.** A short `mktemp -d /tmp/claude-1000/hm.XXXX`, because of the unix socket path
  limit. Scripts lived in the session scratchpad; none is committed.
- **Private tmux server.** `env -i HOME PATH USER LANG TERM HOUSTON_STATE_DIR=<dir>/state
  TMUX_TMPDIR=<dir>/tmux tmux -f /dev/null new-session …`, so no `CREW_*`, `DISPATCH_*`,
  `CLAUDE_CODE_*` or `TMUX` variable reaches it. A presence-only check in a pane confirmed
  `CREW_WORKER_ID`, `CREW_ID`, `CREW_ROLE_ID`, `DISPATCH_SPEC`, `CLAUDECODE` and
  `CLAUDE_CODE_ENTRYPOINT` were unset.
- **houston.** Built from this branch, run in a window of that server:
  `-status-dir <dir>/state -mode tmux -no-opencode -no-auth -claude-mod -debug -addr
  127.0.0.1:17481`. `-no-auth` only on loopback, for curl.
- **Claude Code.**
  - Run as the **real binary**, not the `claude` wrapper on PATH: the wrapper adds the user's own
    plugins, hookyard among them.
  - Flags: `--plugin-dir contrib/claude-mod --setting-sources project,local --settings
    <dir>/settings.json --model haiku --permission-mode default`.
  - Under `bwrap --dev-bind / /` with scratch binds over `~/.claude/projects`,
    `~/.claude/history.jsonl` and `~/.claude/file-history`, so test transcripts and prompts never
    reached the live houston or the real history. `~/.claude/todos` does not exist on this host
    and was not bound.
  - `settings.json` registered this branch's `houston hook` for the classic events, plus the
    delayed PostToolUse wrapper.
- **Raw capture.** For payload evidence houston was swapped for a small Unix-socket listener that
  logs each record with its arrival time.
- **Accepted incidental writes** of any Claude run, outside the binds: the trust entry for the
  scratch cwd in `~/.claude.json`, and Claude's own caches (shell snapshots, debug and statsig
  files).
- **The initial API probe.** Before this harness existed, a probe used the PATH `claude`
  (`claude -p --no-session-persistence`, so no transcript). That wrapper loaded the user's plugins,
  hookyard included. `CREW_*`, `DISPATCH_SPEC` and `TMUX*` were unset and `HOUSTON_STATE_DIR` /
  `HOUSTON_STATUS_DIR` pointed at scratch, so its houston hook writes went to scratch and nothing
  reached the crew bus.
- **Cleanup.** `tmux kill-server`, gated on the server's `@crewtmux 1` marker, ended houston and
  Claude. Every recorded pid was confirmed gone.
