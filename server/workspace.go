package server

import (
	"sort"
	"strings"

	"github.com/noamsto/houston/runs"
	"github.com/noamsto/houston/tmux"
)

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
	IssueID      string          `json:"issue_id,omitempty"`
	PRNumber     string          `json:"pr_number,omitempty"`
	PRState      string          `json:"pr_state,omitempty"`
	PRCheckState string          `json:"pr_check_state,omitempty"`
	Panes        []WorkspacePane `json:"panes"`
}

type WorkspacePane struct {
	ID      string `json:"id"` // "%307"
	Index   int    `json:"index"`
	Active  bool   `json:"active"`
	Command string `json:"command"`          // #{pane_current_command}; the plain-pane label (spec: never the parser)
	Agent   bool   `json:"agent"`            // a composed Run joined this pane id — see buildWorkspace
	RunID   string `json:"run_id,omitempty"` // set whenever Agent is true; the two can never disagree

	// Set only when a Run joined this pane, so the tab can say what is
	// running there without a second stream.
	AgentType string `json:"agent_type,omitempty"`
	State     string `json:"state,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Stale     bool   `json:"stale,omitempty"`
}

// windowGroup accumulates a matched window's own panes as buildWorkspace
// walks the pane list once.
type windowGroup struct {
	win   tmux.WindowOptions
	panes []tmux.PaneOptions
}

// buildWorkspace joins tmux's window/pane snapshot against the run registry's
// own snapshot to shape the tree the Workspace tab renders. A pane whose
// window was not listed is skipped, mirroring deltasFromTmux's own "the two
// queries raced a window closing" handling.
//
// mainCheckouts[root] == true means that @git_root is a repo's main
// checkout; false or an absent key means a linked worktree (including a root
// whose git lookup failed — see workspace_repo.go's repoClassifier for why
// "uncertain" reads as "not main"). buildWorkspace stays pure and
// fixture-testable: it never performs the git call itself, only buckets by
// the answers it's handed.
func buildWorkspace(wins []tmux.WindowOptions, panes []tmux.PaneOptions, snap []runs.Run, mainCheckouts map[string]bool) Workspace {
	paneToRun := make(map[string]runs.Run, len(snap))
	for _, r := range snap {
		if r.Tmux != nil {
			paneToRun[r.Tmux.PaneID] = r
		}
	}

	byTarget := make(map[string]tmux.WindowOptions, len(wins))
	for _, w := range wins {
		byTarget[w.Target()] = w
	}

	groups := make(map[string]*windowGroup)
	for _, p := range panes {
		w, ok := byTarget[p.Target]
		if !ok {
			continue
		}
		g, exists := groups[p.Target]
		if !exists {
			g = &windowGroup{win: w}
			groups[p.Target] = g
		}
		g.panes = append(g.panes, p)
	}

	sessions := make(map[string][]*windowGroup)
	for _, g := range groups {
		sessions[g.win.Session] = append(sessions[g.win.Session], g)
	}

	sessionNames := make([]string, 0, len(sessions))
	for name := range sessions {
		sessionNames = append(sessionNames, name)
	}
	sort.Strings(sessionNames)

	ws := Workspace{Sessions: make([]WorkspaceSession, 0, len(sessionNames))}
	for _, name := range sessionNames {
		grps := sessions[name]
		sort.Slice(grps, func(i, j int) bool { return grps[i].win.Window < grps[j].win.Window })

		session := WorkspaceSession{Name: name, WindowCount: len(grps)}
		for _, g := range grps {
			sort.Slice(g.panes, func(i, j int) bool { return g.panes[i].Index < g.panes[j].Index })

			wp := make([]WorkspacePane, 0, len(g.panes))
			for _, p := range g.panes {
				pane := WorkspacePane{
					ID:      p.PaneID,
					Index:   p.Index,
					Active:  p.Active,
					Command: p.Command,
				}
				if r, ok := paneToRun[p.PaneID]; ok && r.ID != "" {
					pane.Agent = true
					pane.RunID = r.ID
					pane.AgentType = r.Agent
					pane.State = string(r.State)
					pane.UpdatedAt = r.UpdatedAt
					pane.Detail = runDetail(r)
					pane.Stale = r.Stale
				}
				wp = append(wp, pane)
			}

			win := WorkspaceWindow{
				Index:        g.win.Window,
				Name:         g.win.Name,
				Active:       g.win.Active,
				Branch:       g.win.Branch,
				Task:         g.win.Task,
				CrewCodename: g.win.CrewName,
				IssueID:      optionValue(g.win.IssueID),
				PRNumber:     optionValue(g.win.PRNumber),
				PRState:      optionValue(g.win.PRState),
				PRCheckState: optionValue(g.win.PRCheckState),
				Panes:        wp,
			}

			switch {
			case g.win.GitRoot == "":
				session.Other = append(session.Other, win)
			case mainCheckouts[g.win.GitRoot]:
				session.MainCheckout = append(session.MainCheckout, win)
			default:
				session.Worktrees = append(session.Worktrees, win)
			}
		}

		ws.Sessions = append(ws.Sessions, session)
	}

	return ws
}

// runDetail is the one line saying what a run is doing right now. It mirrors
// ui/src/fleet/format.ts's subtitle so a workspace row and a run card never
// disagree. Whitespace is collapsed because message and task are free text.
func runDetail(r runs.Run) string {
	return strings.Join(strings.Fields(runDetailRaw(r)), " ")
}

func runDetailRaw(r runs.Run) string {
	a := r.Activity
	if r.State == runs.StateBlocked {
		if a.Message != "" {
			return a.Message
		}
		return "waiting on you"
	}
	switch {
	case a.Tool != "" && a.Hint != "":
		return a.Tool + " · " + a.Hint
	case a.Tool != "":
		return a.Tool
	case a.Task != "":
		return a.Task
	case r.PR != nil:
		if cs := optionValue(r.PR.CheckState); cs != "" {
			return "PR #" + r.PR.Number + " · " + cs
		}
		return "PR #" + r.PR.Number
	}
	return string(r.State)
}

// optionValue drops lazytmux's "none" sentinel, which it writes into a window
// option when there is nothing to record (no PR yet, no checks).
func optionValue(v string) string {
	if v == "none" {
		return ""
	}
	return v
}
