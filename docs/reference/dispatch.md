# Dispatch, new dispatcher, restart, repo registry

Read when changing `/api/dispatch*`, the new-dispatcher launcher, dispatcher restart or the repo registry.

Moved verbatim from the root `CLAUDE.md`; section headings are unchanged.

## Dispatch

The Dispatch tab and every route in this section exist only in dispatcher mode (see "Modes").

The Dispatch tab starts a worker by running the host's `dispatch` CLI
(`server/dispatch.go`, `server/dispatch_exec.go`), or a new dispatcher through
the launcher described under "New dispatcher". This is remote command
execution from an HTTP request, so the handler is closed by construction:

- `GET /api/dispatch/options` — the repos houston knows (tmux ∪ the repo
  registry, deduplicated by resolved path; a tmux `@git_root` is resolved to its
  main repo), each with `registered` (a registry member), that repo's crews from
  `<git-common-dir>/crew/crews/` and `home` — the subset of `crews` whose
  dispatcher pane lives in that repo. Also `repo_roots` (the configured roots),
  tiers, efforts, plans, the per-engine model allowlist (`engines`, displayed in
  `engine_order`), `tier_models` (the default model per engine+tier), and
  `dispatcher_engines`: `dispatch --engines` intersected with
  `dispatchEngineOrder`, or `[]` plus `dispatcher_engines_error` when that
  command fails, so the worker form still loads.
- `POST /api/dispatch` — `{repo, title, spec?, tier, engine, model, effort,
  plan?, crew, issue?}`. Every argv value is an enum, an allowlisted model, or
  an anchored-regex match; `repo` must be in the options set and `crew` must be
  `"new"` or exist in that repo. The title is the only free text — one argv
  element that may not start with `-`, equal `resume`, or look like an issue id
  (dispatch has no `--`). The task body goes to a 0600 temp file passed as
  `DISPATCH_SPEC` and removed afterwards; it is never logged. The response
  carries `crew` only when that crew exists after the response: the supplied
  or minted id on success (200) and on timeout (504), the minted id on the
  rare exit-0-without-`worker_id` 502, and the supplied (never a minted) id on
  a dispatch-side failure (422).
