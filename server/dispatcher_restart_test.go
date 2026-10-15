package server

import (
	"os"
	"runtime"
	"testing"

	"github.com/noamsto/houston/runs"
)

func TestParseLauncherArgv(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want launcherSettings
	}{
		{"houston shaped", []string{"/nix/store/x-bash/bin/bash", "/nix/store/y-dispatcher/bin/dispatcher", "--agent", "claude", "--model", "opus", "--effort", "high"},
			launcherSettings{"claude", "opus", "high"}},
		{"hand launched", []string{"bash", "/usr/bin/dispatcher"}, launcherSettings{}},
		{"task text between flags", []string{"bash", "/usr/bin/dispatcher", "--agent", "pi", "fix the flaky test", "--model", "m1"},
			launcherSettings{Engine: "pi", Model: "m1"}},
		{"trailing flag without value", []string{"bash", "/usr/bin/dispatcher", "--agent", "claude", "--model"},
			launcherSettings{Engine: "claude"}},
		{"new before agent", []string{"bash", "/usr/bin/dispatcher", "--new", "--agent", "pi"}, launcherSettings{Engine: "pi"}},
		{"last occurrence wins", []string{"bash", "/usr/bin/dispatcher", "--model", "a", "--model", "b"}, launcherSettings{Model: "b"}},
		{"no dispatcher element", []string{"bash", "script.sh", "--agent", "claude"}, launcherSettings{}},
		{"autocompact consumes its value", []string{"bash", "/usr/bin/dispatcher", "--autocompact", "5000", "--effort", "low"},
			launcherSettings{Effort: "low"}},
		{"autocompact value is not a flag", []string{"bash", "/usr/bin/dispatcher", "--autocompact", "--model", "x"}, launcherSettings{}},
		{"dispatcher-old does not match", []string{"bash", "/x/dispatcher-old", "--agent", "claude"}, launcherSettings{}},
		{"flag before dispatcher ignored", []string{"--agent", "pi", "dispatcher", "--model", "m"}, launcherSettings{Model: "m"}},
		{"empty", nil, launcherSettings{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseLauncherArgv(tt.argv); got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestReadLauncherArgv(t *testing.T) {
	argv, ok := readLauncherArgv(os.Getpid())
	if runtime.GOOS != "linux" {
		if ok || argv != nil {
			t.Fatalf("got %v, %v on a non-linux host", argv, ok)
		}
		return
	}
	if !ok || len(argv) == 0 || argv[0] == "" {
		t.Fatalf("own argv: got %v, %v", argv, ok)
	}
	if _, ok := readLauncherArgv(1 << 30); ok {
		t.Fatal("a pid that does not exist read as ok")
	}
}

func TestRestartSettings(t *testing.T) {
	s := func(v string) *string { return &v }
	inh := launcherSettings{Engine: "claude", Model: "opus", Effort: "high"}
	tests := []struct {
		name     string
		req      restartRequest
		inh      launcherSettings
		runAgent string
		want     dispatcherRequest
	}{
		{"inherit all", restartRequest{}, inh, "claude", dispatcherRequest{Engine: "claude", Model: "opus", Effort: "high"}},
		{"model override", restartRequest{Model: s("sonnet")}, inh, "claude", dispatcherRequest{Engine: "claude", Model: "sonnet", Effort: "high"}},
		{"effort override", restartRequest{Effort: s("low")}, inh, "claude", dispatcherRequest{Engine: "claude", Model: "opus", Effort: "low"}},
		{"engine change drops model", restartRequest{Engine: s("pi")}, inh, "claude", dispatcherRequest{Engine: "pi", Model: "", Effort: "high"}},
		{"engine change with model", restartRequest{Engine: s("pi"), Model: s("m")}, inh, "claude", dispatcherRequest{Engine: "pi", Model: "m", Effort: "high"}},
		{"engine equal to inherited keeps model", restartRequest{Engine: s("claude")}, inh, "claude", dispatcherRequest{Engine: "claude", Model: "opus", Effort: "high"}},
		{"model cleared", restartRequest{Model: s("")}, inh, "claude", dispatcherRequest{Engine: "claude", Model: "", Effort: "high"}},
		{"effort cleared", restartRequest{Effort: s("")}, inh, "claude", dispatcherRequest{Engine: "claude", Model: "opus", Effort: ""}},
		{"no inherited engine uses run agent", restartRequest{}, launcherSettings{}, "pi", dispatcherRequest{Engine: "pi"}},
		{"engine equal to run agent keeps model", restartRequest{Engine: s("pi")}, launcherSettings{Model: "m"}, "pi", dispatcherRequest{Engine: "pi", Model: "m"}},
		{"engine differs from run agent drops model", restartRequest{Engine: s("claude")}, launcherSettings{Model: "m"}, "pi", dispatcherRequest{Engine: "claude"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := restartSettings(tt.req, tt.inh, tt.runAgent)
			if got.Engine != tt.want.Engine || got.Model != tt.want.Model || got.Effort != tt.want.Effort {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRestartBusy(t *testing.T) {
	q := func(kind string) *runs.Question { return &runs.Question{Text: "?", Kind: kind} }
	tests := []struct {
		name string
		run  runs.Run
		want bool
	}{
		{"thinking", runs.Run{State: runs.StateThinking}, true},
		{"running", runs.Run{State: runs.StateRunning}, true},
		{"compacting", runs.Run{State: runs.StateCompacting}, true},
		{"idle", runs.Run{State: runs.StateIdle}, false},
		{"review", runs.Run{State: runs.StateReview}, false},
		{"done", runs.Run{State: runs.StateDone}, false},
		{"failed", runs.Run{State: runs.StateFailed}, false},
		{"blocked without question", runs.Run{State: runs.StateBlocked}, true},
		{"blocked on turn question", runs.Run{State: runs.StateBlocked, Question: q(runs.QuestionTurn)}, false},
		{"blocked on dialog", runs.Run{State: runs.StateBlocked, Question: q(runs.QuestionDialog)}, true},
		{"blocked on ask", runs.Run{State: runs.StateBlocked, Question: q(runs.QuestionAsk)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := restartBusy(tt.run); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
