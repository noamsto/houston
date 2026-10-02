package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// installFakeTmux puts an executable `tmux` script first on PATH that
// records its argv, one element per line, in <out>/tmux-argv.
func installFakeTmux(t *testing.T, out, body string) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}
	bin := t.TempDir()
	script := "#!" + bash + "\nprintf '%s\\n' \"$@\" > '" + out + "/tmux-argv'\n" + body
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestExecTmuxPassesArgvVerbatim(t *testing.T) {
	out := t.TempDir()
	installFakeTmux(t, out, "echo '  sess\t@1\t%2  '\n")
	argv := []string{"new-window", "-c", "/a dir/with spaces", "--", `$(touch PWNED)`, `it's "quoted"`, "tail;"}

	got, err := execTmux(context.Background(), argv)
	if err != nil {
		t.Fatalf("execTmux: %v", err)
	}
	if got != "sess\t@1\t%2" {
		t.Errorf("stdout = %q, want it trimmed", got)
	}
	if recorded := readFile(t, filepath.Join(out, "tmux-argv")); recorded != strings.Join(argv, "\n")+"\n" {
		t.Errorf("argv = %q, want %q", recorded, argv)
	}
	if _, err := os.Stat(filepath.Join(out, "PWNED")); err == nil {
		t.Error("a shell expanded $(…)")
	}
}

func TestExecTmuxErrorCarriesStderr(t *testing.T) {
	installFakeTmux(t, t.TempDir(), "echo \"can't find session: =proj\" >&2\nexit 1\n")

	_, err := execTmux(context.Background(), []string{"has-session", "-t", "=proj"})
	if err == nil {
		t.Fatal("expected an error for a non-zero exit")
	}
	if !strings.Contains(err.Error(), "can't find session: =proj") {
		t.Errorf("err = %v, want tmux's stderr in it", err)
	}
}

// TestDispatcherLaunchHelper is not a test on its own: the wrapper tests
// re-exec the test binary into it, since ExecDispatcherLaunch replaces the
// calling process.
func TestDispatcherLaunchHelper(t *testing.T) {
	if os.Getenv("HOUSTON_LAUNCH_HELPER") != "1" {
		t.Skip("helper process only")
	}
	err := ExecDispatcherLaunch(os.Getenv("HOUSTON_LAUNCH_FILE"))
	fmt.Fprintln(os.Stderr, "launch-dispatcher:", err)
	os.Exit(1)
}

type wrapperFixture struct {
	out, file, launcher string
}

// newWrapperFixture writes a fake launcher that records its argv, cwd and
// the crew variables, plus a launch file pointing at it.
func newWrapperFixture(t *testing.T, args []string) wrapperFixture {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}
	out := t.TempDir()
	launcher := filepath.Join(t.TempDir(), "dispatcher")
	script := "#!" + bash + "\nout='" + out + "'\n" + `printf '%s\n' "$@" > "$out/argv"
pwd -P > "$out/cwd"
printf 'CREW_ID=%s\nCREW_WORKER_ID=%s\nDISPATCH_SPEC=%s\n' "${CREW_ID-<unset>}" "${CREW_WORKER_ID-<unset>}" "${DISPATCH_SPEC-<unset>}" > "$out/env"
`
	if err := os.WriteFile(launcher, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(dispatcherLaunchFile{Launcher: launcher, Args: args, CrewID: dispatchTestNewCrewID})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "dispatcher-1.json")
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return wrapperFixture{out: out, file: file, launcher: launcher}
}

// runWrapper runs ExecDispatcherLaunch(file) in a child test binary from dir,
// with a leaked crew identity in its environment.
func runWrapper(t *testing.T, file, dir string, extraEnv ...string) ([]byte, error) {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		if key, _, _ := strings.Cut(kv, "="); key != "TMUX_PANE" {
			env = append(env, kv)
		}
	}
	env = append(env, "HOUSTON_LAUNCH_HELPER=1", "HOUSTON_LAUNCH_FILE="+file,
		"CREW_ID=leaked", "CREW_WORKER_ID=w", "DISPATCH_SPEC=x")
	env = append(env, extraEnv...)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDispatcherLaunchHelper$")
	cmd.Env = env
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

