package mode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	srvPath = "/srv-secret-path"
	ownPath = "/own-secret-path"
)

var sep = string(os.PathListSeparator)

type fakeProbe struct {
	Probe
	serverCalled bool
	searched     []string // path of every Executable call
	asked        []string // name of every Executable call
}

// newFake reports the names in have as present on every path.
func newFake(sp string, spErr error, have ...string) *fakeProbe {
	f := &fakeProbe{}
	f.Probe = Probe{
		ServerPath: func(context.Context) (string, error) {
			f.serverCalled = true
			return sp, spErr
		},
		OwnPath: ownPath,
		Executable: func(name, path string) bool {
			f.asked = append(f.asked, name)
			f.searched = append(f.searched, path)
			return slices.Contains(have, name)
		},
	}
	return f
}

func TestParse(t *testing.T) {
	for _, s := range []string{"auto", "dispatcher", "tmux"} {
		m, err := Parse(s)
		if err != nil || string(m) != s {
			t.Errorf("Parse(%q) = %q, %v", s, m, err)
		}
	}
	for _, s := range []string{"", "Dispatcher", "x"} {
		_, err := Parse(s)
		if err == nil {
			t.Errorf("Parse(%q): want error", s)
			continue
		}
		for _, v := range []string{"auto", "dispatcher", "tmux"} {
			if !strings.Contains(err.Error(), v) {
				t.Errorf("Parse(%q) error %q does not name %q", s, err, v)
			}
		}
	}
}

func TestResolveForced(t *testing.T) {
	tests := []struct {
		name      string
		requested Mode
		have      []string
	}{
		{"tmux despite both binaries", Tmux, []string{"dispatch", "crew"}},
		{"dispatcher despite none", Dispatcher, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(srvPath, nil, tt.have...)
			m, reason := Resolve(context.Background(), tt.requested, f.Probe)
			if m != tt.requested {
				t.Errorf("mode = %q, want %q", m, tt.requested)
			}
			if reason != "forced by -mode" {
				t.Errorf("reason = %q", reason)
			}
			if f.serverCalled || len(f.searched) != 0 {
				t.Errorf("probes called: server=%v executable=%v", f.serverCalled, f.searched)
			}
		})
	}
}

func TestResolveAuto(t *testing.T) {
	tests := []struct {
		name       string
		sp         string
		spErr      error
		have       []string
		want       Mode
		wantSearch string
		inReason   []string
	}{
		{"both found", srvPath, nil, []string{"dispatch", "crew"}, Dispatcher, srvPath + sep + ownPath, []string{"dispatch and crew found"}},
		{"crew missing", srvPath, nil, []string{"dispatch"}, Tmux, srvPath + sep + ownPath, []string{"crew not found"}},
		{"dispatch missing", srvPath, nil, []string{"crew"}, Tmux, srvPath + sep + ownPath, []string{"dispatch not found"}},
		{"both missing", srvPath, nil, nil, Tmux, srvPath + sep + ownPath, []string{"dispatch and crew not found"}},
		{"server error, both found", "", errors.New("no server running"), []string{"dispatch", "crew"}, Dispatcher, ownPath, []string{"dispatch and crew found", "unavailable", "no server running"}},
		{"server error, absent", "", errors.New("no server running"), nil, Tmux, ownPath, []string{"dispatch and crew not found", "unavailable"}},
		{"server path empty", "", nil, []string{"dispatch", "crew"}, Dispatcher, ownPath, []string{"empty"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(tt.sp, tt.spErr, tt.have...)
			m, reason := Resolve(context.Background(), Auto, f.Probe)
			if m != tt.want {
				t.Errorf("mode = %q, want %q (reason %q)", m, tt.want, reason)
			}
			if !f.serverCalled {
				t.Error("ServerPath not called")
			}
			if len(f.searched) == 0 {
				t.Fatal("Executable not called")
			}
			for _, got := range f.searched {
				if got != tt.wantSearch {
					t.Errorf("Executable saw path %q, want %q", got, tt.wantSearch)
				}
			}
			for _, name := range []string{"dispatch", "crew"} {
				if !slices.Contains(f.asked, name) {
					t.Errorf("Executable never asked for %q", name)
				}
			}
			for _, s := range tt.inReason {
				if !strings.Contains(reason, s) {
					t.Errorf("reason %q missing %q", reason, s)
				}
			}
			for _, secret := range []string{srvPath, ownPath} {
				if strings.Contains(reason, secret) {
					t.Errorf("reason %q leaks a PATH value", reason)
				}
			}
		})
	}
}

func TestResolveAutoEmptyOwnPath(t *testing.T) {
	f := newFake(srvPath, nil, "dispatch", "crew")
	f.OwnPath = ""
	m, _ := Resolve(context.Background(), Auto, f.Probe)
	if m != Dispatcher {
		t.Errorf("mode = %q, want %q", m, Dispatcher)
	}
	for _, got := range f.searched {
		if got != srvPath {
			t.Errorf("Executable saw path %q, want %q", got, srvPath)
		}
	}
}

func TestResolveUnknownIsAuto(t *testing.T) {
	f := newFake(srvPath, nil, "dispatch", "crew")
	m, _ := Resolve(context.Background(), "", f.Probe)
	if m != Dispatcher || !f.serverCalled {
		t.Errorf("mode = %q, serverCalled = %v", m, f.serverCalled)
	}
}

func writeFile(t *testing.T, dir, name string, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, name), perm); err != nil {
		t.Fatal(err)
	}
}

func TestOnPath(t *testing.T) {
	exec, plain, other := t.TempDir(), t.TempDir(), t.TempDir()
	writeFile(t, exec, "crew", 0o755)
	writeFile(t, plain, "crew", 0o644)
	if err := os.Mkdir(filepath.Join(other, "crew"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"executable file", exec, true},
		{"non-executable file", plain, false},
		{"directory", other, false},
		{"found later in list", plain + sep + exec, true},
		{"missing dir", filepath.Join(exec, "nope"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OnPath("crew", tt.path); got != tt.want {
				t.Errorf("OnPath(crew, %q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// Not parallel: it changes the process working directory.
func TestOnPathIgnoresCwdRelativeElements(t *testing.T) {
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(prev); err != nil {
			t.Fatal(err)
		}
	})
	dir := t.TempDir()
	writeFile(t, dir, "crew", 0o755)
	if err := os.Mkdir(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "bin"), "crew", 0o755)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".", "bin", "", sep} {
		if OnPath("crew", path) {
			t.Errorf("OnPath(crew, %q) = true, want false", path)
		}
	}
	if !OnPath("crew", dir) {
		t.Errorf("OnPath(crew, %q) = false, want true", dir)
	}
}
