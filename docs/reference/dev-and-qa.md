# Dev tooling: OpenCode setup, load measurement, QA harness

Read when running perf/load fixtures, the QA harness, or setting up OpenCode integration.

Moved verbatim from the root `CLAUDE.md`; section headings are unchanged.

### OpenCode Setup

OpenCode TUI uses a **random port by default**, which houston can't discover. To enable houston integration:

**Option 1: Start OpenCode with a fixed port**
```bash
opencode --port 4096
```

**Option 2: Use opencode serve + attach**
```bash
# Terminal 1: Start headless server
opencode serve --port 4096

# Terminal 2: Attach TUI for interaction
opencode attach http://localhost:4096
```

**Option 3: Tell houston the port**
```bash
houston --opencode-url http://localhost:YOUR_PORT
```

**Option 4: Use the houston plugin (recommended)**

Copy the plugin to your OpenCode config:
```bash
cp contrib/opencode-plugin/houston.ts ~/.config/opencode/plugins/
```

The plugin automatically writes server info to `~/.local/state/houston/opencode-servers/` when OpenCode starts. Houston reads these files to discover running instances.

After installing the plugin, restart your OpenCode instances.

Houston scans ports 4096-4100 by default. Use `--no-opencode` to disable.


### Load measurement

Synthetic load, never real session content:

- `go run -tags tools ./cmd/loadfixture -dir D` writes a state dir
  (`D/state`) and Claude projects dir (`D/home/.claude/projects`) of synthetic
  sessions and transcripts (`-sessions`, `-scale`, `-seed`), and a
  `D/state/.loadfixture` marker naming the projects dir. Run houston against
  it with `HOME=D/home houston -status-dir D/state`.
- `scripts/perf/startup.sh BIN D PORT [seconds] [mode]` runs an isolated
  instance on a copy of the fixture state and reports startup CPU/RSS, when all
  sessions are listed, list and chat readiness, the `/api/runs` snapshot size
  and latency, and a reconnect storm (concurrent snapshots: wall time, server
  CPU). With `PPROF=<file>` it starts houston with `-debug` and saves a CPU
  profile of the storm there.
- `NODE_PATH=<playwright-core install>/node_modules CHROMIUM=<chromium> node scripts/perf/client.cjs PORT STATE_DIR RUN_ID CHAT_RUN_ID`
  drives headless mobile Chromium (`playwright-core`, 4x CPU slowdown) through
  a local proxy that can stall (no FIN/RST) or reset live connections, and
  reports time-to-interactive for first load, reload and Chat, plus how long a
  sleep and resume takes to deliver live runs and chat updates. It **writes**
  into `STATE_DIR` (hook state files under `claude/`) and appends lines to the
  transcript of `CHAT_RUN_ID`'s state file, so `STATE_DIR` must be a
  loadfixture `D/state`: the script refuses a dir without the `.loadfixture`
  marker and any transcript outside the marker's projects dir. The target is a
  houston started by hand on that dir (`HOME=D/home houston -status-dir D/state
  -addr 127.0.0.1:PORT -mode tmux -no-opencode`); `startup.sh`'s private state
  dir is not reusable. Its header comment has the invocation.
