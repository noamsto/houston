package server

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/noamsto/houston/mode"
)

// strippedCrewEnv are inherited keys that would misdirect a crew command run on
// the operator's behalf: houston's own crew identity and pane, and CREW_BIN,
// which crew dash folds into every action argv it prints.
var strippedCrewEnv = []string{"CREW_BIN", "CREW_ID", "CREW_WORKER_ID", "TMUX_PANE", "DISPATCH_SPEC"}

const (
	crewStdoutCap = 16 << 10
	crewStderrCap = 4 << 10
)

type crewExec struct {
	Dir, CrewID string
	Args        []string // after the crew binary
}

type crewResult struct {
	Stdout, Stderr string
	ExitCode       int
	Err            error
}

// runCrew runs the crew binary at crewPath with no shell, so Args reach execve
// untouched.
func runCrew(ctx context.Context, crewPath string, x crewExec) crewResult {
	cmd := exec.CommandContext(ctx, crewPath, x.Args...) //nolint:gosec // callers build Args from validated run ids and enums; the binary is resolved once at startup
	cmd.Dir = x.Dir
	cmd.Env = crewEnv(os.Environ(), x.CrewID)
	// A timeout signals crew's whole process group: a gh left running under
	// `crew merge` would finish the merge with nobody to release the worker.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	// A grandchild that ignores SIGTERM can hold the captured pipes open past
	// the deadline.
	cmd.WaitDelay = 2 * time.Second
	stdout := &cappedBuffer{max: crewStdoutCap}
	stderr := &cappedBuffer{max: crewStderrCap}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()

	res := crewResult{
		Stdout: stdout.String(),
		Stderr: strings.TrimSpace(stderr.String()),
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		// A timeout signals the child, so Run reports "signal: terminated" —
		// an ExitError. Checking the context first keeps a timeout from being
		// misread as crew's own refusal.
		res.Err = context.DeadlineExceeded
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	default:
		res.Err = err
	}
	return res
}

// crewEnv is base without strippedCrewEnv, plus CREW_ID when crewID is set.
func crewEnv(base []string, crewID string) []string {
	env := make([]string, 0, len(base)+1)
	for _, kv := range base {
		key, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(strippedCrewEnv, key) {
			env = append(env, kv)
		}
	}
	if crewID != "" {
		env = append(env, "CREW_ID="+crewID)
	}
	return env
}

// resolveCrew finds crew on the tmux server's PATH ahead of houston's own, the
// search mode resolution used; "crew" when neither has it.
func resolveCrew(serverPath string) string {
	path := os.Getenv("PATH")
	if serverPath != "" {
		path = serverPath + ":" + path
	}
	if p, ok := mode.Find("crew", path); ok {
		return p
	}
	return "crew"
}
