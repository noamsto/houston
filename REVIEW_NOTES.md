# Review notes — #121

`review_mode: full` (go-reviewer promoted to opus per the cross-component
correctness-change rule in `EVIDENCE_REVIEW.md`; security-reviewer at normal
rung; targeted test-runner). `recurrence_escalation: unused`.

| invariant/family | finding | observed head | fix commit | proof | disposition | rounds used |
| --- | --- | --- | --- | --- | --- | --- |
| gone-pane detection (tmux/client.go ResolvePane) | `#{pid}` addition broke whole-line-blank gone detection (found by subagent during test-writing, pre-review) | 7f6f69b (pre-review commit) | 7f6f69b (fixed pre-review, same commit as the diff under review) | `TestResolvePaneVanishedIntegration` red→green against real tmux 3.6a | fixed | 0 |
| doc accuracy (runs/run.go TmuxRef.Server comment) | comment said "Session/Window/PaneID above are tagged but this is not" — misleading, Server *is* tagged (`json:"-"`) | 7f6f69b | (this round, pre-push) | reworded, mechanical | fixed | 1 |
| test coverage (server/runs_terminal_test.go) | missing "same server ⇒ allowed" and "known run / unknown pane server ⇒ allowed" cases; dead `if rec.Code == http.StatusConflict` assertion that could never fire; input-route success case never asserted the key was actually sent | 7f6f69b | (this round, pre-push) | `TestRunTerminalServerCheckAllowsResolution` (3 cases × 2 routes, 6 subtests, all green) replaces `TestRunTerminalServerUnknownAllowsResolution` | fixed | 1 |
| doc accuracy (CLAUDE.md) | WebSocket Protocol / Foreign panes sections didn't mention the new send-time server check or its 409 | 7f6f69b | (this round, pre-push) | reworded, mechanical | fixed | 1 |
| WS connection lifetime vs. server identity | an already-open WS terminal doesn't re-check server identity after tmux reconnects to a same-named session that reused the pane id (go-reviewer HIGH, verified by direct code read of servePane/ControlClient.supervise) | 7f6f69b | — | — | deferred (#138) | 0 |
| registry.go mergeInto Tmux field | whole-pointer `Tmux` swap can replace a known `Server` with an unknown one from a lower-precedence (hooks) layer (go-reviewer MEDIUM; security-reviewer independently traced and narrowed to a self-healing transitional case, not a live bypass) | 7f6f69b | — | — | deferred (#139) | 0 |
| legacy /api/pane/:target/send+/ws | no pane-identity protection at all (pre-existing, out of #121's scope; flagged independently by both reviewers) | 7f6f69b | — | — | deferred (#140) | 0 |

## Evidence

- Consumer map for `TmuxRef.Server`/`tmux.Pane.Server`: producers `runs/hooksource.go` (from `hub.SessionView.TmuxServer`), `runs/tmuxsource.go` (from `tmux.PaneOptions.ServerPID`); consumer `server/runs_terminal.go`'s `runPane`; zero JSON/API exposure (`json:"-"` on both, verified empirically by security-reviewer via a throwaway marshal).
- Behavioral regression test: `server/runs_terminal_test.go`'s `TestRunTerminalResolution/*/server_mismatch` (409, `ResolvePane` called once, nothing sent, both routes) and `TestRunTerminalServerCheckAllowsResolution` (all three non-refusal combinations, both routes).
- `go build ./...`, `go vet ./...`, `go test ./...`, `golangci-lint run ./...` all clean at head. `npm run build` (tsc + vite) and `npm test` (vitest, 518 tests) clean.
- Both go-reviewer (opus, escalated) and security-reviewer independently traced the exact race in the issue (tmux restart between HookSource polls, pane id reuse) and confirmed the mismatch check correctly detects and refuses it — no attacker-controlled bypass of the "unknown ⇒ false" default was found.
