# Plan — Workspace tab: the tmux tree across hosts, including non-agent panes

Issue #38. Design: `docs/superpowers/specs/2026-09-07-houston-overhaul-design.md`
(binding). State of play: `docs/superpowers/2026-09-08-state-of-play.md`,
"B. Crews, Workspace and Dispatch tabs". Closest precedent: PR #35 (Crews tab),
`docs/superpowers/plans/2026-09-10-crews-tab-and-branch-pane-join.md`.

The work splits into seven components: tmux field harvest (C1), the pure tree
builder (C2), the HTTP handler (C3), the TS client + poll hook (C4), the
Workspace view (C5), Shell wiring (C6), and the integration gate (C7).
`I1`-`I4` are the interfaces held stable across that split, pinned inline in
the step that owns them.

Order: `C1 ∥ C4`, then `C1 → C2 → C3`, `C4 → C5 → C6`, `{C3, C6} → C7`.

Baseline before any step: Go build, vet and tests green; UI typecheck, lint
and tests green.

Revised once after plan-critic. Three blocking findings, all verified against
the code and all adopted: `WorkspaceCrew.Color` had no reachable derivation
(`tmuxColorToHex` is unexported to `runs`, and the one chip that would have
used it doesn't render a dynamic colour) — dropped, not exported, since
nothing in this plan needs it; `Agent`/`RunID` were specified two
contradictory ways and the gap is a real, reachable case — a pane can carry a
composed `Run` via `HookSource` alone (`hook/hook.go`'s own `tmuxCoords()`,
independent of lazytmux's `@claude_status`), so `Agent` is now derived from
whether a `Run` joined, not from lazytmux's option; and `WorkspaceView`'s
`now` prop was unused and would fail `tsc -b` under this repo's
`noUnusedParameters` — removed. Several non-blocking notes were also folded
in: `tmux/options.go` gains a `Target()` method so C2 does not hand-duplicate
`windowsByTarget`'s join-key logic, the handler guards a nil `wsTmux`, and the
UI test step gains a `vi.mock` plan and DOM-distinguishable fixtures.

**Revised again mid-execute, per an explicit dispatcher directive (human
decision, not a suggestion).** A plain session→window→pane mirror was
rejected: measured live on this machine, one tmux session commonly spans
several different `@git_root` values (five, in one case), so the session
level alone does not tell a viewer which windows are "the real checkout" and
which are throwaway worktrees. A crew-based grouping was rejected too — most
runs on this machine carry a codename but an empty crew-bus id, so grouping
by crew would strand the majority of panes in a no-crew bucket. A
user-toggleable grouping was rejected as unbuilt, untested surface for a
choice a human does not want to keep making. The fixed shape instead: each
session groups its windows into **main checkout** (the window(s) whose
`@git_root` IS a repo's main checkout) and **worktrees** (everything else
under that repo), derived from repo identity via `git rev-parse
--path-format=absolute --git-common-dir` — not from the session name, which
the measurement above already falsifies as a repo proxy. This exact
git-common-dir technique already exists in this codebase for the same kind of
per-root keying: `runs/crewsource.go`'s `CrewSource.crewDir` resolves it (with
a plain `--git-common-dir`, then a manual relative-path fixup this plan's
`--path-format=absolute` variant skips) once per distinct root and caches the
result — this plan reuses that discipline, not the function, since `crewDir`
answers a different question (where the crew bus lives) with a different
cache lifetime (owned by `CrewSource`, ticking every 3s) than this plan needs
(owned by the HTTP handler, answering once per request but caching across
requests). A window with an empty `@git_root` belongs to no repo and must
stay reachable rather than being silently dropped — it lands in a third,
unlabelled-by-repo bucket. See the revised `I2` in Step 5 and the new
`repoClassifier` in Step 8a below for exactly how this composes.

## Why this is a plain `GET`, not a stream

The design spec's API surface table (line 266) lists `GET /api/workspace` next
to `GET /api/runs` *and* `GET /api/runs/stream` as separate rows — it lists a
stream variant explicitly wherever one exists, and does not for `/api/workspace`.
That is a decision already made, not an oversight to relitigate: the tree is
polled. `useWorkspace` (C4) fetches on mount and on an interval while the
Workspace tab is the mounted one; unlike Fleet/Crews, Workspace holds no
per-tab input state worth preserving across a tab switch (no composer, no
half-typed anything), so `Shell.tsx` mounts it conditionally instead of
keeping it mounted-but-`hidden`, and polling stops for free the moment the tab
is left — no visibility-change plumbing needed.

## Why correlation needs no `runs/` change

The join key is the tmux pane id (already decided, state-of-play). Every
source that can key an agent run to a pane — `tmuxsource.go`'s own tmux layer,
`hooksource.go` via `TmuxPane` — keys it as the *same* registry key, so a
joined agent pane composes into exactly one `Run` whose `Run.Tmux.PaneID`
equals that pane id, and `Run.ID` is already the externally-routable id the UI
uses (`#/fleet/<id>/activity`). Both fields are public. The handler builds a
`paneID → RunID` map by one pass over `s.runs.Snapshot()` and never touches
`idFor` or any other unexported registry internal — C3 does not import
anything from `runs/` beyond `runs.Run` and `s.runs.Snapshot()`, both already
used by `server/runs_reply.go`.

## C1 — tmux field harvest

- [ ] **Step 1: extend the format strings.** In `tmux/options.go` add
      `#{window_name}` and `#{window_active}` to `windowOptionsFormat` (new
      trailing fields, indices 12–13, so every existing index keeps its
      meaning and only the four raw-string fixtures in `options_test.go`
      change); add `#{pane_current_command}`, `#{pane_index}` and
      `#{pane_active}` to `paneOptionsFormat` (new trailing fields, indices
      4–6). These ride the *same* `-a -F` calls `TmuxSource` already issues
      every tick — no new tmux roundtrip.

      **`I1` — new struct fields**, appended (never inserted, to keep every
      existing field index stable):
      ```go
      type WindowOptions struct {
          // ...existing 12 fields unchanged...
          Name   string // #{window_name}
          Active bool   // #{window_active}
      }
      type PaneOptions struct {
          // ...existing 4 fields unchanged...
          Command string // #{pane_current_command}
          Index   int    // #{pane_index}
          Active  bool   // #{pane_active}
      }

      // Target is the "session:window" join key PaneOptions.Target already
      // carries verbatim. Exists so C2 does not hand-duplicate this string
      // build a second time outside the tmux package.
      func (w WindowOptions) Target() string {
          return fmt.Sprintf("%s:%d", w.Session, w.Window)
      }
      ```
      `tmux/options.go` gains a `"fmt"` import for this. Used only from
      `server/workspace.go` (C2) — `runs/tmuxsource.go`'s own
      `windowsByTarget` is left as its existing inline `fmt.Sprintf`, not
      swapped to call it, since that file is outside this plan's scope fence
      (`WORKER_TASK.md`: `runs/` only for what the tree genuinely needs and
      does not already expose) and touching it for a same-behaviour rename
      buys nothing this plan needs.
- [ ] **Step 2: bump the parsers.** `ParseWindowOptions`'s field-count guard
      12 → 14; `ParsePaneOptions`'s 4 → 7. Parse `Active` as `f[n] == "1"`
      (tmux's own boolean convention, matching `parseSessionLine`'s
      `parts[3] == "1"` elsewhere in this package) and `Index` via
      `strconv.Atoi`, skipping the line on a parse failure exactly like
      `Window` already does for `WindowOptions`.
- [ ] **Step 3: fix the four fixtures this breaks.** `TestParseWindowOptions`'s
      two lines, `TestParseWindowOptionsSkipsMalformedLines`'s second line, and
      `TestParseWindowOptionsSurvivesPipeInFreeText`'s line all get two more
      `\x1f`-separated fields appended. Add explicit assertions for the new
      `Name`/`Active` values on at least one enriched and one bare window.
      `TestParsePaneOptions`'s two lines each get three more fields; assert
      `Command`, `Index` and `Active` on both rows (one active, one not).
- [ ] **Step 4: verify C1.** `go build ./...`, `go vet ./...`,
      `go test ./tmux/...`.

## C2 — the pure tree builder

New `server/workspace.go`, tree-shaping logic kept separate from the HTTP
handler so it is unit-testable with hand-built fixtures — the same split
`runs/crewjoin.go`'s `resolvePane` uses relative to its own HTTP-adjacent
caller.

- [ ] **Step 5: the response types.**

      **`I2` — the JSON contract**, pinned once because C4 mirrors it verbatim:
      ```go
      type Workspace struct {
          Host     string             `json:"host"` // "" means local; unset by anything built here — M2's seam
          Sessions []WorkspaceSession `json:"sessions"`
      }
      type WorkspaceSession struct {
          Name         string            `json:"name"`
          WindowCount  int               `json:"window_count"`
          MainCheckout []WorkspaceWindow `json:"main_checkout,omitempty"`
          Worktrees    []WorkspaceWindow `json:"worktrees,omitempty"`
          Other        []WorkspaceWindow `json:"other,omitempty"` // no @git_root — belongs to no repo, still reachable
      }
      type WorkspaceWindow struct {
          Index        int             `json:"index"`
          Name         string          `json:"name"`
          Active       bool            `json:"active"`
          Branch       string          `json:"branch,omitempty"`
          Task         string          `json:"task,omitempty"`
          CrewCodename string          `json:"crew_codename,omitempty"`
          Panes        []WorkspacePane `json:"panes"`
      }
      type WorkspacePane struct {
          ID      string `json:"id"`      // "%307"
          Index   int    `json:"index"`
          Active  bool   `json:"active"`
          Command string `json:"command"` // #{pane_current_command}; the plain-pane label (spec: never the parser)
          Agent   bool   `json:"agent"`   // a composed Run joined this pane id — see Step 6
          RunID   string `json:"run_id,omitempty"` // set whenever Agent is true; the two can never disagree
      }
      ```
      No `WorkspaceCrew`/`Color` field: the only converter from a raw tmux
      colour name to `#rrggbb` (`tmuxColorToHex`) is unexported to `runs`, and
      the chip this plan actually renders (`.run-chip.codename`, Step 18) is a
      flat fill/text colour with no dynamic accent — carrying a `Color` field
      nothing renders is exactly the "always zero, inviting a reader to
      wonder why" trap `I2`'s own reasoning warns against. If a future plan
      wants a coloured accent here, exporting `tmuxColorToHex` is that plan's
      job, not this one's.

      Bucketing a window into `MainCheckout`/`Worktrees`/`Other` is not this
      function's decision alone — it needs to know, for each distinct
      non-empty `@git_root`, whether that root is a main checkout. That
      answer comes from a real `git` subprocess call (Step 8a), which
      `buildWorkspace` itself must not perform — it stays pure and
      fixture-testable, per the retro's own "prove it" standard. The caller
      resolves the map once (one call per **distinct** root, not per pane or
      per window — `crewDir`'s own discipline) and hands it in.
- [ ] **Step 6: `buildWorkspace`.** Pure function
      `buildWorkspace(wins []tmux.WindowOptions, panes []tmux.PaneOptions, snap []runs.Run, mainCheckouts map[string]bool) Workspace`.
      `mainCheckouts[root] == true` means that `@git_root` is a repo's main
      checkout; `false` or an absent key means a linked worktree (this
      includes a root whose `git rev-parse` lookup failed — the classifier in
      Step 8a resolves an error to `false` rather than asserting "main" on
      uncertain information, and caches that like any other answer, mirroring
      `crewDir`'s own accepted "caches negative results for the process
      lifetime" trade-off from the state-of-play defect list — not a new
      problem this plan is introducing). Bucket each window: `w.GitRoot == ""`
      → `Other`; `mainCheckouts[w.GitRoot]` → `MainCheckout`; else →
      `Worktrees`. Windows keep their original per-session `Index` ordering
      within whichever bucket they land in. `WindowCount = len(wins for this session)`,
      counted before bucketing (a session's total window count regardless of
      which bucket a window ends up in).
      First pass over `snap`: for every `r` with `r.Tmux != nil`, record
      `paneToRun[r.Tmux.PaneID] = r.ID`. Index windows by `w.Target()` (`I1`)
      and group panes onto their window by `p.Target` — the same field name,
      populated from the same `"session:window"` format string, so this is a
      direct map lookup, not a re-derivation. Sessions are ordered by name;
      windows within a session by `Index`; panes within a window by `Index` —
      all deterministic, never by map iteration order. A pane whose window was
      not listed is skipped, mirroring `deltasFromTmux`'s own "the two queries
      raced a window closing" handling.

      Window fields come straight from the matched `tmux.WindowOptions`:
      `Name`/`Active`/`Index` from `I1`'s new fields, `Branch` from `w.Branch`,
      `Task` from `w.Task`, and `CrewCodename` from `w.CrewName` — lazytmux's
      `@crew_name` is already treated as the codename by
      `runs/tmuxsource.go:126`'s own `Crew.Codename` assignment, so this reuses
      that same reading rather than inventing a new one.

      `RunID = paneToRun[p.PaneID]`, always, regardless of `p.ClaudeStatus`.
      `Agent = RunID != ""` — **derived from whether a Run actually joined,
      not from lazytmux's `@claude_status` option.** The two are not
      equivalent: `HookSource` can compose a `Run` for a pane from houston's
      own hook state (`hook/hook.go`'s `tmuxCoords()`) with no dependency on
      lazytmux at all, so a pane can be a real, routable agent run while
      `ClaudeStatus == ""`. Keying `Agent` off `ClaudeStatus` instead would
      make such a pane render as a plain shell — invisible to exactly the
      "open it as a run rather than duplicating it" requirement this tab
      exists for (`WORKER_TASK.md`'s correlation clause). Since `RunID` can
      only ever be non-empty when the registry actually composed a run for
      that pane id, `Agent` and `RunID` can never disagree by construction —
      there is no third state to reconcile.
- [ ] **Step 7: tests.** `server/workspace_test.go`: a session with one plain
      window (no branch/crew, one pane with `ClaudeStatus == ""` and no
      matching run — the ordinary shell case, asserts `Agent: false`); a
      session with a branch + crew window holding a pane that joins a
      `runs.Run` fixture by pane id where the fixture also sets `ClaudeStatus`
      (the ordinary lazytmux-enriched case — asserts `Agent: true`, `RunID`
      set); **a pane whose `ClaudeStatus == ""` but whose id matches a
      `runs.Run` fixture with `Tmux != nil`** (the hook-only case from Step 6
      — asserts `Agent: true`, `RunID` set, proving `Agent` is not reading
      `ClaudeStatus`); **the mirror case: a pane with `ClaudeStatus` set but
      whose id matches no run in `snap`** (asserts `Agent: false`, `RunID: ""`
      — pins "can never disagree" from the other direction too: lazytmux
      opinion alone, with no composed run, still does not make it routable); a
      pane whose window is absent from `wins` (asserts it is skipped); ordering
      (sessions/windows/panes out of tmux's own emission order come back
      sorted). **Bucketing**: one session with three windows sharing session
      name — one whose `GitRoot` is a key in `mainCheckouts` with value
      `true` (asserts it lands in `MainCheckout`), one whose `GitRoot` is a
      key with value `false` (asserts `Worktrees`), one whose `GitRoot` is
      `""` (asserts `Other`) — plus a fourth window whose `GitRoot` is
      non-empty but **absent** from `mainCheckouts` entirely (asserts
      `Worktrees`, the same as an explicit `false`, proving the "absent key"
      reading from Step 6's spec, not just the explicit-`false` one); assert
      `WindowCount == 4` regardless of bucket. Revert the sort, the "no
      matching window" skip, the `Agent = RunID != ""` derivation (swap it for
      `p.ClaudeStatus != ""`), and the bucketing logic (swap it for "everything
      goes to `Worktrees`") in turn and confirm each test goes red before
      restoring — the state-of-play retro requires this, not "it passed."

## C3 — the HTTP handler

- [ ] **Step 8: the injectable seam.**

      **`I3` — `workspaceLister`**, narrow enough to fake in tests, mirroring
      `runs/tmuxsource.go`'s unexported `lister` (which cannot be imported
      across packages) and the `replyRunner` field precedent in
      `server/server.go`:
      ```go
      type workspaceLister interface {
          ListWindowOptions() ([]tmux.WindowOptions, error)
          ListPaneOptions() ([]tmux.PaneOptions, error)
      }
      ```
      Add `wsTmux workspaceLister` to `Server`, defaulted to the same
      `tmuxClient` already constructed in `New` (`*tmux.Client` satisfies the
      interface structurally — no change to the existing `s.tmux` field or its
      35+ call sites).
- [ ] **Step 8a: the repo classifier.** New `server/workspace_repo.go`.

      **`I4` — `mainCheckoutChecker`**, narrow enough to fake in tests:
      ```go
      type mainCheckoutChecker interface {
          isMainCheckout(root string) bool
      }
      ```
      Real implementation `repoClassifier`, a `sync.Mutex`-guarded
      `map[string]bool` cache plus one `git` call per cache miss:
      `exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--path-format=absolute", "--git-common-dir")`,
      bounded by a timeout constant (mirror `crewDirTimeout`'s 5s in
      `runs/crewsource.go`). `root` is a main checkout iff the call succeeds
      AND `filepath.Dir(strings.TrimSpace(commonDir)) == root` — a linked
      worktree's common dir resolves to the *original* repo's `.git`,
      somewhere else entirely, while the main checkout's own `.git` is a
      direct child of `root`. An error (git missing, root no longer exists)
      caches `false`, not an error state — Step 6 already documents why
      "uncertain" reads as "not main" rather than blocking the tree. The
      cache is keyed on `root` only and never invalidated — a worktree
      changing identity under a live root without the process restarting is
      not a case this plan is taking on, matching `crewDir`'s own accepted
      "caches negative results for the process lifetime" trade-off.
      Add `wsRepos mainCheckoutChecker` to `Server`, defaulted to
      `newRepoClassifier()` in `New`.
      Tests in `server/workspace_repo_test.go`: a real temp dir `git init`'d
      as the main checkout → `true`; `git worktree add` a second temp dir from
      it → `false` for the worktree's root, `true` still for the original; a
      nonexistent path → `false`, not a panic or a hung call; a second call on
      an already-cached root does not shell out again (assert via a call
      counter on a fake `exec`-driving seam, or simply time-bound the test and
      trust the mutex-guarded map — pick whichever this codebase's existing
      `crewDir` test, if any, already established as the pattern).
- [ ] **Step 9: `handleWorkspace`.**
      `GET /api/workspace` → `Workspace`. 503 when `s.runs == nil`,
      `s.wsTmux == nil` or `s.wsRepos == nil` (unstarted registry, or a
      hand-built `&Server{}` in a test that never set the defaults — matches
      `handleRunsSnapshot`'s own guard for the first case; the rest exist
      because server tests construct `&Server{...}` by hand, per
      `server/runs_reply_test.go`, and a nil interface call panics rather than
      degrading to 502). On a `ListWindowOptions`/`ListPaneOptions` error, 502
      with the tmux error text — no special-casing of "no server running" the
      way `ListSessions` does: houston's own purpose is monitoring a live
      tmux, so a dead tmux server is a genuine failure to surface, not a quiet
      empty tree. Otherwise: collect the distinct non-empty `w.GitRoot` values
      across `wins`, call `s.wsRepos.isMainCheckout(root)` once per distinct
      root (never once per window or per pane — the whole point of the cache),
      build `mainCheckouts`, then
      `buildWorkspace(wins, panes, s.runs.Snapshot(), mainCheckouts)`,
      `Content-Type: application/json`, 200.
- [ ] **Step 10: register it.** One line in `server/server.go`:
      `apiMux.HandleFunc("GET /api/workspace", s.handleWorkspace)` — on
      `apiMux`, inheriting the token/origin/Host gates unchanged, matching
      Step 11 of the Crews plan verbatim in shape.
- [ ] **Step 11: tests.** `server/workspace_handler_test.go`, mirroring
      `runs_api_test.go`'s shape: 503 without a registry (and separately,
      without `wsRepos`); 200 with a fake `wsTmux`, a fake `wsRepos` (returning
      a canned answer per root — no real `git` call in this test file, that
      belongs to Step 8a's own test), and a registry carrying one composed
      run, decoding the body and asserting the joined pane's `run_id` AND that
      it landed in the right bucket; 502 when the fake `wsTmux` errors;
      `TestWorkspaceRouteIsBehindTheAuthGate` through `s.Handler()` exactly
      like `TestRunsRoutesAreBehindTheAuthGate` (401 unauthenticated) — a new
      route registered outside `apiMux` would reopen the hole #4 closed, so
      this is not optional boilerplate.
- [ ] **Step 12: verify C3.** `go build ./...`, `go vet ./...`,
      `go test ./server/... ./runs/... ./tmux/...`.

## C4 — the TS client and poll hook

- [ ] **Step 13: `ui/src/api/workspace.ts`.** `I2`'s mirror:
      ```ts
      export interface WorkspacePane { id: string; index: number; active: boolean; command: string; agent: boolean; run_id?: string }
      export interface WorkspaceWindow { index: number; name: string; active: boolean; branch?: string; task?: string; crew_codename?: string; panes: WorkspacePane[] }
      export interface WorkspaceSession { name: string; window_count: number; main_checkout?: WorkspaceWindow[]; worktrees?: WorkspaceWindow[]; other?: WorkspaceWindow[] }
      export interface Workspace { host: string; sessions: WorkspaceSession[] }
      export async function fetchWorkspace(): Promise<Workspace>
      ```
      (This superseded an earlier flat `windows: WorkspaceWindow[]` shape on
      `WorkspaceSession` — see the mid-execute revision note near the top of
      this plan for why. `useWorkspace` (Step 14) is unaffected: it only
      fetches and polls an opaque `Workspace` blob, never inspects
      `sessions[].*` itself.)
      `fetchWorkspace` throws on a non-2xx (the hook below is what turns that
      into a UI state) — same relative-URL, same-origin-cookie pattern
      `replyRun` already uses; no explicit `credentials` needed.
- [ ] **Step 14: `useWorkspace`.** New `ui/src/fleet/useWorkspace.ts`. Fetches
      once on mount, then on a `setInterval` (poll every 3s — a plain fixed
      interval, not tied to any registry source's own tick rate; `TmuxSource`'s
      2s cadence is an internal detail of the background composer, not a
      promise about API freshness); clears the interval on unmount. State:
      `{ workspace: Workspace | null, error: string | null, loading: boolean }`.
      A poll failure sets `error` but keeps the last-known `workspace` rather
      than blanking the view — the same "don't destroy state that's trivially
      re-derivable" instinct the design spec states for `Stale` (this is not
      `Stale` itself; nothing here composes into a `Run`).
- [ ] **Step 15: tests.** `useWorkspace.test.tsx`. `ui/src/hooks/useRuns.lifecycle.test.ts`
      is the closest precedent for the *shape* of a lifecycle test (`renderHook`
      with a named probe function, not an inline arrow — an inline arrow can
      trip `react-hooks/rules-of-hooks`, which Step 16's `npx eslint .` runs)
      and for fake timers, but it mocks `EventSource`, not `fetch` — there is
      no `global.fetch` mock precedent anywhere in `ui/src`. Mock the module
      instead, `vi.mock('../api/workspace')`, the same way
      `ui/src/fleet/ReplyComposer.test.tsx` mocks `../api/runs`: mount → one
      call to the mocked `fetchWorkspace` → state populated; advancing fake
      timers by the poll interval fires a second call; unmount stops further
      calls (assert the mock's call count does not grow after unmount); a
      rejected call sets `error` and leaves a prior `workspace` value in
      place.
- [ ] **Step 16: verify C4.** `npx tsc -b`, `npx eslint .`,
      `npx vitest run` (client + hook only at this point).

## C5 — the Workspace view

Design it twice, on purpose (the task's own instruction) — nesting is real:
session → repo-bucket → window → pane is four levels now (the repo roll-up
added one, mid-execute — see the revision note near the top of this plan),
names run long (branch names, worktree paths), and this has to stay
one-thumb usable.

**Option A — collapsible accordion.** Sessions (and optionally the
main-checkout/worktrees buckets) start collapsed; tapping a header expands it
in place. Pro: with many sessions, a phone shows more at a glance by default.
**Rejected**: this codebase has zero existing disclosure-widget precedent —
no collapse state, no expand/collapse animation, no `aria-expanded` pattern
anywhere in `ui/src/fleet/` — and Fleet/Crews have already trained the user
on one interaction model, "scan a flat list under a section header, tap a
card." Inventing a second interaction primitive on top of that for one tab is
a second visual *and* interaction language, which the task explicitly
forbids. It also doesn't earn its complexity at this data's actual scale — a
personal dev machine carries a handful of tmux sessions, not hundreds — so
the "more at a glance" win is close to zero in practice. The dispatcher's own
phrase for the `Worktrees` bucket, "collapsed under a count", means labelled
and de-emphasised under one small header, **not** hidden behind a tap — the
tab's whole purpose is that every pane stays reachable, and an accordion
would put the worktree panes (often where the actual agent work lives) behind
an extra gesture.

**Option B — flat grouped scroll (chosen).** No collapse state at all, one
extra nesting level over the original two-level design. Sessions render as
`.fleet-group`-style section headers (exact reuse of the class Fleet already
uses for its host grouping — same visual weight, same place in the eye),
carrying the session name and `window_count`. Within a session, up to three
repo-bucket sub-groups render in a fixed order — Main checkout, Worktrees
(N), Other — each only when its window list is non-empty (a session with no
worktree windows shows no `Worktrees` header at all, not an empty one).
Within a bucket, each window is a compact one-line sub-header (index/name,
plus a branch chip and a crew-codename chip *only* when present, reusing
`.run-chip`/`.run-chip.codename` verbatim). Panes render as a tight list of
one-line rows under their window — `command` as the label, a small dot for
`active`, and a tap target only when `pane.agent` (which by Step 6's
construction always carries a `run_id` when true — a plain pane renders
inert, same "affordance from capability, not from type" rule `Caps` already
uses for runs). Still a pure scroll, still zero new CSS interaction states —
the extra level is a heavier section header, not a new gesture.

- [ ] **Step 17: `ui/src/fleet/WorkspaceView.tsx`.** Props: `{ onOpen?: (runId: string) => void }`
      (deliberately takes a bare `run_id` string, not a `Run` — `WorkspaceView`
      never holds a `Run[]`, only the tree; `Shell.tsx` maps the id to the same
      hash-navigation `FleetView`/`CrewsView` already use). Uses `useWorkspace`
      internally. Unlike `FleetView`/`CrewsView`, which take `runs: Run[]` as a
      prop and correlate live via `useRuns()`, `WorkspaceView` takes no
      `Run[]` at all — correlation already happened server-side (C3), so this
      is the one place in `ui/src/fleet/` that does not consume `useRuns()`,
      and the file's top comment says why in one line, pointing at this plan.
      A small internal helper renders one `[label, windows]` bucket
      (`("Main checkout", session.main_checkout)`, `("Worktrees (N)",
      session.worktrees)` with `N = session.worktrees?.length ?? 0` baked into
      the label, `("Other", session.other)`) and is called up to three times
      per session, skipping any bucket whose list is empty/undefined — this
      keeps the "only when non-empty" rule in one place instead of three
      near-identical `{cond && …}` blocks. Loading state before the first
      successful fetch; error state on `error && !workspace` (an error *with*
      a prior workspace renders the tree, stale, same instinct as Step 14);
      empty state when `sessions.length === 0`.
- [ ] **Step 18: CSS.** New rules in `ui/src/fleet/fleet.css` (same file every
      other fleet view already extends — no second stylesheet): `.workspace`
      (root, mirrors `.crews`/`.fleet`), `.ws-bucket` (the Main
      checkout/Worktrees/Other sub-header, one visual step lighter than
      `.fleet-group` so the session header stays the dominant grouping level),
      `.ws-window` (window sub-header row), `.ws-pane` (pane row, plain and
      inert by default), `.ws-pane.agent` (tappable — cursor/hover per the
      existing `.run-card:active` scale-down convention). Reuses
      `.fleet-group`, `.run-chip`, `.run-chip.codename` unmodified.
- [ ] **Step 19: tests.** `WorkspaceView.test.tsx`, `vi.mock('./useWorkspace')`
      (the `RunDetail.test.tsx`/`TerminalPane.test.tsx` pattern of mocking the
      owning hook rather than the network) returning a fixed `Workspace`
      fixture per case — no interval to fake here, since the hook itself is
      replaced wholesale. Fixture: one session with `main_checkout` holding one
      window with a branch + `crew_codename` and one agent pane
      (`agent: true, run_id: "pane-1"`, `command: "claude"`); the same session's
      `worktrees` holding a plain window with two non-agent panes that are
      otherwise distinguishable (different `id`/`command`, e.g. a `"zsh"` pane
      and a `"vim"` pane, both `agent: false`, no `run_id`); no `other` on this
      session (asserts no `Other` bucket header renders at all) — plus a
      *second* session whose only window has an empty `branch`/`GitRoot`
      surfaced through `other` (asserts an `Other` header DOES render when the
      data calls for it, closing the "only render when non-empty" claim from
      both directions). Assert: the `Main checkout` and `Worktrees` bucket
      headers render for session one with the right window under each (and the
      worktree count in the `Worktrees` label matches); clicking the agent
      pane's DOM element (via `container.querySelector('.ws-pane')`, the
      `CrewsView.test.tsx` style) calls `onOpen` with exactly `"pane-1"`; and
      clicking either plain pane does not call `onOpen` at all. Loading/error/
      empty states each get one case (mock the hook to return each state
      directly).
- [ ] **Step 20: verify C5.** `npx tsc -b`, `npx eslint .`, `npx vitest run`.

## C6 — Shell wiring

- [ ] **Step 21: wire it in.** In `ui/src/fleet/Shell.tsx`: add
      `import { WorkspaceView } from './WorkspaceView'`; delete the
      `workspace:` entry from the `PLACEHOLDER` object literal and narrow its
      type to `Record<'dispatch', string>` (only one key survives, same move
      as Step 30 of the Crews plan narrowing it to
      `Exclude<Tab, 'fleet' | 'crews'>` — both the type and the literal must
      drop `workspace`, or the literal fails an excess-property check); the
      existing catch-all render at line 63,
      `{tab !== 'fleet' && tab !== 'crews' && <div className="shell-placeholder">{PLACEHOLDER[tab]}</div>}`,
      no longer typechecks once `PLACEHOLDER` only has a `dispatch` key — replace
      it with `{tab === 'dispatch' && <div className="shell-placeholder">{PLACEHOLDER.dispatch}</div>}`;
      add, alongside it,
      `{tab === 'workspace' && <WorkspaceView onOpen={(id) => { window.location.hash = `#/fleet/${id}/activity` }} />}`
      as a plain conditional mount (not `hidden`-kept, per the "Why this is a
      plain GET" note above — Workspace has no state worth preserving across a
      tab switch, so mounting it fresh each time is correct, not a shortcut).
      Nothing else in this file changes — not the Dispatch tab's copy, not the
      tab bar markup.
- [ ] **Step 22: verify C6.** `npx tsc -b`, `npx eslint .`, `npx vitest run`.

## C7 — the proofs no component can give

- [ ] **Step 23: full gate.** Go build, vet, test; UI typecheck, lint, tests;
      `gofmt -l` clean on touched files.
- [ ] **Step 24: bundle grep.** `npm run build`, then grep the built CSS for a
      declaration of `.workspace`, `.ws-bucket`, `.ws-window`, `.ws-pane`,
      `.ws-pane.agent`, and the built JS for the string `/api/workspace`. A
      passing build proves
      nothing here — the entire Mocha token file once shipped unimported
      behind a fully green build, lint, tests and an Opus review.
- [ ] **Step 25: live before/after, if a tmux server is reachable in this
      environment.** Hit the built binary's `/api/workspace` against the real
      local tmux (`curl` is enough — no browser needed for the data-level
      check). `/api/workspace` sits behind the token gate like every other
      `/api/` route: read `<state-dir>/token` and pass it as
      `Authorization: Bearer <token>` (the `?token=` query form is WS-upgrade
      only), against a `Host` the server recognises (`localhost`/`127.0.0.1`
      need no `-hostname` flag). Confirm: every currently open pane appears
      exactly once, an agent pane in the current worktree carries a `run_id`
      that also appears in `/api/runs`, and a plain shell pane carries
      `agent: false` and no `run_id`.
- [ ] **Step 26: PR body.** State plainly whether a browser was available to
      look at the rendered tree — if not, say so exactly as the Crews-tab PR
      did, do not claim the rendering is verified. Include the curl evidence
      from Step 25, the two-design note from C5 verbatim or summarized, and
      any escalations from plan-critic revisions.
