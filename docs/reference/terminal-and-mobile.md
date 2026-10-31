# Terminal, WebSocket, mobile UI, stream liveness

Read when changing the terminal WS protocol, input routes, mobile gestures/composer or SSE reopen logic.

Moved verbatim from the root `CLAUDE.md`; section headings are unchanged.

## Mobile Features

- **Wide terminal**: fixed-width container (~120 columns), CSS-scaled to fit the viewport
- **Free one-finger pan**: a single finger drags in both axes at once (after an 8px slop) — horizontal movement pans, vertical movement scrolls terminal history; there is no axis lock
- **Pinch-to-zoom**: Two-finger pinch with focal-point tracking
- **Alternate-screen apps** (pi, `less`, vim): the app has no tmux scrollback, so xterm has nothing to scroll. The WS `meta` frame carries `alternate_on` and `mouse_on` (`#{alternate_on}`; `mouse_on` = `#{mouse_any_flag}` and `#{mouse_sgr_flag}`), read by the 1 s meta poll and re-sent when they change. While `alternate_on`, a vertical one-finger drag (mobile) or a wheel turn (desktop) goes to the app over the WS `scroll` message instead of `scrollLines` (`lib/altScroll.ts`; `usePaneSocket.sendScroll(mode, data)`, `mode` `wheel` or `page`): with `mouse_on`, SGR wheel reports `\x1b[<64;X;YM` (up) / `\x1b[<65;X;YM` (down) at the 1-based cell under the finger or cursor, one per 24 px of travel; otherwise PageUp/PageDown, one per 160 px. The `meta` the UI acts on is up to a second old, so the server re-reads the pane (`tmux.Client.PaneScrollState`, one `display-message`) just before sending and drops the scroll unless the pane is still on the alternate screen and not in a mode (copy mode), and, for `wheel`, still tracking the mouse in SGR; an unreadable state or an unknown `mode` drops it too. Typed `input` is not gated. `ControlClient.SendKeys` writes a mouse report with `send-keys -H` so it reaches the app as one sequence. Horizontal pan, pinch and double-tap are unchanged; the normal screen (Claude with real history) keeps xterm scrolling, and the Detached / "↓ Live" logic does not apply to the app's own scroll.
- **Run tabs in the bottom bar** (`MobileShell`, `RunDetail tabsInBar`): while a run detail is open the `.shell-tabs` bar holds a leading Back button (`.run-tabs-back`: `‹` plus the destination, see "Navigation") followed by a `role="tablist"` of the run's tabs (`role="tab"`, `aria-selected`, ≥44 px, same height as the shell tabs), and the top tab row is not rendered; desktop `ConsoleShell` keeps the top row. A run with no tabs (or still loading) shows just Back. Holding Back for 500 ms pops to the shell root (`popToRoot`; the title says so). The bar hides with the on-screen keyboard (`keyboard-open`) like the shell tabs. The old landscape-terminal collapse of the header/bar was removed, since it would have hidden both the tabs and Back. The bar highlights from the route and `caps` alone, so it may differ from the body only for a Terminal route whose capability was lost after going live.
- **Edge swipe back** (`hooks/useEdgeSwipeBack.ts`, mobile run-detail overlay only; `RunDetail edgeSwipeBack`, off in `ConsoleShell`): a touch starting within 24 px of the overlay's left edge that moves rightward and horizontal-dominant (decided after 10 px of travel) drags the overlay with the finger (`translateX`, list showing through) and commits past 38% of the width, or a flick of ≥ 0.5 px/ms over ≥ 40 px, by calling the same `back()` as the header and bottom-bar Back buttons (see "Navigation"); otherwise it springs back. A vertical-dominant or leftward start cancels. The listeners are capture-phase on the overlay and stop an edge touch from reaching descendants, so the terminal's one-finger pan, chat scroll and horizontally scrollable rows never see it; any other start behaves as before (the terminal's pan therefore does not start in the leftmost 24 px). The on-screen keyboard does not disable it (the zone is never a field). `prefers-reduced-motion`: no follow-the-finger, just commit or cancel. Safari's own edge swipe: a `touchcancel` never commits, and a commit is dropped if `location.hash` changed since the touch began, so the two cannot both navigate. If `onBack` leaves the hash unchanged (a swallowed pop), the overlay springs back instead of being stranded off-screen. `.run-detail` sets `overscroll-behavior-x: none`.
- **Detached mode**: horizontal finger movement (≥8px that actually pans, drag, pinch or the column scrubber) or ending a drag scrolled up detaches the view from the live cursor — output no longer auto-pans it, a reconnect reseed with unchanged dims keeps the pan and distance from the bottom, and a "↓ Live" pill appears bottom-centre (a keyboard/container resize still re-snaps vertically). Re-attach = tapping the pill, or a drag that starts scrolled up and ends at the bottom edge; either snaps to the cursor and follows it immediately
- **Composer** (`MobileInputBar.tsx`, docked under the terminal in the run-detail Terminal tab): multi-line field where Enter inserts a newline and Send (or Ctrl/Cmd+Enter) sends the text followed by Enter; an empty Send presses Enter. Both composers share one layout (`ComposerControls.tsx`, `composer.css`): a full-width auto-growing field (about five lines), then one action row with attach, voice (Web Speech API), the `/` toggle and a primary Send on the right (Chat adds Esc). File attach: picking a file only stages it as a thumbnail chip (× removes it), and Send then posts image plus text as one `{type:'image'}` input.
  Both composers (this one and the Chat tab's) keep their draft per run in localStorage under separate keys (`houston-draft:run:<id>` for this Terminal composer, `houston-draft:chat:<id>` for Chat; `useComposerDraft`) and clear it only after a confirmed successful send, even one that lands after the composer remounted; a failed or timed-out send shows the error and keeps the text and any staged image. A draft expires after 24h; expired or malformed entries are swept on first use per page load. For a pane run both composers key by `run.draft_key` instead of the run id (`draftKeyOf`, `runs/draftkey.go`: a one-way hash of tmux server pid + pane id + session id, `omitempty` on the wire, so the raw session id never leaves the server), because pane ids restart at `%0` after a tmux server restart so a draft does not carry over to the next agent that gets the pane id; a run with no pane (no `draft_key`) keeps its run id. When houston knows the run's session id (hooks layer, or crew `engine_session`), a new session in the same pane (`/clear`) starts with an empty composer; a tmux-only run with no session id, or an unknown server pid, is keyed by what is known and leans on the 24h expiry. The staged image is held in memory only, so it is lost when the composer unmounts or the page reloads.
- **Quick keys**: one horizontally scrollable row — Esc, ^C (asks Confirm/Cancel inline before sending), Enter, Tab, Shift+Tab, ↑/↓, 1–5, Y/N, Alt+P, ^O, ^Z, `/copy`. All but `/copy` are keystrokes: `POST /api/runs/:id/input` `{type:'key', key:...}` (400 if `key` isn't in the `terminalKeys` allowlist). No implicit Enter, so a digit answers a numbered prompt without a stray Enter.
- **Quick commands** (`QuickCommands.tsx`, claude runs only — `run.agent` `claude`/`claude-code`; the `/` toggle sits in the action row of both composers, and its pills open above the field, below the quick keys in the Terminal tab): the toggle reveals `/compact`, `/compact…` (prefills the composer with `/compact `), `/clear` (inline Confirm/Cancel first, since it wipes the context) and `/context`, sent as text + Enter. All are disabled unless the run's `state` is `idle` (`quickCommands.ts`; a hook `waiting` is already demoted to `idle`), with the reason in the pill title. A `↳ suggested` pill appears when the very last chat item is a non-commentary assistant message with a fenced block whose body is a single line starting with a slash command (`/name …`; multi-line blocks and absolute paths like `/home/u/x.sh` are ignored; derived client-side from the chat updates, no server support); it opens an editable sheet whose Send posts the edited text, and the pill stays hidden after a successful send until the suggestion changes (and shows again if the same command is suggested later). `/compact…` prefills only an empty composer (a non-empty draft or a staged image is never overwritten or turned into an image prompt; a note asks to clear the draft and attachment first) and then goes through the normal composer Send, which is not idle-guarded. Feedback line: `Sent … — waiting`, then `… accepted` once the run leaves `idle`, or after 10 s `No reaction seen yet` (for `/compact`) / `Sent — this command may not change the run state; check the terminal.` (other commands, which can finish without a state change).
- **Choices**: when the pane `meta.choices` is present, each choice renders as a tappable `n. label` button above the row and answers with its ordinal key.
- **Answering a prompt** (Chat tab, Claude runs; see "Answering from Chat"): a pending `AskUserQuestion` renders as a staged picker with ≥44 px option rows (`--tap`) and one "Send answer" button; the Other text field opens on tap, takes focus and scrolls itself into view (again on every `visualViewport` resize while focused), so it works with the on-screen keyboard open. A permission dialog is pinned above the timeline as `PermissionBar`: the question, a tap-to-expand detail (the command, clamped to 8 lines until expanded), and one ≥44 px button per choice.
- **Keyboard**: `useKeyboardInset` tracks the on-screen keyboard via `visualViewport`; `MobileShell` shortens itself to sit above it and hides the tab bar so the terminal and composer keep the space.

On desktop the Terminal tab takes keystrokes directly (xterm `onData` → WS `input`); clicking it focuses it.

There is no WIDE/FIT toggle — that affordance was removed. A terminal rework
(font-size zoom, possibly more) is in progress; revisit this
section once it ships rather than trusting the bullets above for anything not
already verified in `TerminalPane.tsx` / `useTouchGestures.ts`.


## WebSocket Protocol

The primary terminal route is `GET /api/runs/:id/terminal`
(`server/runs_terminal.go`), which resolves the run to its live tmux pane and
upgrades to the same bidirectional WebSocket envelope described below. Pane
resolution happens before the upgrade, so a bad address never reaches
`Upgrade()` — it comes back as a plain HTTP error: 503 if the run registry
isn't started, 404 for an unknown run, 409 if the run has no terminal
capability/pane, 409 again if tmux confirms the pane id no longer exists (it
exited or the session is gone), 409 again if the run's recorded tmux server
and the freshly resolved pane's server are both known and disagree (see
"Point-of-use server check" in `docs/reference/fleet-runs.md`), or 503 `tmux unavailable` if tmux itself
couldn't be reached (missing binary, timeout, wrong socket permissions, or a
client/server protocol mismatch after an upgrade left a stale tmux server
running).

Past the upgrade, a run-route socket can still close with 4409 "tmux server
changed" (the pane is gone or has moved to a different server) or 1011
"could not verify tmux server" (the check itself failed) — see "Point-of-use
server check" in `docs/reference/fleet-runs.md`.

The run route carries a JSON envelope, `{"type":"...","data":{...}}`
(`server/pane_ws.go`, `ui/src/hooks/usePaneSocket.ts`,
`ui/src/api/terminal.ts` for address/URL selection):

**Server → Client:**
- `dims` — Pane dimensions (cols/rows) to resize xterm.js to match. Sent at connect and again whenever the 1 s meta poll sees the pane's size change (another client attaching, a zoom toggled elsewhere), each time followed by a fresh `seed`: a TUI redraws relative to the screen height, so an xterm left at the old size scrolls Claude's prompt box and status bar instead of repainting them in place.
- `seed` — Full snapshot of pane content plus `cursor` (`{x, y}`, 0-based within the visible screen, read in the same tmux call as the capture); also used for a mid-stream re-seed (a dropped-output gap or a resize) — there is no separate `reseed` type on the wire. The UI clears scrollback in the write queue before every seed (the seed repeats the pane's history, so keeping the old scrollback shows it twice) and parks the cursor at `cursor` (the lines are written one after another, which leaves it at the end of the last line, and a shell echoes keystrokes without positioning them).
- `output` — Incremental terminal data
- `meta` — Pane metadata (agent type, status, mode, activity, choices)

**Client → Server:**
- `input` — `{data: <text>}`, sent verbatim to the pane (no implicit Enter)
- `resize` — `{cols, rows}`, request terminal resize
- `scroll` — `{mode: "wheel"|"page", data}`: scroll bytes for an app on the alternate screen (see "Mobile Features"). Sent like `input` only if the server's fresh check of the pane passes; otherwise dropped silently.

There is no `resize-done` acknowledgment, and no `special:<key>` message type
on either WebSocket — the WS `input` message always carries raw keystrokes.

Non-typing input (quick keys, images) instead goes through a POST route, and
the allowlist that bounds it lives there, not on the socket:

- `POST /api/runs/:id/input` (run address) — same resolution ladder as the WS
  route (503/404/409/409/409/503) before the body is even read. Body is one of:
  `{"type":"text","text":"..."}` (literal text, then Enter — `tmux.Client.SendKeys` sends it as
  `send-keys -l -- <chunk>` in ≤ 8 KiB chunks, backslash-escaping a chunk's trailing `;` because tmux
  drops it as a command separator, and waits `enterSettle` before Enter so a TUI agent doesn't read
  text+Enter as one paste and turn the Enter into a newline),
  `{"type":"key","key":"<terminalKeys>"}` (one key, no Enter — 400 if `key`
  isn't in the `terminalKeys` allowlist in `server/runs_terminal.go`: Escape,
  C-c, Enter, Tab, BTab, Up, Down, M-p, C-o, C-z, y, n, 1–9), or
  `{"type":"image","text":"...","images":[...]}` (temp-file paths + text,
  then Enter). Text and image `text` longer than 256 KiB is 413 `text too long`
  before any `send-keys`. `SendKeys` uses the request context, so a client
  disconnect stops later chunks and never sends Enter after cancel. Max body 50 MiB; an
  oversized body is 413. A send that already delivered a chunk and then fails
  or is cancelled is 502 with body `{"partial":true}`. Success is 204 with no body. Because the allowlist
  tops out at `9`, a choice past the 9th ordinal has no key to send.
  `MobileInputBar.tsx` sends through `ui/src/api/terminal.ts`'s run-address
  helpers.
  `/input` is unchecked: it types whatever it is given. Answering a question or permission dialog goes through `POST /api/runs/:id/answer` instead, which verifies the pane first (see "Answering from Chat").

## Stream liveness

All SSE streams, `/api/runs/stream`, the chat stream and the crew feed stream, send a named
`ping` event (`event: ping`, empty data — not a comment, so `EventSource`
listeners see it) every 25 s. On the runs stream a ping tick owed a resync
(a dropped update for that subscriber) sends a full `snapshot` instead.

A phone that slept leaves EventSource and WebSocket connections half-open with
no error ever firing, so the UI reopens streams itself
(`ui/src/hooks/streamLiveness.ts`): `useRuns`, `useRunChat` and `useCrewFeed` reopen their
stream when the page was hidden ≥ 10 s, or when a visible page has heard
nothing (message or ping) for 60 s. `usePaneSocket` replaces its terminal
socket on return after ≥ 10 s hidden at once, without waiting for the old
socket's close, and ignores that socket's later events. After a shorter hidden
spell an open connection is left alone; the terminal socket still reconnects at
once if it is not open.


