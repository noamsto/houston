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
	out, file string
}

// newWrapperFixture puts a fake `dispatcher` first on PATH that records its
// argv, cwd and the crew variables, and appends "launcher" to <out>/calls;
// plus a launch file for it.
func newWrapperFixture(t *testing.T, args []string) wrapperFixture {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}
	out := t.TempDir()
	bin := t.TempDir()
	script := "#!" + bash + "\nout='" + out + "'\n" + `printf '%s\n' "$@" > "$out/argv"
pwd -P > "$out/cwd"
printf 'CREW_ID=%s\nCREW_WORKER_ID=%s\nDISPATCH_SPEC=%s\nCREW_ROLE_ID=%s\n' "${CREW_ID-<unset>}" "${CREW_WORKER_ID-<unset>}" "${DISPATCH_SPEC-<unset>}" "${CREW_ROLE_ID-<unset>}" > "$out/env"
echo launcher >> "$out/calls"
`
	if err := os.WriteFile(filepath.Join(bin, "dispatcher"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return wrapperFixture{out: out, file: writeTestLaunchFile(t, dispatcherLaunchFile{Args: args, CrewID: dispatchTestNewCrewID})}
}

func writeTestLaunchFile(t *testing.T, lf dispatcherLaunchFile) string {
	t.Helper()
	b, err := json.Marshal(lf)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "dispatcher-1.json")
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return file
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
		"CREW_ID=leaked", "CREW_WORKER_ID=w", "DISPATCH_SPEC=x", "CREW_ROLE_ID=r")
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
	wantEnv := "CREW_ID=" + dispatchTestNewCrewID + "\nCREW_WORKER_ID=<unset>\nDISPATCH_SPEC=<unset>\nCREW_ROLE_ID=<unset>\n"
	if got := readFile(t, filepath.Join(f.out, "env")); got != wantEnv {
		t.Errorf("launcher crew env = %q, want %q", got, wantEnv)
	}
	if _, err := os.Stat(f.file); !os.IsNotExist(err) {
		t.Errorf("launch file still present (stat err %v)", err)
	}
}

// The wrapper stamps its own window by pane id; houston never makes the
// window current, so the stamps must not depend on that.
func TestDispatcherLaunchWrapperStampsWindow(t *testing.T) {
	f := newWrapperFixture(t, []string{"--agent", "pi"})
	installFakeTmux(t, t.TempDir(), `printf '%s\n' "$*" >> '`+f.out+`/calls'`+"\n")

	if out, err := runWrapper(t, f.file, t.TempDir(), "TMUX_PANE=%5"); err != nil {
		t.Fatalf("wrapper failed: %v\n%s", err, out)
	}
	want := strings.Join([]string{
		"set-option -w -t %5 remain-on-exit failed",
		"set-option -w -t %5 @crew_name dispatcher",
		"set-option -w -t %5 @crew_color colour99",
		"launcher",
	}, "\n") + "\n"
	if got := readFile(t, filepath.Join(f.out, "calls")); got != want {
		t.Errorf("calls =\n%s\nwant\n%s", got, want)
	}
	if got := readFile(t, filepath.Join(f.out, "argv")); got != "--agent\npi\n" {
		t.Errorf("launcher argv = %q", got)
	}
}

func TestDispatcherLaunchWrapperFailures(t *testing.T) {
	noLauncher := writeTestLaunchFile(t, dispatcherLaunchFile{Args: []string{"--agent", "pi"}, CrewID: dispatchTestNewCrewID})
	for _, tc := range []struct {
		name, file, want string
		env              []string
	}{
		{"missing file", filepath.Join(t.TempDir(), "gone.json"), "no such file", nil},
		{"launcher not on PATH", noLauncher, "dispatcher launcher not found", []string{"PATH=" + t.TempDir()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runWrapper(t, tc.file, t.TempDir(), tc.env...)
			if err == nil {
				t.Fatalf("wrapper succeeded, want a non-zero exit\n%s", out)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("output = %q, want it to mention %q", out, tc.want)
			}
		})
	}
	if _, err := os.Stat(noLauncher); !os.IsNotExist(err) {
		t.Error("a launch file the wrapper read was not removed")
	}
}

