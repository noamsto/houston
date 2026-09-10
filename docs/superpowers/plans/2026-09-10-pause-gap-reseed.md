# Re-seeding at the end of a `%pause` gap — Design Record

houston #34. Closes defect 3 of
`docs/superpowers/2026-09-08-state-of-play.md`, "Known defects and follow-ups".

## What was wrong

`tmux/control_client.go` marked every subscriber of a pane dirty the instant
`%pause %N` arrived (added in Task 3 of
`2026-09-07-control-mode-transport-correctness.md`). A `Dirty` event tells
`server/pane_ws.go` to re-seed from `capture-pane`. That capture ran at the
**start** of the output gap, not the end:

- tmux discards everything a paused pane paints. On `%continue` it resumes
  from the pane's current offset — the backlog is dropped, not replayed.
- A `capture-pane` taken at `%pause` therefore reflects the screen *before*
  the discarded output existed.
- When output resumes, houston paints new bytes onto a snapshot that
  predates the hole. The screen is wrong and stays wrong until the next
  unrelated re-seed.

A second, independent half of the same defect: nothing resumes a pause
houston did not issue itself. The seed handshake pauses and continues a pane
deliberately, but a `%pause` tmux raises on its own — the only kind live
today, since nothing sets `pause-after` — has no owner, so the pane would
stay paused forever.

Both halves are dormant in the current deployment: `pause-after` is 0 by
default and houston never sets it, so the only `%pause` on the wire is
houston's own handshake, which continues it a moment later. The fix had to be
correct before flow control could ever turn that dormancy off.

## The tmux facts the fix rests on

Verified against tmux 3.6a's `control.c`:

- `%pause %N` fires only on the not-paused → paused transition; `%continue
  %N` only on the reverse. Pausing an already-paused pane, or continuing one
  that isn't paused, emits nothing. The notifications are edge-triggered, so
  tracking gap state as a per-pane flag is sound and a *count* of anything
  is not — see "Why not a counter" below.
- `pause-after` is a client option (`CLIENT_CONTROL_PAUSEAFTER`), set with
  `refresh-client -f pause-after=N`. It is what would make tmux pause a pane
  on its own, as flow control against a slow reader; today it is unset, so
  every `%pause` on the wire is one houston asked for via
  `refresh-client -A %N:pause`.
- `capture-pane` runs over the tmux CLI (`exec.Command`, a separate process
  per call); `RunCommand` runs over the control-mode connection and is
  answered by `readLoop` itself, via the `%begin`/`%end` block the command
  produces. `readLoop` is the single reader for every pane on the
  connection, so it must never block on a round trip it alone can complete —
  a resume issued from `readLoop` would deadlock the client against itself.

## Mechanism

A `%pause %N` opens a **gap** on pane `N` and arms a **10-second deadline**.
The gap is closed exactly once, by whichever of two paths claims it first:

- **`%continue %N`** (`closeGap`) — the healthy path. Deletes the gap record
  and marks dirty only the subscribers that were flagged `inGap` when the
  gap opened; a subscriber that attached during the gap is left alone.
- **the deadline expiring** (`expireGap`) — the unhealthy path. Sends one
  `refresh-client -A %N:continue`, then marks dirty *every* subscriber
  currently on the pane, including ones that attached mid-gap.

State lives in two places, both guarded by `cc.mu`: `ControlClient.gaps
map[string]*gap` holds the open gap per pane (just a `*time.Timer`), and each
`PaneSub.inGap bool` records whether that subscriber predates the gap's
opening. Putting `inGap` on the subscription handle rather than in a list
hanging off the gap record means `Unsubscribe` during a gap drops the
subscriber and its buffered output immediately, instead of the gap retaining
a reference to a subscriber nobody is reading from anymore.

Deleting the gap record is the claim: `closeGap` and `expireGap` both read
`cc.gaps[paneID]` and delete it under the same `cc.mu` critical section, so a
`%continue` racing the deadline produces exactly one marking pass and at
most one resume. `expireGap` additionally checks `cc.gaps[paneID] == g`
before deleting — a timer whose `Stop()` lost that race must claim only the
gap it was armed for, never a newer one that has since opened on the same
pane.

