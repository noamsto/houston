// Package mode decides which of houston's two modes it runs in: dispatcher
// mode (crew bus, Dispatch and Crews tabs) or tmux mode (plain tmux and agent
// monitor).
package mode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Mode string

const (
	Auto       Mode = "auto"
	Dispatcher Mode = "dispatcher"
	Tmux       Mode = "tmux"
)

// Parse accepts exactly "auto", "dispatcher" and "tmux".
func Parse(s string) (Mode, error) {
	switch m := Mode(s); m {
	case Auto, Dispatcher, Tmux:
		return m, nil
	}
	return "", fmt.Errorf("invalid mode %q (valid: %s, %s, %s)", s, Auto, Dispatcher, Tmux)
}

// Probe is the outside world Resolve consults in auto.
type Probe struct {
	ServerPath func(ctx context.Context) (string, error) // the tmux server's global PATH
	OwnPath    string                                    // houston's own PATH
	Executable func(name, path string) bool              // name resolves on the list path
}

// Resolve never returns Auto. Callers pass a Parse result; any value other
// than Dispatcher or Tmux is treated as Auto. reason is for a log line and
// never contains a PATH value.
func Resolve(ctx context.Context, requested Mode, p Probe) (m Mode, reason string) {
	if requested == Dispatcher || requested == Tmux {
		return requested, "forced by -mode"
	}

	search := p.OwnPath
	var note string
	sp, err := p.ServerPath(ctx)
	switch {
	case err != nil:
		note = fmt.Sprintf("tmux server PATH unavailable (%v); checked houston's PATH only", err)
	case sp == "":
		note = "tmux server PATH unavailable (empty); checked houston's PATH only"
	case p.OwnPath == "":
		search = sp
	default:
		search = sp + string(os.PathListSeparator) + p.OwnPath
	}

	var missing []string
	for _, name := range []string{"dispatch", "crew"} {
		if !p.Executable(name, search) {
			missing = append(missing, name)
		}
	}

	m, reason = Dispatcher, "dispatch and crew found"
	if len(missing) > 0 {
		m, reason = Tmux, strings.Join(missing, " and ")+" not found"
	}
	if note != "" {
		reason += "; " + note
	}
	return m, reason
}

// Find returns the first executable regular file named name in an absolute
// element of path. Relative and empty elements never match, as in OnPath.
func Find(name, path string) (string, bool) {
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		file := filepath.Join(dir, name)
		info, err := os.Stat(file)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return file, true
		}
	}
	return "", false
}

// OnPath reports whether name is an executable regular file in some absolute
// element of path. An empty or relative element never matches: exec.LookPath
// refuses a match it finds there (exec.ErrDot), so dispatch could not run it.
func OnPath(name, path string) bool {
	_, ok := Find(name, path)
	return ok
}
