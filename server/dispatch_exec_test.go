package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// installFakeDispatch writes body as an executable `dispatch` script and puts
// it first on PATH. It also plants the keys the runner must strip, so every
// test proves they never reach the child.
func installFakeDispatch(t *testing.T, body string) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}
	bin := t.TempDir()
	script := "#!" + bash + "\n" + body
	if err := os.WriteFile(filepath.Join(bin, "dispatch"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DISPATCH_SPEC", "/inherited/spec.md")
	t.Setenv("CREW_ID", "inherited-crew")
	t.Setenv("CREW_WORKER_ID", "inherited-worker")
	t.Setenv("TMUX_PANE", "%99")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func runDispatch(t *testing.T, timeout time.Duration, x dispatchExec) dispatchResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return execDispatch(ctx, x)
}

func TestExecDispatchInvocation(t *testing.T) {
	out := t.TempDir()
	installFakeDispatch(t, `out='`+out+`'
printf '%s\n' "$@" > "$out/argv"
pwd -P > "$out/cwd"
env > "$out/env"
f=$DISPATCH_SPEC
{ stat -c %a "$f" 2>/dev/null || stat -f %Lp "$f"; } > "$out/mode"
cp "$f" "$out/spec"
printf '%s' "$f" > "$out/specpath"
echo "worker_id: worker:feat/1-x#s1"
`)
	dir := t.TempDir()
	argv := []string{"dispatch", "standard", "sonnet", "--crew-id", "c1", `add "quoted" widget $(id) now`}
	spec := "# Task\n\ndo the thing\n"

	res := runDispatch(t, 10*time.Second, dispatchExec{Dir: dir, Argv: argv, Spec: spec})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("err=%v exit=%d stderr=%q", res.Err, res.ExitCode, res.Stderr)
	}
	if res.Stdout != "worker_id: worker:feat/1-x#s1" {
		t.Errorf("stdout = %q", res.Stdout)
	}

	gotArgv := strings.Split(strings.TrimSuffix(readFile(t, filepath.Join(out, "argv")), "\n"), "\n")
	if !slices.Equal(gotArgv, argv[1:]) {
		t.Errorf("argv = %q, want %q", gotArgv, argv[1:])
	}

	wantDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(out, "cwd"))); got != wantDir {
		t.Errorf("cwd = %q, want %q", got, wantDir)
	}

	specPath := readFile(t, filepath.Join(out, "specpath"))
	if !strings.HasPrefix(filepath.Base(specPath), "houston-dispatch-") {
		t.Errorf("DISPATCH_SPEC = %q, want a houston temp file", specPath)
	}
	var specVars []string
	for _, line := range strings.Split(readFile(t, filepath.Join(out, "env")), "\n") {
		for _, key := range []string{"CREW_ID=", "CREW_WORKER_ID=", "TMUX_PANE="} {
			if strings.HasPrefix(line, key) {
				t.Errorf("inherited %s reached dispatch", line)
			}
		}
		if strings.HasPrefix(line, "DISPATCH_SPEC=") {
			specVars = append(specVars, line)
		}
	}
	if want := []string{"DISPATCH_SPEC=" + specPath}; !slices.Equal(specVars, want) {
		t.Errorf("DISPATCH_SPEC entries = %q, want %q", specVars, want)
	}

	if got := strings.TrimSpace(readFile(t, filepath.Join(out, "mode"))); got != "600" {
		t.Errorf("spec file mode = %s, want 600", got)
	}
	if got := readFile(t, filepath.Join(out, "spec")); got != spec {
		t.Errorf("spec content = %q, want %q", got, spec)
	}
	if _, err := os.Stat(specPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("spec file still present after return: %v", err)
	}
}

func TestExecDispatchEmptySpecSetsNoVar(t *testing.T) {
	out := t.TempDir()
	installFakeDispatch(t, `env > '`+out+`/env'`+"\n")

	res := runDispatch(t, 10*time.Second, dispatchExec{Dir: t.TempDir(), Argv: []string{"dispatch"}})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("err=%v exit=%d", res.Err, res.ExitCode)
	}
	if env := readFile(t, filepath.Join(out, "env")); strings.Contains("\n"+env, "\nDISPATCH_SPEC=") {
		t.Error("DISPATCH_SPEC set with an empty spec")
	}
}

