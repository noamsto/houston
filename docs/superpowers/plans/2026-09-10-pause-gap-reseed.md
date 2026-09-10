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

Both halves are dormant in the current deployment, for **two** independent
reasons — the first round of this work recorded only one.

- `pause-after` is 0 by default and houston never sets it, so tmux raises no
  `%pause` of its own.
- The seed handshake's pause does not parse either. `server/pane_ws.go:120`,
  `:132` and `:178` build `refresh-client -A %N:pause` / `:continue` as bare
  tokens, which tmux's command-string lexer rejects (see "The tmux facts"
  below); `:121` swallows the `%error` at `slog.Debug` and proceeds with
  `paused = false`. So houston's own handshake has never paused a pane either,
  and there is no `%pause` on the wire at all.

The second reason means this design had not been exercised against a live
`%pause` before the review that found it. The `server/` half is pre-existing and
outside this change's scope fence; it is flagged on the PR rather than fixed
here. The fix had to be correct before flow control could ever turn that
dormancy off.

## The tmux facts the fix rests on

Verified against tmux 3.6a's `control.c`:

- `%pause %N` fires only on the not-paused → paused transition; `%continue
  %N` only on the reverse. Pausing an already-paused pane, or continuing one
  that isn't paused, emits nothing. The notifications are edge-triggered, so
  tracking gap state as a per-pane flag is sound and a *count* of anything
  is not — see "Why not a counter" below.
- `pause-after` is a client option (`CLIENT_CONTROL_PAUSEAFTER`), set with
  `refresh-client -f pause-after=N`. It is what would make tmux pause a pane
  on its own, as flow control against a slow reader; today it is unset, so the
  only `%pause` houston could see is one it asked for via
  `refresh-client -A '%N:pause'` — and the one call site that asks,
  `server/pane_ws.go`, still sends the bare unquoted form, which is the second
  dormancy reason above.
- **tmux's command-string lexer rejects a bare token that starts with `%` and
  contains `:`.** So the pane-state argument must be quoted. Measured in
  control mode with `-f /dev/null`, each row against a pane whose pause state
  made the outcome meaningful — `%pause`/`%continue` are edge-triggered, so a
  resume aimed at a running pane emits nothing and looks the same as a no-op:

  | command | result |
  |---|---|
  | `refresh-client -A '%0:pause'` | `%pause %0`, `%end` |
  | `refresh-client -A '%0:continue'` | `%continue %0`, `%end` |
  | `refresh-client -A "%0:continue"` | `%continue %0`, `%end` |
  | `refresh-client -A %0:pause` | `parse error: syntax error`, `%error` |
  | `refresh-client -A %0:continue` | `parse error: syntax error`, `%error` |
  | `refresh-client -A %0` | `%end`, **no** `%continue` |
  | `refresh-client -A x%0:continue` | `%end`, **no** `%continue` |

  Three outcome classes, not two: parses and resumes (either quoting), parses
  and does **not** resume, and does not parse. The last two classes are why a
  test that only checks for a successful `%end` proves nothing.
- **Each command produces exactly one block, and blocks arrive in the order
  tmux received the commands.** `%begin`'s third argument is the command
  number; its fourth is flags, documented "currently not used", so flags cannot
  classify a block. The number is a **server-global** counter — a fresh client
  saw `678835` — so houston can never predict the number tmux will assign its
  own write.
- **A real `tmux -CC attach-session` answers the attach command itself with one
  block, before houston writes anything**, and that block's `%begin` carries
  tmux's control-mode DCS introducer. Raw bytes off a pty:

  ```
  FIRST 120 BYTES: b'\x1bP1000p%begin 1789033623 812527 0\r\n%end 1789033623 812527 0\r\n%session-changed $761 ...'
  AFTER WRITE:     b'%begin 1789033624 812704 1\r\n%end 1789033624 812704 1\r\n'
  ```

  So exactly one block per connection is owed to no stdin write — and until this
  round `ParseControlLine` matched on a bare `%begin ` prefix, so **houston
  misparsed the first line of every connection it ever opened** and saw only that
  block's orphan `%end`. That is where `RunCommand`'s old "drain any stale
  response from a previous unsolicited `%begin`/`%end`" hack came from: the hack
  was the symptom, not a precaution. `ParseControlLine` now strips the
  introducer, which is a plain parsing fix independent of the attribution work.