func TestLaunchEnvSingleCrewID(t *testing.T) {
	in := []string{"PATH=/bin", "CREW_ID=leaked", "HOME=/h", "CREW_WORKER_ID=w", "DISPATCH_SPEC=/tmp/spec", "CREW_ROLE_ID=r", "CREW_ID=again"}
	got := launchEnv(in, "1700000000-42")
	want := []string{"PATH=/bin", "HOME=/h", "CREW_ID=1700000000-42"}
	if !slices.Equal(got, want) {
		t.Errorf("launchEnv = %q, want %q", got, want)
	}
}

func TestDispatcherLaunchWithRealTmuxRunner(t *testing.T) {
	out := t.TempDir()
	f := newLaunchFixture(t)
	installFakeTmux(t, out, `case "$1" in
has-session) echo "can't find session: proj" >&2; exit 1 ;;
show-environment) echo "PATH=$PATH" ;;
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
	if !slices.Equal(argv[:8], []string{"new-session", "-d", "-E", "-P", "-F", launchFormat, "-s", "proj"}) {
		t.Errorf("tmux argv = %q", argv)
	}
}

// usePrivateTmux points `tmux` on PATH at a fresh private server whose first
// session is named session, killed at cleanup, and returns bash's path.
func usePrivateTmux(t *testing.T, session string) string {
	t.Helper()
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not found")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}
	// A socket path is capped near 100 bytes, too short for t.TempDir().
	sockDir, err := os.MkdirTemp("", "ht")
	if err != nil {
		t.Skip("no short temp dir for a tmux socket")
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })

	bin := t.TempDir()
	wrapper := "#!" + bash + "\nexec '" + realTmux + "' -S '" + sockDir + "/s' -f /dev/null \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx := context.Background()
	if _, err := execTmux(ctx, []string{"new-session", "-d", "-s", session, "--", "sleep", "600"}); err != nil {
		t.Fatalf("start private tmux: %v", err)
	}
	t.Cleanup(func() { _, _ = execTmux(ctx, []string{"kill-server"}) })
	return bash
}

// TestDispatcherLaunchNewSessionKeepsServerEnv runs the launch against a
// private tmux server: a session houston creates must see the server's global
// SSH_AUTH_SOCK, not houston's lack of one.
func TestDispatcherLaunchNewSessionKeepsServerEnv(t *testing.T) {
	bash := usePrivateTmux(t, "base")
	if _, err := execTmux(context.Background(), []string{"set-environment", "-g", "SSH_AUTH_SOCK", "/fake/agent"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_AUTH_SOCK", "")
	if err := os.Unsetenv("SSH_AUTH_SOCK"); err != nil {
		t.Fatal(err)
	}

	f := newLaunchFixture(t)
	f.s.tmuxRun = execTmux
	out := filepath.Join(t.TempDir(), "agent")
	f.s.houstonExe = filepath.Join(t.TempDir(), "houston")
	script := "#!" + bash + "\nprintf '%s' \"${SSH_AUTH_SOCK-unset}\" > '" + out + "'\n: > '" + f.crewDir + "/pid'\nsleep 30\n"
	if err := os.WriteFile(f.s.houstonExe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	f.s.launchCheck = 5 * time.Second

	rec, resp := f.post(t, f.request())
	wantStatus(t, rec, 200)
	if resp.Session != "proj" {
		t.Errorf("session = %q, want a new one named proj", resp.Session)
	}
	if got := readFile(t, out); got != "/fake/agent" {
		t.Errorf("SSH_AUTH_SOCK in the new session = %q, want the server's global /fake/agent", got)
	}
}

// TestDispatcherLaunchPaneGetsServerPath: a pane houston creates must see the
// tmux server's global PATH, not houston's. tmux gives a pane made by a
// session-less command client that client's PATH (over the global one and
// over -e PATH), and houston is that client.
func TestDispatcherLaunchPaneGetsServerPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		base string
	}{{"new-session", "base"}, {"new-window", "proj"}} {
		t.Run(tc.name, func(t *testing.T) {
			bash := usePrivateTmux(t, tc.base)
			const marker = "/srvpath-marker/bin"
			if _, err := execTmux(context.Background(), []string{"set-environment", "-g", "PATH", marker + ":" + os.Getenv("PATH")}); err != nil {
				t.Fatal(err)
			}

			f := newLaunchFixture(t)
			f.s.tmuxRun = execTmux
			out := filepath.Join(t.TempDir(), "path")
			f.s.houstonExe = filepath.Join(t.TempDir(), "houston")
			script := "#!" + bash + "\nprintf '%s' \"$PATH\" > '" + out + "'\n: > '" + f.crewDir + "/pid'\nsleep 30\n"
			if err := os.WriteFile(f.s.houstonExe, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			f.s.launchCheck = 5 * time.Second

			rec, resp := f.post(t, f.request())
			wantStatus(t, rec, 200)
			if resp.Session != "proj" {
				t.Errorf("session = %q, want proj", resp.Session)
			}
			if got := readFile(t, out); !strings.Contains(":"+got+":", ":"+marker+":") {
				t.Errorf("pane PATH lacks the server's global entry %s: houston's PATH reached the pane", marker)
			}
		})
	}
}

// TestDispatcherLaunchKeepsCurrentWindow runs a launch against a private tmux
// server: the new window is created with -d, so the session's current window
// — what a client viewing the session displays — is not switched away.
func TestDispatcherLaunchKeepsCurrentWindow(t *testing.T) {
	bash := usePrivateTmux(t, "proj")
	if _, err := execTmux(context.Background(), []string{"set-environment", "-g", "PATH", os.Getenv("PATH")}); err != nil {
		t.Fatal(err)
	}
	const base = "0"
	if got := tmuxOut(t, "display-message", "-t", "proj", "-p", "#{window_index}"); got != base {
		t.Fatalf("session current window = %q, want %q", got, base)
	}

	f := newLaunchFixture(t)
	f.s.tmuxRun = execTmux
	f.s.houstonExe = filepath.Join(t.TempDir(), "houston")
	script := "#!" + bash + "\n: > '" + f.crewDir + "/pid'\nsleep 30\n"
	if err := os.WriteFile(f.s.houstonExe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	f.s.launchCheck = 5 * time.Second

	rec, resp := f.post(t, f.request())
	wantStatus(t, rec, 200)
	if resp.Session != "proj" || resp.Pane == "" {
		t.Fatalf("response = %+v, want a new window in proj", resp)
	}
	if got := tmuxOut(t, "display-message", "-t", "proj", "-p", "#{window_index}"); got != base {
		t.Errorf("session current window = %q, want %q: a launch must not switch a viewing client", got, base)
	}
	if got := tmuxOut(t, "list-windows", "-t", "proj", "-F", "#{window_index}"); got != base+"\n1" {
		t.Errorf("windows = %q, want the new window beside the base one", got)
	}
}

// TestDispatcherLaunchWrapperStampsNonCurrentWindow runs the real wrapper
// against a private tmux server: on a window that is not current, the
// wrapper's pre-stamp still lands by pane id, and it never selects the
// window (which would switch a viewing client).
func TestDispatcherLaunchWrapperStampsNonCurrentWindow(t *testing.T) {
	usePrivateTmux(t, "proj")
	pane := tmuxOut(t, "display-message", "-t", "proj", "-p", "#{pane_id}")
	tmuxOut(t, "new-window", "-d", "-t", "proj:1", "--", "sleep", "600")
	tmuxOut(t, "select-window", "-t", "proj:1")

	f := newWrapperFixture(t, []string{"--agent", "pi"})
	if out, err := runWrapper(t, f.file, t.TempDir(), "TMUX_PANE="+pane); err != nil {
		t.Fatalf("wrapper failed: %v\n%s", err, out)
	}
	if got := tmuxOut(t, "display-message", "-t", "proj", "-p", "#{window_index}"); got != "1" {
		t.Errorf("session current window = %q, want 1: the wrapper must not switch the client", got)
	}
	if got := tmuxOut(t, "show-options", "-w", "-t", pane, "@crew_name"); !strings.Contains(got, "dispatcher") {
		t.Errorf("@crew_name = %q, want it stamped on the wrapper's own pane", got)
	}
}