- `crew: "new"` mints a crew itself rather than requiring an existing one:
  `<unix>-<pid>` (houston's own pid), created under `<git-common-dir>/crew/crews/`
  inside the dispatch slot so only one request can mint per second; a
  same-second collision is 409 (retry). The literal `"new"` never reaches
  argv — `--crew-id` always carries the minted or supplied id. The minted
  directory is removed only when dispatch couldn't be started or exited
  non-zero; it's kept on timeout, on success, and on the rare exit-0-without-
  `worker_id` case. A houston-minted crew has no dispatcher process (no
  `pid`/`pane` files) — its workers report into the bus and Fleet only;
  `crew adopt <id>` attaches a dispatcher to it later.
- One dispatch at a time (429 otherwise), 120 s timeout. The run is detached
  from the request, so a dropped phone connection doesn't abort a half-built
  worktree; on timeout the process group gets SIGTERM (dispatch's trap clears
  its branch lock), then SIGKILL.
- A crew id other than `"new"` is refused by houston itself before dispatch
  runs: well-formed (`<unix>-<pid>`) but not found under the repo is 404,
  malformed is 400. dispatch's own refusals (tier↔model map, budget) come back
  as 422 with its stderr verbatim. The new run then appears in Fleet through
  the crew source — nothing else to wire.
- The model allowlist and per-tier defaults come from `dispatch --models --json` (`dispatchModelSet`, `server/dispatch_models.go`), run like `dispatch --engines` (`dispatchEnv`, tmux server PATH, 10 s timeout) once per options GET, `POST /api/dispatch` and `POST /api/dispatch/dispatcher`, never cached. Only literal ids are kept: any entry outside `dispatchLiteralModelRe` (globs `*?[`, a leading `-`, spaces) and every `regex` list is dropped, so POST validation stays a string-equality allowlist. `tier_models` is the tier's `default`, else the first literal model in that tier. If the command fails (older dispatch without `--models`, non-zero exit, timeout, unparseable or empty output), `builtinDispatchModels` (the literal entries of dispatch's `modelMap`) is used for options and validation, and the options response carries `dispatch_models_error`. Update the built-ins when the tier map changes; they are only a fallback.
- houston's own `PATH` must reach `tmux` and `dispatch` (exec resolves
  `dispatch` there; a 502 means it could not be started), and a tmux server
  must be running. dispatch and `dispatch --engines` run with
  `PATH=<tmux server's global PATH>:<houston's PATH>` (`dispatchEnv`), so
  `crew`, `git`, an authenticated `gh`, `wt`, `direnv` and the agent CLIs may
  live on either. When the server's `PATH` can't be read, the worker and
  options endpoints `slog.Warn` once per request and keep houston's `PATH`
  alone; the new-dispatcher endpoint fails closed instead (see "Server
  PATH"). Any other missing tool fails inside dispatch and comes back as a 422
  with its stderr.
- `-no-auth` leaves this endpoint enabled: that mode already exposes
  `/api/runs/:id/input` (identity-guarded, but not authenticated), which is
  equivalent command execution.
- `#/dispatch?host=<name>&repo=<path>&crew=<id|new>` prefills the form (host,
  repo and crew only) from a link; after applying it the UI replaces the hash
  with `#/dispatch`. A link with any of the three selects its host (absent:
  local); see "Dispatch onto a peer".

### New dispatcher

`POST /api/dispatch/dispatcher` (`server/dispatcher_launch.go`) opens a tmux
window in a repo running the host's `dispatcher` launcher, seeded with the
tasks as its first prompt. Body `{repo, tasks[], engine, model?, effort?}`;
unknown fields 400, body over 256 KiB 413.

- **Order** (`handleDispatcherLaunch`): tmux server `PATH` 503 → engines
  502 → fields 400 (`validateDispatcher`) → repo 404 → unsafe repo path 422 →
  houston exe/launch dir not passable to tmux 500 → slot 429 → crew mint 409 →
  tmux 503/502. A repo-list failure is 502, a launch-file write 500.
- **Fields:** `repo` must be in the options set; `engine` must be in
  `dispatch --engines` ∩ `dispatchEngineOrder`, run fresh with a 10 s timeout;
  `model` is empty or in `dispatchModels[engine]`; `effort` is empty or in
  `dispatchEfforts`. At most 20 entries in `tasks` (blank ones count toward the
  cap), each ≤ 2000 runes after normalization: every run of `unicode.IsSpace`
  becomes one space, then trimmed, so a multi-line row or paste is accepted;
  empty ones are dropped. A normalized task may not contain a `Cc` rune or start
  with `-` (the launcher parses `--agent`/`--model`/`--effort` anywhere in
  argv). `Cf` is allowed (ZWJ emoji, the bidi marks a Hebrew keyboard inserts):
  task text reaches only `execve` argv, never a shell or tmux. A leading `@` or
  `/` is the operator's own prompt. The UI's `normalizeTask` mirrors this; the
  vectors are duplicated in `server/dispatcher_launch_test.go` and
  `ui/src/fleet/dispatcherForm.test.ts` — keep them in step.
- **Prompt:** 0 tasks → none (the launcher's own name); 1 → the task verbatim;
  N → `N tasks: (1) a (2) b` on one line (claude/pi use the prompt as the
  session `--name`, so no newlines). ≤ 8 KiB.
- **Crew:** houston mints `<unix>-<pid>` with an exclusive `os.Mkdir` under
  `<git-common-dir>/crew/crews/` (same mint as the worker's `crew: "new"`; a
  same-second clash is 409) and forces it through the wrapper's `CREW_ID`, so the
  id is known before launch and `crews` lists it at once.
- **No user text in tmux argv.** tmux 3.x splits a command at any argv element
  ending in `;`, format-expands `-c` (any `#`, so `#()` runs a command), and runs
  a single-argument command through `sh -c`. So the window's command is always
  `houston launch-dispatcher <file>` (three elements, never `sh -c`); the task
  text and launcher args ride in a launch file (`{args, crew_id}`), and
  `tmuxSafeArg` refuses (422) a repo path containing `#`, a control rune or a
  trailing `;` before exec. The file is `<status-dir>/launch/<random>.json`, 0600
  in a 0700 dir; the wrapper removes it on read, houston on a failed start, and
  `New` and every launch (under the slot, so files kept on a 502 don't wait
  for a restart) sweep any older than 10 minutes (a wrapper that never ran
  must not leave task text behind).
- **Wrapper** (`houston launch-dispatcher <file>`, `ExecDispatcherLaunch`): sets
  `remain-on-exit failed` on its own pane (best effort, so a failed launch keeps
  its error text for the startup check and the user; a clean exit closes the
  window), reads and removes the file, resolves `dispatcher` with
  `exec.LookPath`, stamps its window by pane id (`@crew_name dispatcher`,
  `@crew_color colour99`), then
  `syscall.Exec`s the launcher — exec, not a child, so `crew register $$` sees
  an ordinary hand-launched dispatcher. Every tmux call is targeted at
  `$TMUX_PANE` with a 2 s timeout, best effort. It runs in the **tmux
  server's** environment, not houston's: `PATH` is the tmux server's global
  one (see "Server PATH") and must reach `dispatcher` and `claude`/`pi` (a
  missing launcher fails in the pane, so it comes back as a 422 with that
  output, never a 502); `HOUSTON_*` and
  service env don't leak; `CREW_ID`, `CREW_WORKER_ID`, `CREW_ROLE_ID` and
  `DISPATCH_SPEC` are stripped (`launchEnv`) and `CREW_ID` set to the minted id,
  so a tmux-global `CREW_ID` can't win.
- **Session:** the session with the most windows whose `@git_root` resolves to the
  repo (tie → name order); else an existing session named after the sanitized
  repo basename (`[^A-Za-z0-9_-]` → `_`); else `new-session`. `has-session`
  failing with anything but `can't find session` (no server, timeout) is 503
  `tmux unavailable` with nothing created — houston never starts a tmux server.
  `new-session` is `-d` (houston has no terminal to attach) and `-E`: without
  it tmux copies the creating client's — houston's service — values of every
  `update-environment` name (`SSH_AUTH_SOCK`, `DISPLAY`, …) into the session and
  marks the absent ones removed, shadowing the server's globals.
  `new-window` is `-d`: the window never becomes the session's current one, so
  a client viewing that session is not switched to the new dispatcher window.
  The launcher stamps its own window by pane id (`-t "$TMUX_PANE"`, dispatcher
  #655 — houston and #655 ship in the same nix-config bump, because a launcher
  predating it stamps untargeted and would relabel the client's current
  window). The wrapper also pre-stamps `@crew_name`/`@crew_color` by pane id
  (belt-and-braces, so the new window carries its tags even if the launcher's
  own stamp is missed), and the new window carries them without being current.
- **Server PATH** (`tmuxServerPath`): tmux hands a pane created by a
  session-less client — houston — that client's `PATH`, over the global
  environment and even over `-e PATH`. So houston first reads
  `show-environment -g PATH` and runs the `new-window`/`new-session` client,
  and `dispatch --engines`, with it (houston.service's own `PATH` has no
  `dispatcher`, `claude` or `pi`). No global `PATH` (absent, `-PATH`, empty) →
  503 `the tmux server has no global PATH`; tmux unreachable → 503
  `tmux unavailable: …`; nothing created, never a fallback to houston's
  `PATH`. The value is never logged.
- **Startup check** (≤ 3 s, 200 ms poll of `display-message #{pane_dead}`):
  success as soon as `<crew>/pid` holds non-empty text (`readPIDText`: a regular
  file of at most 64 bytes; the launcher passed its own gates and registered; a
  restart also needs the text to differ from what it held before the launch, see
  "Restart dispatcher"). A dead pane → 422 `dispatcher exited with status N` with the
  captured `output`, the window (or the session, when this request created it)
  killed. A gone pane — a tmux error matching `tmux.IsGoneMessage` (`can't
  find`, no socket, `no server running`), or blank fields from a live server
  that no longer knows it — → 422 `dispatcher exited immediately`. Either 422
  removes the launch file and the crew dir when it is empty, otherwise returns
  `crew`. Any other check error → 502 `could not check the dispatcher pane: …`
  with `crew, session, window, pane`, keeping the crew dir and the launch file.
  Neither within 3 s → 200, still starting. Other 502s: engines lookup, tmux
  failing to start the window (crew dir and launch file removed), or tmux
  printing no pane (crew and launch file kept, `crew` returned).
- **Slot:** `launchSlot`, separate from the worker's `dispatchSlot` so a 120 s
  worker dispatch doesn't block a launch; busy → 429.
- **Response** 200: `{crew, session, window, pane, run_id}`; `run_id` is
  `runs.PaneRunID(pane)`, so the UI never derives it. Errors use the same
  `dispatcherResponse` shape (`error`, `output`). Task text is never logged.
- Fleet's `project` for the new window comes from `@git_root`, which the tmux-og
  `after-new-window` hook stamps — houston doesn't set it. Without that hook the
  run still gets role `dispatcher` (`@crew_name`) but no project chip. The run
  enters Fleet only once its agent sets `@claude_status` or `@agent_screen`.
- houston's own `PATH` needs `tmux` and `dispatch` (for `--engines`);
  `dispatcher` and `claude`/`pi` come from the tmux server's global `PATH`.

### Restart dispatcher

`POST /api/runs/{id}/restart` (`server/dispatcher_restart.go`, dispatcher mode, behind the auth gate) ends a dispatcher run's engine session and starts a new dispatcher on the same crew in the same repo. Workers are untouched.

- **Server state only:** the crew id (`Run.Crew.Name`, which must match `dispatchCrewRe`) and the bus (`Run.CrewBus`) come from the dispatcher ↔ crew join; the repo is the dispatch repo whose common dir is the bus's parent (symlink-resolved before matching, so a repo reached through a symlink still matches), and `crews/<id>` must be a directory there. The crew reaches the launcher through the launch file's `crew_id` (`launchEnv` sets `CREW_ID`); it is never minted and houston never removes it, whatever fails.
- **Body** `{engine?, model?, effort?, force?}`, unknown fields 400. An absent field inherits; `""` for model or effort is the launcher default. Inheritance reads the old launcher's argv (`/proc/<pid>/cmdline` of the crew's recorded `pid`, Linux only; `parseLauncherArgv` mirrors the launcher's flag loop). Without it the engine is the run's `agent` and model/effort the launcher defaults. A changed engine does not inherit the model (models are per engine). The resolved values pass `validateDispatcher` exactly like the launch route; an inherited value that no longer validates is a 400, never a silent change. A restart carries no task prompt. `--autocompact` and a `/model` toggled inside the session are not carried.
- **Busy** (`restartBusy`): `thinking`/`running`/`compacting`, or `blocked` unless the question kind is `turn`; for a Claude run also a permission dialog on screen (`answer.PermissionPrompt` over a capture, since a dialog left open past `idle_prompt` reads `idle`). Busy without `force` → 409 `{busy: true}`, nothing touched; the state check (not the capture) repeats just before the kill. Residual: an `AskUserQuestion` dialog left open past `idle_prompt` reads `idle` and is not detected.
- **Order** (`handleRunRestart`; only the window, kill and startup steps are `openDispatcherWindow`, shared with the launch route, as is `launchSlot`): registry 503 → run, dispatcher and crew 404 → body 400/413 → tmux server `PATH` 503 → engines 502 → settings 400 → repo list 502 → repo and crew 404 → crew records no dispatcher pid 409 (`the crew records no dispatcher pid — restart it from a terminal`; fail closed: without the pid the wrapper could not wait for the old dispatcher, and an abort would leave the crew registered to the killed new pane) → unsafe repo path 422 → houston exe/launch dir 500 → old pane re-resolved (`resolveRunPane`) 409/503 → busy 409, or 503 when the Claude pane capture for the dialog check fails → launcher on the tmux server's `PATH` 502 (checked before anything is touched) → slot 429 → launch file 500. Then the new window opens first (the old pane's session while it exists, else the launch route's session rule), the old pane is re-resolved (point-of-use server check) and ended with `kill-pane` by pane id, then the startup check runs. A pane already gone at the kill is fine.
  - The launch file carries `wait_pid` (the old recorded pid): the wrapper waits for that pid to exit before exec, because `crew register` refuses while the recorded pid is alive and the old launcher's `crew deregister` would otherwise remove the new registration. `launchWaitLimit` (60 s) is only a backstop that matters if houston itself dies mid-request (the wrapper then stops waiting and registers): on every houston-side outcome, including the post-await check below, houston has already answered or killed the new window, which ends the waiting wrapper.
  - Killing first was rejected: a failed new window would leave the crew without a dispatcher, and killing a session's last window can take the tmux server down with it. A failure between opening and the kill (busy again, different server, `kill-pane` failed) kills the new window and leaves the old one running.
  - `kill-pane`, not `kill-window`: a dispatcher window has one pane, so the window closes; a user's extra panes in it survive.
- **Startup check:** `awaitDispatcher` counts the launcher as registered when the crew's `pid` file text is non-empty and differs from the text before the launch (a restart's pid file already exists); houston's own await, after the kill, is `launchCheck + restartSlack` (`Server.restartSlack`, 5 s in production, covering a slow exit of the old process).
  - **Post-await check** (restart only, `windowLaunch.onStarting`, runs only when the startup check is still "starting"): if the old recorded pid is still alive (`Server.pidAlive`, default `processAlive`), the wrapper is still waiting on it and cannot register, so the new window is killed and the restart answers 502 `the old dispatcher process is still running after its pane was ended — the new launcher could not register; stop pid N and relaunch with CREW_ID=<crew> dispatcher`, with `old_ended`. Old pid dead → the 200 "still starting" is genuine.
- **`old_ended`** (`dispatcherResponse.OldEnded`, `omitempty`, restart only; the launch route never sets it): true exactly when the restart's `afterOpen` killed the old pane or found it already gone. So every post-kill status carries it (200, 422 dead/gone, 502 unchecked, the 502 above) and no pre-kill refusal does (those leave the old dispatcher running).
- **Statuses:**

  | status | meaning |
  |---|---|
  | 200 | `{crew, session, window, pane, run_id, old_ended}` |
  | 400 | malformed body or a setting outside the allowlists |
  | 404 | `no such run`, `run is not a dispatcher`, `dispatcher has no crew`, `repo is not a known repo`, `crew is not a crew of this repo` |
  | 409 | busy (`busy: true`), crew records no dispatcher pid, run has no terminal, pane gone, pane on a different tmux server (at the first check or just before the kill) |
  | 413 | body too large |
  | 422 | repo path not tmux-safe; the new launcher exited (`output` carries its pane text; `old_ended`) |
  | 429 | another launch or restart is running (`launchSlot`) |
  | 500 | houston exe/launch dir not passable to tmux, launch file not written |
  | 502 | engines or repo list failed; launcher not on the tmux server's `PATH`; tmux could not open the window; could not end the old session (new window killed, old left running); could not check the new pane (`old_ended`); the old dispatcher process still running after its pane was ended (new window killed, `old_ended`) |
  | 503 | no run registry; tmux server `PATH` missing; tmux unavailable (including a failed pane capture in the busy check) |

- **Residuals:** once the old pane is killed, a launcher failure leaves the crew without a dispatcher; the 422 carries the pane output and the crew, and `CREW_ID=<id> dispatcher` in the repo recovers. An old engine's `crew stream` child can outlive the pane; the protocol's `--status`/`--force` reclaims it.
  - A pid recycled between validation and the post-await check reads alive: a spurious 502 and a killed new window although the old dispatcher is dead (a manual relaunch, never a wrong claim).
  - houston dying between the kill and its reply leaves no record; the UI's pending mark expires to "no longer available".
  - A dropped request the server completes never tells the phone the new run id; the new dispatcher simply appears in Fleet.
- **UI:** a `Restart` control in the Crew tab header (`RestartDispatcher`, on a joined dispatcher in dispatcher mode, local or a peer's: restart is forwarded to the peer, see "Dispatch onto a peer"; for a peer run its Settings load that host's options). An inline confirm says the session ends and workers keep running; force is decided when the confirm opens (`isBusy` at that moment, mirroring `restartBusy` minus the pane check) or by a 409 `busy`, never by a state change after opening; either turns it into `Force restart`. An optional Settings disclosure offers engine/model/effort (`same as now` = omitted, `launcher default` = `""`). While the request is in flight the old run id is marked pending (`pendingRuns.ts`, 60 s). A failure is recorded per old run id (`recordRestartFailure`, 10 min) only when the response carries `old_ended`; pre-kill refusals leave the old dispatcher running and record nothing. Cancel clears the record. A network error (status 0) or a 504 (a forwarded peer restart whose outcome is unknown; the local route never answers it) shows "The connection dropped — the restart may still be running on the server…", records nothing and keeps the old id pending. When the old run has left the list, `RunDetail` shows "The restart did not come up" with the error and output instead of "no longer available", and the `CREW_ID=<crew> dispatcher` relaunch line only for 422 or the old-process 502 (the launcher is definitely not running; for a peer run it reads "…in the repo on <host> with CREW_ID=…", the failure record carrying `host`), else "Check Fleet for the new dispatcher before relaunching.". On success the new id is marked pending (`RunDetail` shows "Starting the restarted dispatcher…" instead of "no longer available") and `switchRunTab(newId, 'crew')` replaces the history entry, only while the route still names the old run, so Back never returns to the killed run.

### Repo registry

The set of repos beyond tmux's view (`server/repo_registry.go`,
`server/repos_api.go`).

