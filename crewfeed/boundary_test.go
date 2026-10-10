package crewfeed

import (
	"os/exec"
	"strings"
	"testing"
)

// TestBoundaryStdlibOnly guards the one hard rule this package exists for:
// crewfeed/ never imports the rest of houston. `go list -deps` from the
// package directory lists every transitive import; anything non-standard
// other than the package itself is a violation.
func TestBoundaryStdlibOnly(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}

	out, err := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		if line != "github.com/noamsto/houston/crewfeed" {
			t.Fatalf("crewfeed package depends on non-stdlib package %q", line)
		}
	}
}
