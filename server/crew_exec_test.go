package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeCrewScript(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "crew")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil { //nolint:gosec // test script must be executable
		t.Fatal(err)
	}
	return p
}

func TestCrewEnvStripsIdentityAndSetsCrewID(t *testing.T) {
	base := []string{"A=1", "CREW_BIN=x", "CREW_ID=old", "CREW_WORKER_ID=w", "TMUX_PANE=%1", "DISPATCH_SPEC=s", "B=2"}

	got := crewEnv(base, "new")
	want := []string{"A=1", "B=2", "CREW_ID=new"}
	if !slices.Equal(got, want) {
		t.Fatalf("crewEnv = %v, want %v", got, want)
	}

	got = crewEnv(base, "")
	want = []string{"A=1", "B=2"}
	if !slices.Equal(got, want) {
		t.Fatalf("crewEnv without id = %v, want %v", got, want)
	}
}

func TestResolveCrew(t *testing.T) {
	serverDir, ownDir, empty := t.TempDir(), t.TempDir(), t.TempDir()

	t.Setenv("PATH", empty)
	if got := resolveCrew(""); got != "crew" {
		t.Errorf("none found: got %q, want crew", got)
	}

	ownCrew := writeCrewScript(t, ownDir, "exit 0")
	t.Setenv("PATH", ownDir)
	if got := resolveCrew(""); got != ownCrew {
		t.Errorf("own PATH only: got %q, want %q", got, ownCrew)
	}

	serverCrew := writeCrewScript(t, serverDir, "exit 0")
	if got := resolveCrew(serverDir); got != serverCrew {
		t.Errorf("both: got %q, want server's %q", got, serverCrew)
	}

	t.Setenv("PATH", empty)
	if got := resolveCrew(serverDir); got != serverCrew {
		t.Errorf("server only: got %q, want %q", got, serverCrew)
	}
}

func TestRunCrewExec(t *testing.T) {
	dir := t.TempDir()
	script := writeCrewScript(t, dir, `
echo "args:$*"
echo "id:$CREW_ID"
echo "bin:$CREW_BIN" >&2
case "$1" in
exit3) exit 3;;
big) head -c 20480 /dev/zero | tr '\0' x;;
slow) exec sleep 5;;
esac`)
	t.Setenv("CREW_BIN", "leaked")
	ctx := context.Background()

	res := runCrew(ctx, script, crewExec{Dir: dir, CrewID: "c1", Args: []string{"hello", "--", "-x"}})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("res = %+v", res)
	}
	if !strings.Contains(res.Stdout, "args:hello -- -x") || !strings.Contains(res.Stdout, "id:c1") {
		t.Errorf("Stdout = %q", res.Stdout)
	}
	if res.Stderr != "bin:" {
		t.Errorf("Stderr = %q, want CREW_BIN stripped and output trimmed", res.Stderr)
	}

	res = runCrew(ctx, script, crewExec{Dir: dir, Args: []string{"exit3"}})
	if res.ExitCode != 3 || res.Err != nil {
		t.Errorf("exit res = %+v, want ExitCode 3", res)
	}

	res = runCrew(ctx, script, crewExec{Dir: dir, Args: []string{"big"}})
	if len(res.Stdout) != crewStdoutCap {
		t.Errorf("len(Stdout) = %d, want cap %d", len(res.Stdout), crewStdoutCap)
	}

	tctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	res = runCrew(tctx, script, crewExec{Dir: dir, Args: []string{"slow"}})
	if !errors.Is(res.Err, context.DeadlineExceeded) || res.ExitCode != 0 {
		t.Errorf("timeout res = %+v", res)
	}
}

func TestRunCrewExecTimeoutWithGrandchildHoldingPipes(t *testing.T) {
	dir := t.TempDir()
	script := writeCrewScript(t, dir, "sleep 5 &\nwait")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	res := runCrew(ctx, script, crewExec{Dir: dir})
	if !errors.Is(res.Err, context.DeadlineExceeded) || res.ExitCode != 0 {
		t.Errorf("res = %+v, want DeadlineExceeded", res)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v, want under 3s", d)
	}
}

// A timeout reaches crew's children too: a gh left running would finish the
// merge with nobody to release the worker.
func TestRunCrewExecTimeoutKillsGrandchild(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pids")
	script := writeCrewScript(t, dir, "sleep 30 &\necho \"$$ $!\" > "+pidFile+"\nwait")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	res := runCrew(ctx, script, crewExec{Dir: dir})
	if !errors.Is(res.Err, context.DeadlineExceeded) {
		t.Errorf("res = %+v, want DeadlineExceeded", res)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(b))
	if len(fields) != 2 {
		t.Fatalf("pid file %q, want script and grandchild pids", b)
	}
	for _, f := range fields {
		pid, err := strconv.Atoi(f)
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for syscall.Kill(pid, 0) != syscall.ESRCH {
			if time.Now().After(deadline) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Fatalf("pid %d still alive 3s after the timeout", pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}