func TestExecDispatchNonZeroExit(t *testing.T) {
	installFakeDispatch(t, "echo partial\necho 'no crew here' >&2\nexit 3\n")

	res := runDispatch(t, 10*time.Second, dispatchExec{Dir: t.TempDir(), Argv: []string{"dispatch"}})
	if res.Err != nil {
		t.Fatalf("err = %v, want nil", res.Err)
	}
	if res.ExitCode != 3 {
		t.Errorf("exit = %d, want 3", res.ExitCode)
	}
	if res.Stderr != "no crew here" || res.Stdout != "partial" {
		t.Errorf("stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}
}

func TestExecDispatchMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	res := runDispatch(t, 10*time.Second, dispatchExec{Dir: t.TempDir(), Argv: []string{"dispatch"}, Spec: "x"})
	if !errors.Is(res.Err, exec.ErrNotFound) {
		t.Errorf("err = %v, want exec.ErrNotFound", res.Err)
	}
}

func TestExecDispatchOutputCapped(t *testing.T) {
	installFakeDispatch(t, "head -c 204800 /dev/zero | tr '\\0' a\n")

	res := runDispatch(t, 10*time.Second, dispatchExec{Dir: t.TempDir(), Argv: []string{"dispatch"}})
	if res.Err != nil || res.ExitCode != 0 {
		t.Fatalf("err=%v exit=%d stderr=%q", res.Err, res.ExitCode, res.Stderr)
	}
	if len(res.Stdout) == 0 || len(res.Stdout) > dispatchOutputCap {
		t.Errorf("stdout len = %d, want 1..%d", len(res.Stdout), dispatchOutputCap)
	}
}

// The grandchild ignores SIGTERM and holds stdout open, so only the
// post-Wait SIGKILL to the process group can end it.
func TestExecDispatchTimeoutKillsGroup(t *testing.T) {
	prev := dispatchGrace
	dispatchGrace = 200 * time.Millisecond
	t.Cleanup(func() { dispatchGrace = prev })

	dir := t.TempDir()
	installFakeDispatch(t, `: > lock
trap 'rm -f lock; exit 143' TERM
bash -c 'trap "" TERM; echo $$ > gpid.tmp; mv gpid.tmp gpid; sleep 300' &
while [ ! -s gpid ]; do sleep 0.01; done
sleep 300
`)
	gpidPath := filepath.Join(dir, "gpid")
	t.Cleanup(func() {
		if pid, err := strconv.Atoi(strings.TrimSpace(readFileOrEmpty(gpidPath))); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	start := time.Now()
	res := runDispatch(t, 300*time.Millisecond, dispatchExec{Dir: dir, Argv: []string{"dispatch"}})
	elapsed := time.Since(start)

	if !errors.Is(res.Err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", res.Err)
	}
	if _, err := os.Stat(filepath.Join(dir, "lock")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("lock survived, TERM trap did not run: %v", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(readFileOrEmpty(gpidPath)))
	if err != nil {
		t.Fatalf("grandchild never recorded its pid: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d still alive", pid)
		}
		time.Sleep(2 * time.Millisecond)
	}

	if total := time.Since(start); total > 4*time.Second {
		t.Errorf("took %v (runner returned after %v), want < 4s", total, elapsed)
	}
}

func readFileOrEmpty(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// A background child that inherits dispatch's stdout/stderr keeps the pipes
// open past dispatch's own exit, so cmd.Run's Wait blocks for WaitDelay and
// returns exec.ErrWaitDelay even though dispatch itself succeeded.
func TestExecDispatchBackgroundChildHoldsPipes(t *testing.T) {
	prev := dispatchGrace
	dispatchGrace = 200 * time.Millisecond
	t.Cleanup(func() { dispatchGrace = prev })

	dir := t.TempDir()
	installFakeDispatch(t, `echo "worker_id: worker:feat/1-x#s1"
sleep 3 &
echo $! > '`+dir+`/bgpid'
exit 0
`)
	t.Cleanup(func() {
		if pid, err := strconv.Atoi(strings.TrimSpace(readFileOrEmpty(filepath.Join(dir, "bgpid")))); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	res := runDispatch(t, 10*time.Second, dispatchExec{Dir: t.TempDir(), Argv: []string{"dispatch"}})
	if res.Err != nil {
		t.Errorf("err = %v, want nil", res.Err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit = %d, want 0", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "worker_id: worker:feat/1-x#s1") {
		t.Errorf("stdout = %q, want it to contain the worker_id", res.Stdout)
	}
}

func TestHandleDispatchWithRealRunner(t *testing.T) {
	installFakeDispatch(t, "echo 'worker_id: worker:feat/1-x#s1'\n")
	repo := newDispatchTestRepo(t, "1700000000-123")
	repo.Path = t.TempDir()
	s := newDispatchServer(t, execDispatch, stubDispatchRepos([]dispatchRepo{repo}, nil))
	req := validDispatchRequest(repo, repo.Crews[0])
	req.Spec = "do the thing"

	rec := doDispatch(t, s, dispatchHTTPRequest("POST", dispatchRequestJSON(t, req)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	body := decodeDispatchResponse(t, rec)
	if body.WorkerID != "worker:feat/1-x#s1" {
		t.Errorf("worker_id = %q", body.WorkerID)
	}
	if body.Branch != "feat/1-x" {
		t.Errorf("branch = %q", body.Branch)
	}
}
