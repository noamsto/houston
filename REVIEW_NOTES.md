# Review notes — #152

`review_mode: full` (go-reviewer + agent-docs-reviewer + security-reviewer,
routed from `roster.json`; the pi reviewer role pane is the review gate).
`recurrence_escalation: unused`.

| invariant/family | finding | observed head | fix commit | proof | disposition | rounds used |
| --- | --- | --- | --- | --- | --- | --- |
| terminal-join identity (runs/crewjoin.go `finishedPaneState`/`terminalJoinGrace`) | HIGH: an *idle* new session (SessionStart writes `idle` with a fresh epoch) inside the grace window joins the stale terminal record — the #151 test shape | 3dc43c4 | (this round) | `TestResolvePaneTerminalStateIdleNewOccupantWithinGraceJoins` pins the accepted residual; processing-within-grace and beyond-grace both refuse; code comment + CLAUDE.md state the residual | accepted + documented (follow-up filed with the PR) | 1 |
| doc accuracy (CLAUDE.md, runs/crewjoin.go) | MEDIUM: prose named `failed`, but `@claude_status` has no `failed` word — StopFailure writes `error`, which maps to internal `StateFailed` | 3dc43c4 | (this round) | wording changed to `done`/`idle`/`error` (pane words) | fixed | 1 |
| test oracle (runs/crewsource_test.go) | MEDIUM: beyond-grace fixture derived from `terminalJoinGrace`, so scaling the constant would keep the test green | 3dc43c4 | (this round) | fixed epoch `3700` in `TestResolvePaneTerminalStateNewOccupantDoesNotJoin` | fixed | 1 |

## Evidence

- Consumer map: `paneOptionsFormat`/`ParsePaneOptions` consumers are the
  `ListPaneOptions` callers `runs/tmuxsource.go`, `runs/crewsource.go`,
  `runs/hooksource.go`. `AgentScreen` is additive; only `runs/crewjoin.go`
  reads it. `server/` untouched.
- Lazytmux epoch semantics: `scripts/claude-status-update.sh` — every state
  writes `ts=$_now` except `idle`, which preserves the prior ts; `mark-seen`
  (window switch / viewing the pane) does not rewrite `@claude_status`.
  `agentdetect/statefile/statefile.go` — `@agent_screen = "<state> <epoch>
  [name=count …]"`, vocabulary processing/idle/waiting.
- Fast gate at the fix head: `go build ./...`, `go vet ./...`,
  `go test -race ./...`, `pre-commit run -a` all pass.