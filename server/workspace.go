package server

import (
	"sort"

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
	Panes        []WorkspacePane `json:"panes"`
}

type WorkspacePane struct {
	ID      string `json:"id"` // "%307"
	Index   int    `json:"index"`
	Active  bool   `json:"active"`
	Command string `json:"command"`          // #{pane_current_command}; the plain-pane label (spec: never the parser)
	Agent   bool   `json:"agent"`            // a composed Run joined this pane id — see buildWorkspace
	RunID   string `json:"run_id,omitempty"` // set whenever Agent is true; the two can never disagree
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
	paneToRun := make(map[string]string, len(snap))
	for _, r := range snap {
		if r.Tmux != nil {
			paneToRun[r.Tmux.PaneID] = r.ID
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
				runID := paneToRun[p.PaneID]
				wp = append(wp, WorkspacePane{
					ID:      p.PaneID,
					Index:   p.Index,
					Active:  p.Active,
					Command: p.Command,
					Agent:   runID != "",
					RunID:   runID,
				})
			}

			win := WorkspaceWindow{
				Index:        g.win.Window,
				Name:         g.win.Name,
				Active:       g.win.Active,
				Branch:       g.win.Branch,
				Task:         g.win.Task,
				CrewCodename: g.win.CrewName,
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
