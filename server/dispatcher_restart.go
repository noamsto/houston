package server

import (
	"path/filepath"

	"github.com/noamsto/houston/runs"
)

// launcherSettings are the engine, model and effort a running dispatcher was
// launched with. An empty field means the launcher was not given that flag.
type launcherSettings struct {
	Engine string
	Model  string
	Effort string
}

// parseLauncherArgv reads the settings back from a dispatcher process's argv.
// It mirrors the launcher's own flag loop: the launcher parses flags anywhere
// after its name, so a task text between flags must be skipped, not treated as
// the end of the flags. The last occurrence of a flag wins.
func parseLauncherArgv(argv []string) launcherSettings {
	var s launcherSettings
	start := -1
	for i, a := range argv {
		if filepath.Base(a) == "dispatcher" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return s
	}
	for i := start; i < len(argv); i++ {
		switch argv[i] {
		case "--agent", "--model", "--effort", "--autocompact":
			if i+1 >= len(argv) {
				return s
			}
			i++
			switch argv[i-1] {
			case "--agent":
				s.Engine = argv[i]
			case "--model":
				s.Model = argv[i]
			case "--effort":
				s.Effort = argv[i]
			}
		}
	}
	return s
}

// restartRequest is the body of a dispatcher restart. A nil field inherits the
// running dispatcher's setting; a non-nil empty string clears it.
type restartRequest struct {
	Engine *string `json:"engine"`
	Model  *string `json:"model"`
	Effort *string `json:"effort"`
	Force  bool    `json:"force"`
}

// restartSettings merges the request over the inherited launcher settings.
// Models are per-engine, so an engine change does not inherit the model.
// runAgent stands in for an engine the launcher was not told explicitly.
func restartSettings(req restartRequest, inh launcherSettings, runAgent string) dispatcherRequest {
	base := inh.Engine
	if base == "" {
		base = runAgent
	}
	engine := base
	if req.Engine != nil {
		engine = *req.Engine
	}
	engineChanged := engine != base

	model := inh.Model
	switch {
	case req.Model != nil:
		model = *req.Model
	case engineChanged:
		model = ""
	}
	effort := inh.Effort
	if req.Effort != nil {
		effort = *req.Effort
	}
	return dispatcherRequest{Engine: engine, Model: model, Effort: effort}
}

// restartBusy reports whether restarting the run would cut off work in
// progress. A turn that ended on a question is the agent waiting, not
// working; any other blocked run has a dialog or AskUserQuestion on screen
// that a restart would discard.
func restartBusy(run runs.Run) bool {
	switch run.State {
	case runs.StateThinking, runs.StateRunning, runs.StateCompacting:
		return true
	case runs.StateBlocked:
		return run.Question == nil || run.Question.Kind != runs.QuestionTurn
	case runs.StateReview, runs.StateDone, runs.StateFailed, runs.StateIdle:
		return false
	}
	return false
}
