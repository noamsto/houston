package runs

import (
	"crypto/sha256"
	"encoding/hex"
)

// draftKeyOf names the session a pane run is hosting, for the UI to key
// unsent composer drafts by. Pane ids restart at %0 after a tmux server
// restart, so the run id alone can't tell two agents apart. The hash is
// one-way: the raw session id never reaches the wire. Empty for a run with no
// pane, whose id is already session- or branch-keyed.
func draftKeyOf(r Run) string {
	if r.Tmux == nil {
		return ""
	}
	h := sha256.New()
	for _, part := range []string{r.Tmux.Server, r.Tmux.PaneID, r.Session} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}
