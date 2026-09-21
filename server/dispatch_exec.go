package server

import (
	"context"
	"errors"
)

// dispatchExec is one dispatch invocation, fully resolved by the handler. The
// runner adds nothing and decides nothing.
type dispatchExec struct {
	Dir  string   // matched repo path
	Argv []string // Argv[0] == "dispatch"
	Spec string   // task body; "" means no DISPATCH_SPEC is set
}

type dispatchResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error // exec start failure → 502; context.DeadlineExceeded → 504
}

type dispatchRunner func(ctx context.Context, x dispatchExec) dispatchResult

// execDispatch is the real runner. Its process-group/signal/timeout handling
// is implemented separately (SPEC.md §Execution); this placeholder only
// keeps the package compiling until that lands.
func execDispatch(_ context.Context, _ dispatchExec) dispatchResult {
	return dispatchResult{Err: errors.New("not implemented")}
}
