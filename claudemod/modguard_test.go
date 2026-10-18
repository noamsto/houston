package claudemod

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"testing"
)

var (
	// The mod chains calls across lines (`$.http\n    .fetch(`), so whitespace
	// is allowed between every part of a call.
	forbiddenAPI   = regexp.MustCompile(`\$\s*\.\s*(prompt|command|tool|agent|ui|process|fs|state|store|turn|audio|model|clock)\s*\.|\$\s*\.\s*session\s*\.\s*(append|send)|\$\s*\.\s*env\s*\.\s*set|\bdeny\b|\bdrop\b`)
	apiCall        = regexp.MustCompile(`\$\s*\.\s*([a-zA-Z]+)\s*\.\s*([a-zA-Z]+)`)
	computedAccess = regexp.MustCompile(`\$\s*\[|=\s*\$\s*[;,)}\n]`)
	allowedCalls   = []string{"$.http.fetch", "$.env.get", "$.session.id"}
)

// scanMod returns why src is not a read-only mod, or nil.
func scanMod(src string) error {
	if m := forbiddenAPI.FindAllString(src, -1); len(m) > 0 {
		return fmt.Errorf("mod uses forbidden API: %q", m)
	}
	// Bracket access or a bare $ assigned to a name would reach the API
	// without a `$.noun.method` the scan can see.
	if m := computedAccess.FindAllString(src, -1); len(m) > 0 {
		return fmt.Errorf("mod reaches $ by computed or aliased access: %q", m)
	}
	seen := make(map[string]bool, len(allowedCalls))
	for _, m := range apiCall.FindAllStringSubmatch(src, -1) {
		call := "$." + m[1] + "." + m[2]
		if !slices.Contains(allowedCalls, call) {
			return fmt.Errorf("mod calls %s; only %v are read-only", call, allowedCalls)
		}
		seen[call] = true
	}
	for _, call := range allowedCalls {
		if !seen[call] {
			return fmt.Errorf("scan found no %s call; the scan cannot see it", call)
		}
	}
	return nil
}

// The mod must never act on the session, and CI cannot run the plugin test kit
// (`claude plugin test`), so this scans its source for anything beyond reads.
func TestModIsReadOnly(t *testing.T) {
	src, err := os.ReadFile("../contrib/claude-mod/hooks/register.ts")
	if err != nil {
		t.Fatal(err)
	}
	if err := scanMod(string(src)); err != nil {
		t.Fatal(err)
	}
}

func TestScanMod(t *testing.T) {
	const reads = "$.env.get('HOME');\n$.session.id();\n$.http\n  .fetch('http://houston/v1/record', {})\n"
	tests := []struct {
		name string
		src  string
		ok   bool
	}{
		{"read-only", reads, true},
		{"split prompt.submit", reads + "$.prompt\n  .submit({ text: 'x' })\n", false},
		{"bracket access", reads + "$['prompt'].submit({ text: 'x' })\n", false},
		{"bare $ assigned", reads + "const p = $\n", false},
		{"unlisted call", reads + "$.http.post('http://houston/v1/record', {})\n", false},
		{"missing read", "$.env.get('HOME')\n$.session.id()\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := scanMod(tt.src)
			if tt.ok && err != nil {
				t.Fatalf("rejected: %v", err)
			}
			if !tt.ok && err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
