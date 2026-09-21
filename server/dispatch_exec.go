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
)

// dispatchExec is one dispatch invocation, fully resolved by the handler. The
// runner adds nothing and decides nothing.
type dispatchExec struct {
	Dir  string   // matched repo path
	Argv []string // Argv[0] == "dispatch"
	Spec string   // task body; "" means no DISPATCH_SPEC is set
}

type dispatchResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error // exec start failure → 502; context.DeadlineExceeded → 504
}

type dispatchRunner func(ctx context.Context, x dispatchExec) dispatchResult

const dispatchOutputCap = 64 << 10

// dispatchGrace is how long dispatch gets after SIGTERM to run its trap and
// drop its branch lock. A var so tests can shorten it.
var dispatchGrace = 5 * time.Second

// strippedDispatchEnv are inherited keys that would misdirect dispatch:
// houston's own pane is not a dispatcher pane, and a stale crew identity or
// spec would leak into the new worker.
var strippedDispatchEnv = []string{"DISPATCH_SPEC", "CREW_ID", "CREW_WORKER_ID", "TMUX_PANE"}

// cappedBuffer keeps the first max bytes and silently drops the rest. Write
// always reports the full length: a short write would make the pipe copier
// stop reading, and the child would die of EPIPE mid-run.
type cappedBuffer struct {
	buf []byte
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.max - len(b.buf); room > 0 {
		b.buf = append(b.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string { return string(b.buf) }

func execDispatch(ctx context.Context, x dispatchExec) dispatchResult {
	env := dispatchEnv()
	if x.Spec != "" {
		path, err := writeDispatchSpec(x.Spec)
		if err != nil {
			return dispatchResult{Err: err}
		}
		defer func() { _ = os.Remove(path) }()
		env = append(env, "DISPATCH_SPEC="+path)
	}

	cmd := exec.CommandContext(ctx, x.Argv[0], x.Argv[1:]...)
	cmd.Dir = x.Dir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// SIGTERM to the whole group, not a SIGKILL to the leader: dispatch's
	// TERM trap removes its per-branch lock, which it never reclaims itself.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = dispatchGrace
	stdout := &cappedBuffer{max: dispatchOutputCap}
	stderr := &cappedBuffer{max: dispatchOutputCap}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()

	res := dispatchResult{
		Stdout: strings.TrimSpace(stdout.String()),
		Stderr: strings.TrimSpace(stderr.String()),
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		// WaitDelay only kills the leader; a grandchild that ignored SIGTERM
		// is still in the group.
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		res.Err = context.DeadlineExceeded
	case errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil:
		// dispatch itself exited fine but a background child it started
		// (without redirecting its output) still held stdout/stderr open past
		// WaitDelay — that's the child's doing, not a dispatch failure.
		res.ExitCode = cmd.ProcessState.ExitCode()
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	default:
		res.Err = err
	}
	return res
}

func dispatchEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(strippedDispatchEnv, key) {
			env = append(env, kv)
		}
	}
	return env
}

func writeDispatchSpec(spec string) (string, error) {
	f, err := os.CreateTemp("", "houston-dispatch-*.md")
	if err != nil {
		return "", err
	}
	_, werr := f.WriteString(spec)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