- **Dispatcher-mode fixture:** `-crews N -workers M` on `cmd/loadfixture` also writes `D/repo` (a git main checkout) whose bus (`.git/crew/events.jsonl`: dispatch, status, msg, pr-watch and reap rows, plus a few finished windowless branches per crew) holds N crews of M windowed workers, and `D/crews.tsv` naming the dispatchers and workers (`internal/loadfixture/crews.go`). It leaves `crews/<id>/pane|pid` to the tmux helper, because the join rejects a pane file older than the tmux server.
  - `scripts/perf/crewtmux.sh start D WORKDIR` starts a **private tmux server** (`env -u TMUX -u TMUX_PANE TMUX_TMPDIR=WORKDIR/tmux tmux -f /dev/null`; with `$TMUX` set tmux ignores `TMUX_TMPDIR` and would create the windows on the live server, so the script also refuses WORKDIR/tmux when it is a default socket dir; tmux 3.2+ likewise falls back to `/tmp` when `TMUX_TMPDIR` is empty or not an existing directory, so every call refuses unless WORKDIR/tmux exists) with a dispatcher window and the worker windows stamped (`@git_root`, `@crew_name`, `@crew_color`, `@branch`, `@claude_status`), writes each crew's `pane` and `pid` (the window's `pane_pid`), and prints `TMUX_TMPDIR=WORKDIR/tmux`. `start` sets the server option `@crewtmux 1`; `stop WORKDIR` (and `start`'s failure cleanup) kills the server only when it carries that marker, and creates nothing. Keep WORKDIR short: the socket path must fit a unix socket (`sun_path`, about 108 bytes).
  - `startup.sh` cannot target this server: it always runs on its own fresh, empty `$work/tmux`, so dispatcher mode there sees no dispatcher or worker windows. Start houston by hand on the fixture state: `env -u TMUX -u TMUX_PANE TMUX_TMPDIR=WORKDIR/tmux HOME=D/home houston -status-dir D/state -addr 127.0.0.1:PORT -mode dispatcher -no-opencode`, then run `client.cjs`'s `fleet` scenario against it.
  - `SCENARIOS=fleet node scripts/perf/client.cjs PORT` (same `NODE_PATH`/`CHROMIUM`; it writes nothing) times the Fleet list at 390x844 (`fleet`) and 1280x800 (`fleet-desktop`), both with the 4x CPU slowdown: `tti`, `nodes` (elements under the list container), `cards` (`.run-card`) and `rows` (`.worker-row`). `FLEET_EXPECT` is the count of run cards plus worker rows to wait for (unset: any count that has stopped changing for 500 ms); `SCREENSHOT_DIR` saves `fleet-mobile.png` and `fleet-desktop.png`. Run it against a main-built binary and this branch on the same fixture to compare.

### QA harness

`scripts/qa/` is an end-to-end pass over the ten UI areas (Fleet, navigation, Chat, answering, Terminal, Crew, Workspace, Dispatch, modes, liveness) against real claude and pi agents, run before releases. **Local only**: it needs real claude/pi credentials and quota, so it is not CI. It runs next to the live houston (`127.0.0.1:9090`) and the live tmux server, which must receive nothing; every rule below keeps the two apart.

- `scripts/qa/setup.sh [--no-agents] [S]`: builds the UI and houston, writes a `loadfixture` (12 sessions, 2 crews of 7 workers), starts the private tmux server, scratch repos `$S/repos/qa-repo` and `qa-wt`, shell windows (`wt`, `less`), a claude window (`--model haiku`) and a pi window (`QA_PI_MODEL`), then houston in dispatcher mode, and installs `playwright-core` into `$S/pw` with `npm ci` from the committed `scripts/qa/pw/package-lock.json` (1.57.0 exactly, integrity-checked). `--no-agents` skips claude and pi (enough for Fleet, nav, crew and fixture-only checks). `S` is `mktemp -d /tmp/hqa.XXXXXX` by default and must match `/tmp/hqa.<random>` (a marked `.houstonqa` dir); an explicit `S` must be a real directory (not a symlink) you own with mode 700, a new one is made with `mkdir -m 700`, and every script (`qa_check_root`) refuses otherwise. It prints `PORT=` and `S=` and writes `$S/qa.env` (`PORT`, `TMUX_TMPDIR`, `NODE_PATH`, `CHROMIUM`, pane ids, `CREW1`/`CREW2`). Crew 2 has its `pane` file removed, so it is the orphan crew.
- `S=$S [SCENARIOS=fleet,nav] [SHOTS=dir] node scripts/qa/qa.cjs`: default is all of `fleet nav chat answer terminal crew workspace dispatch modes liveness`, each from `scripts/qa/scenarios/<name>.cjs`. The built-in `guard` self-test (area 0) runs first and proves a foreign HTTP and WebSocket origin is refused. Stdout is one JSON line per check, `{"area","check","result":"pass|fail|skip","note","shot"}`, then `{"summary":{pass,fail,skip},"guard_violations":[…]}`. Exit 1 on any fail or guard violation, 2 on a setup error. Screenshots go to `$S/shots`.
- `scripts/qa/houston.sh S dispatcher|tmux|restart|stop` swaps the houston under test (the `modes` and `liveness` scenarios use it and restore dispatcher mode). `scripts/qa/isolation.sh S [--self-test]` reads content, never mtimes, to prove the live hook state, live tmux panes and `~/.claude/projects` hold nothing of `$S`; run it before and after a pass. `scripts/qa/teardown.sh [--purge] S` stops houston, kills the private server (only with its `@crewtmux` marker), fails if any process still names `$S` or carries `HOUSTON_STATE_DIR=$S/state`, and `--purge` removes `$S`.

Isolation rules:

- **Private tmux server** (`crewtmux.sh start`, `@crewtmux 1`): every tmux call goes through `tp` (`lib.sh`) or `ctx.tmux`, which run `env -u TMUX -u TMUX_PANE TMUX_TMPDIR=$S/tmux tmux -f /dev/null`. With `$TMUX` set tmux ignores `TMUX_TMPDIR` and acts on the caller's server; tmux 3.2+ falls back to `/tmp` when the dir is missing, so both refuse unless `$S/tmux` exists and is not a default socket dir. The one plain `tmux` call is `isolation.sh`'s read-only listing of the live server; with no live server to list, check (c) cannot run and `isolation.sh` exits 2 (`ISOLATION_NO_LIVE_TMUX=1` accepts that and leaves (c) out of the summary).
- **Env scrub** (`qa_scrub_env`, plus `set-environment -gu` on the server): `CREW_*`, `DISPATCH_*`, `DISPATCHER_*`, `CLAUDE_CODE_*`, `CLAUDECODE`, `CLAUDE_PID`, `CLAUDE_EFFORT`, `TMUX` and `TMUX_PANE` are dropped from the script's environment, and the server's global environment loses `CREW_WORKER_ID`, `CREW_ID`, `CREW_ROLE_ID` and `DISPATCH_SPEC` (`set-environment -gu`), because an agent started with `CREW_WORKER_ID` has hookyard post "exited" for the caller's worker on the real crew bus.
- **Hook state**: the server's global env sets `HOUSTON_STATE_DIR` and `HOUSTON_STATUS_DIR` to `$S/state` before any agent starts, so the deployed `houston hook` writes there; `setup.sh` fails unless a claude state file for the pane appears under `$S/state`.
- **Transcripts**: claude and the houston under test run under `bwrap --dev-bind / / --bind $S/home/.claude/projects ~/.claude/projects` (`HOME` unchanged, so credentials, plugins and OAuth refresh stay the single real files). The live houston rescans `~/.claude/projects` every 30 s and would list QA sessions; a hook's `transcript_path` resolves to the scratch dir only inside the bind. pi writes to `$S/pi-sessions`.
- **`--permission-mode default`** for the QA claude: the user's settings default to auto, which would never show the permission dialogs the `answer` scenario needs.
- **Browser guard** (`lib.cjs`): `route('**/*')` and `routeWebSocket` let only `127.0.0.1:$PORT` through (anything else is aborted and recorded in `violations`); service workers are blocked; `lib.cjs` takes `S`, `PORT` and every other `qa.env` key from the file and refuses a differing process-environment value (`NODE_PATH` and `TMUX_TMPDIR` excepted: the file wins), requires `S` to be a marked `/tmp/hqa.<random>` dir, and refuses any `PORT` outside 17470-17499 (so 9090 too; `lib.sh` enforces the same range). `POST /api/dispatch*` is never sent: `page.route` fulfils it in the browser, `ctx.api` refuses non-GET there, and a context-level route aborts and records any such POST that no `page.route` fixture caught (page routes win over it).
- **Token and scratch dir**: the bearer token for `houston.sh`'s readiness probe sits in `$S/auth.hdr` (0600, `curl -H @file`), never in argv, where other local users could read it. `qa_pid_names_root` matches `$S` only as a whole path, so `/tmp/hqa.ab` never matches `/tmp/hqa.abc`; `teardown.sh` still kills the private tmux server and scans when houston will not stop, then exits 1.
- **`crew` CLI against the fixture bus** runs from `$S/repo` with `CREW_WORKER_ID` and `CREW_ROLE_ID` unset and the private tmux env (`TMUX`/`TMUX_PANE` unset, `TMUX_TMPDIR=$S/tmux`): it can nudge and set options on the pane id in `crews/<id>/pane`, which on the caller's server would address a live pane.

Gotchas:

- Claude's workspace-trust dialog starts on "No, exit"; `setup.sh` moves to "Yes" before it sends the first prompt.
- `gtrash` cannot trash under `/tmp` (no per-uid trash dir there), so `--purge` falls back to `rm -rf` of the validated `/tmp/hqa.*` dir.
- The crew `question-live` and `question-composer` checks append to `$S`'s bus and skip on a second run; re-run `setup.sh` for a fresh `$S`. The `workspace` scenario ends by removing `$S/repos/qa-wt`.
- Real-agent checks spend Claude quota: haiku, one-sentence prompts. Keep new prompts short.
- Pass only `S=` to the scripts; never export `qa.env` or setup's `TMUX_TMPDIR=` line into your shell. `tp` and `ctx.tmux` compare `$S/tmux` against the caller's `TMUX_TMPDIR` to refuse a default socket dir, so with `TMUX_TMPDIR=$S/tmux` exported every tmux call is refused, teardown cannot stop the private server, and `isolation.sh` exits 2.

Not covered: WebKit and iOS Safari (no WebKit browser launches on this host), so anything Safari-specific stays manual.


### Vite Proxy

The Vite dev server (`ui/vite.config.ts`) proxies `/api` to `http://localhost:9090`. Change the target port if your Go backend runs on a different port.