**The resume precedes the marking, and that ordering is the entire point of
the change.** On the deadline path the pane is still paused when the timer
fires. Marking first would send the re-seeding consumer into `capture-pane`
while tmux is still discarding output — everything painted between that
capture and the resume actually landing would be lost, and the subscriber,
having already acked, would never re-seed again. That is defect 3 rebuilt on
the path that exists to close it. `expireGap` therefore releases `cc.mu`,
calls `cc.RunCommand("refresh-client -A " + paneID + ":continue")` from the
timer's own goroutine, and only re-takes the lock to mark subscribers dirty
once that call returns — successfully or not. `RunCommand` serializes on
`cmdMu`, so the resume's `%begin`/`%end` block cannot be misdelivered to a
different pending caller (in particular, not to the handshake's own pause
command — a misdelivered `%error` there would seed an unpaused pane, exactly
the corruption this work exists to prevent).

A resume that fails — tmux answering `%error`, or `RunCommand` returning early
because the connection is gone — leaves the pane possibly still paused, and
`%pause` is edge-triggered, so tmux will never announce it again. Dropping the
gap there would rebuild the original defect with no bound at all: the
subscriber re-seeds once, acks, and waits forever on output tmux is still
discarding. `expireGap` therefore **re-opens the gap** on failure, arming a
fresh deadline that retries the resume, and logs the failure at `Warn` — the
only signal a pane is stuck, so it must be visible without `-debug`. The
re-arm is gated on the pane still having a subscriber: with nobody watching,
the retry would loop for the life of the client for no one.

**The failed lap marks nobody**, which is the same rule as on the success path
— mark only once the resume has landed — applied where it actually bites.
Marking against a pane that is still paused is not merely a wasted capture, it
destroys both recovery paths at once: the consumer captures the pre-gap screen,
and the subscriber's `dirty` flag stays set across the round trip, so the
`markDirtyLocked` that the re-armed gap's own `closeGap` (or its next
`expireGap`) performs is a no-op. No gap, no pending `Dirty` — the terminal is
silently missing everything tmux discarded until the page is reloaded. Not
marking costs nothing by comparison: the subscriber is still mid-gap, so its
stream is already stopped, and the mark it is owed arrives from whichever path
closes the re-armed gap — by construction, against a pane that is live.

Releasing `cc.mu` before the `RunCommand` call, rather than holding it across
the round trip, keeps a blocked PTY write out of `dispatch` — a `RunCommand`
call parked behind a stalled write would otherwise stall every pane on the
connection, not just the one whose gap is expiring.

Gap state is swept in the two places a connection's pause state stops being
meaningful. `markAllDirty`, called on every reconnect, calls
`clearGapsLocked()` before its own marking loop: pause state belongs to the
connection that raised it, so a gap left open by a dead connection must not
let a `%continue` arriving on the new one re-seed anybody. `Close()` calls
`clearGaps()` after setting the closed flag; `openGap` itself checks
`isClosed()` and refuses to open a new gap once closed, because `readLoop`
keeps parsing already-buffered lines after `Close()` returns, and a `%pause`
handled after the sweep would otherwise arm a full-length timer that outlives
the client it belongs to.

### Why not an expected-continue counter

The state-of-play entry that opened this work proposed "an expected-continue
counter." A count of *issued* pause commands desynchronises from tmux's
notifications by construction: `%pause`/`%continue` are edge-triggered, so a
second `refresh-client -A %N:pause` sent while the pane is already paused
produces no second `%pause` to balance against it, and a counter driven by
commands sent would overcount relative to the transitions tmux actually
reports. A counter driven by notifications received degenerates to exactly
the boolean this design uses — "is there an open gap on this pane" — so the
counter framing adds a dimension the edge-triggered protocol doesn't have.
What replaces it is a single gap record per pane plus a per-subscriber
`inGap` flag, both driven directly off the transitions tmux actually emits.

## The two designs considered

**Chosen: an edge-triggered gap with a deadline**, as described above — a gap
opens on `%pause`, closes on `%continue`, and a timer bounds how long it can
stay open before houston resumes it unilaterally.

**Rejected: attribute pauses by recognising the command as it passes through
`RunCommand`.** The alternative was to have `ControlClient` notice when a
caller sends `refresh-client -A %N:pause` through `RunCommand`, and treat
only *that* pause as houston's own — deferring its dirty marking — while
treating any other `%pause` (one tmux raised on its own, once flow control
is enabled) as needing no resume tracking at all, on the theory that only
houston-issued pauses need special handling.

It lost for two reasons:

- It couples `tmux/` to the exact string a caller in `server/pane_ws.go`
  composes. `tmux/` has no other dependency on how `server/` spells its
  commands, and the coupling fails silently: a reformatted command string,
  or a second call site composing the same command slightly differently,
  stops being recognised with no compile error and no test signal beyond a
  screen going stale in production.
- It still needs a separate liveness bound for a gap whose `%continue` never
  arrives — a stuck handshake, a wedged connection — which is exactly what
  the deadline already provides. Attribution by string-matching would have
  to be built *and* a timeout mechanism layered on top of it; the deadline
  alone does both jobs, since it doesn't care who asked for the pause, only
  whether it closed in time.

Ownership tracking — "was this `%pause` houston's own?" — is deliberately
absent from the shipped design for the same reason: houston's own handshake
pause is continued long before the deadline would ever fire, so the deadline
never fights it, and on the rare occasion the handshake itself overruns (see
"What it supersedes" below), the deadline's behaviour — resume, then mark
everyone — is correct anyway. No part of `tmux/` needs to know who issued a
pause, which is what keeps this fix inside `tmux/`: the pause command is a
free-form string composed in `server/pane_ws.go`, and recognising it in
`tmux/` would couple the two packages through that spelling for no benefit
the deadline doesn't already provide.

## What it supersedes

This replaces Task 3 of `2026-09-07-control-mode-transport-correctness.md`,
"`%pause` marks the stream dirty." That task's load-bearing rule — mark on
`%pause` only, never on `%continue` — is superseded, but its underlying
concern is not discarded; it moves into `openGap`/`closeGap`: a subscriber
that attaches mid-gap must not be re-seeded by that gap's `%continue`, or the
seed handshake (which pauses the pane *before* it subscribes) would earn a
spurious re-seed immediately after every deliberate seed.

The caveat that rule carried is unchanged and still applies: tmux does not
guarantee `%pause` precedes the pausing command's `%end`. On the rare reorder
the handshake's own subscriber exists before its `%pause` is parsed, so that
subscriber is itself `inGap` when the gap opens and earns one spurious
re-seed at `%continue` — the same cost the original design paid, on the same
rare ordering.

## What is left open

- The retry is unbounded. A live pane whose resume keeps erroring re-arms once
  per deadline, forever; nothing caps the attempts or backs the interval off.
  That is deliberate — giving up would leave the pane dark, which is the exact
  failure this change exists to prevent — and since a failed lap marks nobody,
  each lap costs one `refresh-client` and one `Warn`, not a re-seed. It ends on
  a successful resume, the last subscriber leaving, a reconnect
  (`markAllDirty` → `clearGapsLocked`), or `Close()`.
- A fresh `%pause` arriving while a previous gap's resume is still in flight
  (i.e. a new pause on the same pane racing `expireGap`'s `RunCommand` call)
  opens a new gap and waits out a full second deadline of its own. Nothing
  short-circuits it against the resume already in progress.
- A deadline goroutine that wins its claim on `cc.gaps[paneID]` *before*
  `Close()` runs can still be inside `RunCommand` when `Close()` returns, and
  can still queue a `Dirty` afterward. `clearGaps()` only cancels timers and
  forgets gap records that exist at sweep time; it cannot recall a goroutine
  already parked past that point. The `Dirty` lands on a subscriber that is
  about to be torn down along with the rest of the closed client, so it is
  inert in practice, but it is not prevented by construction.

## Follow-ups

- **Enable `refresh-client -f pause-after=N`.** This is a product decision,
  not a mechanical one: it replaces tmux's current behaviour for a slow
  control client — killing it — with pausing and re-seeding it, which this
  change now makes safe to do. Turning it on should revisit the 10-second
  deadline, since at that point it doubles as the backpressure interval, not
  just a liveness bound on an already-rare stuck pause.
- **Bound the WebSocket seed write.** The 10-second deadline is a stated
  bound on the pause→continue window, not a proof of one: the handshake's
  window contains three `fork`/`exec` tmux round trips and two WebSocket
  writes, and no write deadline is set anywhere in the repo, so a
  backgrounded phone on a stalled TCP connection can in principle exceed any
  finite deadline houston chooses. When that happens the consequence is
  already benign by construction — the seeding socket is marked dirty by the
  deadline path and re-seeds once — but making the 10-second figure a proof
  rather than a statement requires bounding that write, which lives in
  `server/` and is out of this change's scope.