- **Roots:** `-repo-root DIR` (repeatable) bounds what can be registered or
  picked. Default `$HOME/git` (none, with a warning, when the home dir is
  unknown) — houston runs as a service and cannot infer more; repos elsewhere
  stay reachable through the tmux set whenever a window is open there, and become registrable by adding a `-repo-root` for their parent. Each
  root is made absolute and symlink-resolved at startup; one that doesn't resolve
  is dropped with a warning. No usable root → the picker is empty and adds are
  422.
- **File:** `<status-dir>/repos.json` `{"repos":[…]}`, 0600, temp file + rename,
  capped at 500 (409). A corrupt file is logged and read as empty, and is not
  overwritten until the next successful add/remove.
- **Invariant:** every entry, after symlink resolution, lies inside a root and is
  a git main checkout (`<real>/.git` is a directory and the git common dir is
  `<real>/.git`). The stored value is the resolved path, so a symlink retargeted
  outside the roots fails the next listing. Add runs the full check including
  `git rev-parse` (400 relative or containing a `Cc` or `Cf` rune —
  `dispatchHasControlRune`, checked before any filesystem access — 422 when the
  symlink-resolved path contains one, lies outside the roots, or is not a main
  checkout); listing runs the cheap one (`EvalSymlinks` inside a
  root plus `Lstat(.git)` is a directory), so an options GET never spawns up to
  500 gits. An entry failing it is dropped from `options.repos` but kept in the
  file — a transient unmount must not delete it — and `GET /api/repos` shows it
  `valid:false` so the picker can still remove it.