- **A notification can appear inside a block**, despite the man page saying it
  never does, and **which side of `%end` it lands on depends on the pane**.
  Measured on 3.6a: against a pane actively producing output,
  `refresh-client -A '%0:pause'` emits `%pause %0` *between* its own `%begin`
  and `%end`; against an idle pane, eight of eight pauses emitted it *after*
  `%end`. tmux raises the notification when it next goes to write output for the
  pane, so a busy pane gets it inside the command's own processing.

  `readLoop` classifies **before** consulting `inBlock`, which is what makes
  both orderings work. That ordering is load-bearing, not an accident: a
  security review of this change proposed gating the notification cases on
  `!inBlock` for symmetry with `EventData`, and it was rejected on this
  evidence. The gate would drop `%pause` for exactly the busy panes the gap
  machinery exists to protect, silently — and no test would catch it, because a
  test driving an idle pane sees the after-`%end` ordering and stays green.
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
  `refresh-client -A '%N:continue'`, then marks dirty *every* subscriber
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
calls `cc.RunCommand` with the quoted resume from the timer's own goroutine, and
only re-takes the lock to mark subscribers dirty once that call returns —
successfully or not.

The first round of this record claimed that `RunCommand`'s `cmdMu` made the
resume's block impossible to misdeliver. **That was wrong**, and the review
demonstrated it: `cmdMu` holds only between *pending* callers, and four writers
reach stdin without ever taking it — `sendLiteral`, `sendControl`'s hex branch,
`SendSpecialKey`, and `attach`'s own `refresh-client -f ignore-size`. tmux
answers each with its own block, and the drain `RunCommand` performed before its
write could not catch a block that arrived after it. Measured with a concurrent
`SendSpecialKey` writer: 15169 of 187405 calls took another command's reply.
`paneWSReadLoop` runs for the whole socket lifetime, so a user pressing a key at
a frozen pane was exactly who triggered it. The flaw was pre-existing in
`RunCommand`; routing the resume through it is what made it load-bearing, since
a stolen `%end` turns a guaranteed `%error` into a believed success and
`markPaneDirty` then fires against a still-paused pane whose gap record is
already gone.

### How a block is attributed now

Binding is by **order**, with the command number as the begin/end consistency
check. It is not a command-number-keyed lookup, because that is not
constructible: the number is assigned server-side from a global counter and
houston never learns the one its own write received.

- Every write to `cc.stdin` enrolls a queue entry **inside the same `stdinMu`
  critical section that performs the write**, so enrollment order is write order
  by construction. A waiterless writer enrols a `nil` entry; skipping it would
  put the connection permanently one block out of step.
- The **first block of every connection is dropped unbound** — it answers the
  `attach-session` command the dialer ran. Its command number is still recorded,
  so its `%end` is a match and not a mismatch. The marker lives in `readLoop`'s
  own frame, which is entered once per connection: as a field on the client it
  would misalign every connection after the first.
- Each later `%begin` binds to the oldest entry still awaiting a block, and
  `%end`/`%error` is delivered only when its command number equals the open
  `%begin`'s. A `%begin` that finds an empty queue binds to nothing and is
  dropped; a block houston did not write owns no queue position.
- A failed write, or a terminator whose number does not match, **desynchronises**
  the connection: every enrolled waiter is released with an error and the
  connection is torn down so `supervise` re-dials. A failed write may have
  delivered a partial command line, so no cheaper recovery is sound — and the
  teardown is what keeps the state from being terminal. Without it a
  desynchronised but transport-healthy connection would leave `readLoop`
  reading, `connOK` true, `supervise` never re-dialling, and every later command
  failing for the life of the client while the paused pane stayed dark. The
  reconnect is the recovery: `attach` resets the queue and `markAllDirty`
  re-seeds every subscriber.

`cmdMu` is kept, because concurrent `RunCommand` callers are not what this
design needs to support, but it is no longer load-bearing for correctness: it
serializes callers and nothing more.