func TestDispatcherLaunchWrapperExecsLauncher(t *testing.T) {
	prompt := "2 tasks: (1) fix the $(flaky) test; now (2) it's \"done\""
	args := []string{"--agent", "claude", "--model", "opus", prompt}
	f := newWrapperFixture(t, args)
	cwd := realTempDir(t)

	if out, err := runWrapper(t, f.file, cwd); err != nil {
		t.Fatalf("wrapper failed: %v\n%s", err, out)
	}

	if got := readFile(t, filepath.Join(f.out, "argv")); got != strings.Join(args, "\n")+"\n" {
		t.Errorf("launcher argv = %q, want %q", got, args)
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(f.out, "cwd"))); got != cwd {
		t.Errorf("launcher cwd = %q, want %q", got, cwd)
	}
	wantEnv := "CREW_ID=" + dispatchTestNewCrewID + "\nCREW_WORKER_ID=<unset>\nDISPATCH_SPEC=<unset>\n"
	if got := readFile(t, filepath.Join(f.out, "env")); got != wantEnv {
		t.Errorf("launcher crew env = %q, want %q", got, wantEnv)
	}
	if _, err := os.Stat(f.file); !os.IsNotExist(err) {
		t.Errorf("launch file still present (stat err %v)", err)
	}
}

func TestDispatcherLaunchWrapperSetsRemainOnExit(t *testing.T) {
	f := newWrapperFixture(t, []string{"--agent", "pi"})
	tmuxOut := t.TempDir()
	installFakeTmux(t, tmuxOut, "")

	if out, err := runWrapper(t, f.file, t.TempDir(), "TMUX_PANE=%5"); err != nil {
		t.Fatalf("wrapper failed: %v\n%s", err, out)
	}
	want := []string{"set-option", "-w", "-t", "%5", "remain-on-exit", "failed"}
	if got := readFile(t, filepath.Join(tmuxOut, "tmux-argv")); got != strings.Join(want, "\n")+"\n" {
		t.Errorf("tmux argv = %q, want %q", got, want)
	}
	if got := readFile(t, filepath.Join(f.out, "argv")); got != "--agent\npi\n" {
		t.Errorf("launcher argv = %q", got)
	}
}

func TestDispatcherLaunchWrapperFailures(t *testing.T) {
	relative := filepath.Join(t.TempDir(), "relative.json")
	if err := os.WriteFile(relative, []byte(`{"launcher":"dispatcher","args":[],"crew_id":"1-2"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, file, want string
	}{
		{"missing file", filepath.Join(t.TempDir(), "gone.json"), "no such file"},
		{"relative launcher", relative, "not an absolute path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runWrapper(t, tc.file, t.TempDir())
			if err == nil {
				t.Fatalf("wrapper succeeded, want a non-zero exit\n%s", out)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("output = %q, want it to mention %q", out, tc.want)
			}
		})
	}
	if _, err := os.Stat(relative); !os.IsNotExist(err) {
		t.Error("a launch file the wrapper read was not removed")
	}
}

func TestDispatcherLaunchWithRealTmuxRunner(t *testing.T) {
	out := t.TempDir()
	f := newLaunchFixture(t)
	installFakeTmux(t, out, `case "$1" in
has-session) exit 1 ;;
new-session) mkdir -p '`+f.crewDir+`' && : > '`+f.crewDir+`/pid'; printf 'proj\t@3\t%%7\n' ;;
esac
`)
	f.s.tmuxRun = execTmux

	rec, resp := f.post(t, f.request())
	wantStatus(t, rec, 200)
	if resp.Pane != "%7" || resp.RunID != "pane-7" || resp.Session != "proj" {
		t.Errorf("response = %+v", resp)
	}
	argv := strings.Split(strings.TrimSuffix(readFile(t, filepath.Join(out, "tmux-argv")), "\n"), "\n")
	if !slices.Equal(argv[:7], []string{"new-session", "-d", "-P", "-F", launchFormat, "-s", "proj"}) {
		t.Errorf("tmux argv = %q", argv)
	}
}