- **Routes:** `GET /api/repos` → `{roots, repos:[{path,name,valid}]}`;
  `POST /api/repos` `{path}` → 200 `{path,name}` (idempotent); `DELETE /api/repos`
  `{path}` → 204, 404 when absent, never touching the repo itself.
- **Picker:** `GET /api/repos/candidates?q=` → `{roots, candidates:[{path,name,
  registered}], truncated}`. Depth ≤ 3 below each root, hidden directories
  skipped, symlinks not followed, a directory with a `.git` directory is reported
  and not descended into; 10 000 directories visited or 200 results →
  `truncated`. `q` is a case-insensitive substring of the root-relative path,
  ≤ 200 runes (400 on a `Cc` or `Cf` rune, `dispatchHasControlRune`). Registration re-validates fully.
- **Remember-on-use:** a 200 from either dispatch endpoint adds the repo through
  the same path (for a forwarded dispatch it happens on the peer, against the
  peer's own roots and registry); one outside the roots is silently not remembered, which keeps
  one invariant (every entry is inside a root) instead of a second "tmux once
  listed it" class.
- **No auto-registration at startup:** a service's cwd is `/` or `$HOME`, so
  "houston's own repo" is meaningless, and tmux plus remember-on-use already
  cover every repo the user opens or dispatches into.