A resume **tmux refused** leaves the pane possibly still paused, and `%pause` is
edge-triggered, so tmux will never announce it again. Dropping the gap there
would rebuild the original defect with no bound at all: the subscriber re-seeds
once, acks, and waits forever on output tmux is still discarding. `expireGap`
therefore **re-opens the gap** on a refusal, arming a fresh deadline that
retries the resume, and logs it at `Warn` — the only signal a pane is stuck, so
it must be visible without `-debug`. The re-arm is gated on the pane still
having a subscriber: with nobody watching, the retry would loop for the life of
the client for no one.

**Only a refusal tmux actually sent re-arms.** The first round re-armed on any
error, including `control connection lost`, where `RunCommand` returned without
writing anything — so the lap cost a warning and no resume, and `markAllDirty`
discarded the gap at reconnect regardless. Measured over a 300 ms outage with a
stalled re-dial at a 20 ms deadline: 14 warnings, **0 resumes written**, gap
still armed. At the production 10 s deadline that is roughly 6 warnings a
minute of pure noise. `RunCommand` now returns distinguishable sentinels, and
`expireGap` re-arms and warns only on `errTmuxRefused`; nothing written, a
failed write, a dead connection, a closed client and a desynchronised stream all
return without re-arming and log at `Debug`. Reconnect is the recovery on those
paths, and it sweeps the gap itself.

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
second `refresh-client -A '%N:pause'` sent while the pane is already paused
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
caller sends `refresh-client -A %N:pause` through `RunCommand` (the bare form
`server/pane_ws.go` composes, which does not parse), and treat
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

- The refusal retry is unbounded. A live pane tmux keeps refusing to resume
  re-arms once per deadline, forever; nothing caps the attempts or backs the
  interval off. That is deliberate — giving up would leave the pane dark, which
  is the exact failure this change exists to prevent — and since a failed lap
  marks nobody, each lap costs one `refresh-client` and one `Warn`, not a
  re-seed. It ends on a successful resume, the last subscriber leaving, a
  reconnect (`markAllDirty` → `clearGapsLocked`), or `Close()`. A refusal is now
  the **only** path that laps at all: the first round's claim that this was the
  per-lap cost was wrong precisely because the connection-lost path also re-armed
  and cost a warning per deadline while writing nothing.
- **"Enroll before the write" is argued structurally, not witnessed by a test.**
  Moving the enrollment after the write leaves the whole suite green, including a
  concurrent-writer reproduction at `-race -count=20`. The reason is the fake, not
  the test's shape: `recordedConn`'s `Write` and `written()` share one mutex, so an
  in-test answerer cannot see the command line until the write has already
  returned, and it then has to format a block and have `readLoop` reach
  `claimCommand` inside the few nanoseconds before the writing goroutine appends.
  `-race` slows both sides equally, so the window does not widen. It is real
  against a PTY, where tmux answers on a separate fd. Catching it would need a
  probe writer that synchronously feeds a block from inside `Write` — a test whose
  only purpose is to kill that mutation — so the ordering rests on the code being
  read, not on a red test.
- **The scheme assumes exactly one block per stdin write and has no detector for
  a stray one.** A block tmux emits that no write produced, beyond the one
  baseline block per connection, would shift the queue for the life of that
  connection and still pass the command-number check, since its own `%begin` and
  `%end` agree. Nothing enforces the assumption; it rests on the documented
  protocol plus the fact that houston writes one command per line.
- **`paneID` is interpolated unquoted by the three send paths** (`sendLiteral`,
  `sendControl`'s hex branch, `SendSpecialKey`). That is safe today only because
  pane IDs reach them from tmux via `GetPaneID` and `SendSpecialKey`'s key name
  comes from a fixed table, not from a client. The shape check below narrows the
  notification path but does not cover these.
- **The pane-ID shape check narrows the command sink rather than removing it.**
  `parsePaneNotification` now takes the first field and requires `%` followed by
  digits, so a notification carrying `;` or a quote is not treated as a
  notification at all. What made that worth doing is that the previous guard was
  not the one the record implied: "only a conformant tmux server writes those
  lines" is not what protects it, because `readLoop` classifies before consulting
  `inBlock`, so a command response *body* line beginning `%pause ` would dispatch
  as a notification. What actually protects it is that houston issues only
  `refresh-client`, whose response bodies are empty.
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
