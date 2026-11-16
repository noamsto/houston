package crewview

import (
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

var ErrNotExecutable = errors.New("crewview: action is not executable")
var ErrTextRequired = errors.New("crewview: reply text required")

const MaxReplyText = 8 << 10

const replyPlaceholder = "<text>"

// Check reports whether houston may run its action: allowlisted verb, exact
// argv shape, crew and branch matching the item, local or remote_worker origin.
func Check(it Item) error {
	a := it.Action
	if a == nil || it.Crew == "" || (it.Origin != OriginLocal && it.Origin != OriginRemote) || len(a.Argv) == 0 {
		return ErrNotExecutable
	}
	// argv[0] is never executed (the caller runs its own resolved crew), but a
	// producer that names another program means the argv is not crew's to run.
	if bin := a.Argv[0]; filepath.Base(bin) != "crew" || (bin != "crew" && !filepath.IsAbs(bin)) {
		return ErrNotExecutable
	}
	want := shape(it)
	if want == nil || !slices.Equal(a.Argv[1:], want) {
		return ErrNotExecutable
	}
	return nil
}

func Executable(it Item) bool { return Check(it) == nil }

// Args returns the argv after the crew binary, with text substituted for a
// reply's placeholder.
func Args(it Item, text string) ([]string, error) {
	a := it.Action
	if a == nil || len(a.Argv) == 0 {
		return nil, ErrNotExecutable
	}
	if err := Check(it); err != nil {
		return nil, err
	}
	args := slices.Clone(a.Argv[1:])
	if a.Verb != "reply" {
		return args, nil
	}
	if strings.TrimSpace(text) == "" || len(text) > MaxReplyText {
		return nil, ErrTextRequired
	}
	args[len(args)-1] = text
	return args, nil
}

// shape returns the argv after the crew binary that the item's verb must carry,
// or nil when the verb is not runnable from houston.
func shape(it Item) []string {
	a := it.Action
	if a == nil {
		return nil
	}
	switch a.Verb {
	case "peek":
		if it.Branch == "" {
			return nil
		}
		return []string{"where", "--crew", it.Crew, "--", it.Branch}
	case "nudge":
		// Worker is the codename, else the branch: the target crew nudge expects.
		if it.Worker == "" || strings.HasPrefix(it.Worker, "-") {
			return nil
		}
		return []string{"nudge", "--crew", it.Crew, "--", it.Worker}
	case "reply":
		if it.Branch == "" {
			return nil
		}
		return []string{"reply", "--crew", it.Crew, "--", "worker:" + it.Branch, replyPlaceholder}
	case "merge":
		if a.PR == nil {
			return nil
		}
		return []string{"merge", strconv.Itoa(*a.PR), "--crew", it.Crew}
	}
	return nil
}
