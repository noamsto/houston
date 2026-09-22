# Review notes — #98

Round 1 batch: go-reviewer (promoted to opus, cross-component contract change),
security-reviewer (conservative include: input routing), targeted test-runner.
Both reviewers returned **Block**.

| invariant/family | finding | observed head | fix commit | proof | disposition | rounds |
| --- | --- | --- | --- | --- | --- | --- |
| distrust guard must not fail open | go HIGH: a failed `ServerIdentity()` zeroed the identity for every session, so a foreign pane re-merged with the other agent's tmux layer on the next tick (reviewer reproduced) | c01d109 | pending | carry the previous identity forward; new test | open | 1 |
| regression test pins the invariant | go HIGH: the hook test changed pane *and* server, so either half of `stale` alone kept it green (mutation-verified); the literal #98 case (same server, new pane) was untested | c01d109 | pending | table-driven over the four env cases | open | 1 |
| distrust tests exercise the collision | go HIGH: single-session fixtures meant `normalize` could be deleted from `current()` or the `<-sub` branch with `./runs` green (mutation-verified) | c01d109 | pending | HookSource-level test with a foreign *and* a live session on `%20` | open | 1 |
| ended session keeps its pane | security HIGH: `SessionEnd` never clears the coordinates, so an ended session's run keeps merging with that pane's live tmux layer and keeps terminal/reply caps if the pane is reused by a non-hook occupant | c01d109 | — | pre-existing (not introduced here); fixing it changes how an ended session's card composes, which is a design decision beyond this fix | deferred (issue) | 1 |
| identity failure visibility | security MEDIUM: the degraded guard was announced at `slog.Debug` only | c01d109 | pending | raised to `slog.Warn` | open | 1 |
| `$TMUX` parsing | go LOW: `fields[1]` breaks on a socket path containing a comma | c01d109 | pending | index the pid from the right | open | 1 |
| `PID` recorded outside tmux | go MEDIUM: `PID` now only set inside the refresh branch, so a non-tmux session records `pid: 0` forever | c01d109 | pending | set it when still zero | open | 1 |
| docs match behaviour | go MEDIUM: CLAUDE.md's ghost-run paragraph predates the distrust rule; `tmux_server` undocumented | c01d109 | pending | CLAUDE.md "Foreign panes" bullet | open | 1 |

Refuted / not acted on:

- go MEDIUM "foreign collapsed into gone": deliberate and spec-accepted — a
  distrusted pane takes the vanished-pane path, and the existing revive rule
  keeps a session alive (without a `Tmux` ref) as soon as newer hook activity
  arrives.
- go MEDIUM "`current()` returns raw views": a style contract, not a defect;
  the new collision test pins every `normalize` call site.
- go MEDIUM "retry has no backoff": the old `TmuxSession == ""` guard retried
  on every event too, so this is not a regression.

Round 2 (targeted re-review of ff5eafc, opus): **Block**. Round 1's three
findings are confirmed closed (all five mutants die on a named assertion), but
the same invariant is violated again by a narrower path:

| invariant/family | finding | observed head | fix commit | proof | disposition | rounds |
| --- | --- | --- | --- | --- | --- | --- |
| distrust guard must not grant a foreign pane | round 2 HIGH: if the tmux server restarts *and* the identity query fails on that tick, the carried-forward identity matches the stale state file and the freshly minted `%N` is trusted — wrong TmuxRef and caps for one poll tick (reviewer reproduced); the code comment claims `paneGone` catches it, which is false for a re-minted id | ff5eafc | pending | escalated assessment (see below) | open | 2 (cap reached) |
| poller identity threading is untested | round 2 HIGH: deleting `prev = ps` from `pollPanes`, or seeding it with a zero `paneSet`, leaves `./runs ./hook` green | ff5eafc | pending | HookSource-level test asserting a distrusted run stays distrusted across an identity failure | open | 2 (cap reached) |
| log volume | round 2 MEDIUM: `slog.Warn` every 5 s while the identity query keeps failing | ff5eafc | pending | log the transition, not the repeat | open | 2 |
| poller closure captures `panes` by reference | round 2 MEDIUM: safe today by happens-before; a second write in `Run` would make it a real race | ff5eafc | pending | pass the seed as a parameter | open | 2 |

Refuted in round 2: "`PID` write is out of scope" — the pre-change code set
`PID` on every session unconditionally; the `else if` only restores that, it
does not add a field or a consumer.

`recurrence_escalation: used` — same invariant violated after an attempted fix,
so one fresh deep assessment was launched for a bounded replacement approach
(leading candidate: fold `#{pid}`/`#{start_time}` into `paneOptionsFormat` so
the identity is atomic with the listing and the carry-forward disappears). The
two review→fix rounds are spent, so its proposal is handed off, not
implemented, unless the dispatcher authorises the extra round.

## Escalated assessment (recurrence, opus, review-only)

Recommendation: **replace the carry-forward on this branch** — append `#{pid}`
and `#{start_time}` to `paneOptionsFormat` so one `list-panes -a -F` yields the
pane set *and* the identity of the server that enumerated it (verified on tmux
3.6a: the vars expand per line and match `display-message`). Then delete
`Client.ServerIdentity`, the `prev` threading and the carry-forward: the
"listing OK / identity from another moment" state becomes unrepresentable, so
both round 1 and round 2 fail the same way — unwritable rather than
re-answered. It is a net deletion (~2 production + 2 test files), with a
lenient `start_time` parse so an unexpandable field degrades to the pre-guard
behaviour instead of blanking every run, and `paneForeign` / "unknown identity
⇒ trust" unchanged.

Not closed by it, and explicitly a separate follow-up: the ≤5 s *staleness*
window (a restart landing just after a successful listing), which pre-dates
this change. Closing the keystroke half of that needs a point-of-use check —
`TmuxRef.Server` compared against `#{pid}` in `ResolvePane`, refused in
`server/runs_terminal.go` before the WS upgrade.

Ship judgement: do **not** ship the carry-forward as the final answer, because
its failure mode is the exact invariant the issue exists for and the correct
replacement is smaller than the code it removes.

**Status: blocked on the dispatcher.** The two review→fix rounds are spent, so
per the evidence contract this proposal is handed off, not implemented.
