package server

import (
	"github.com/noamsto/houston/tmux"
)

// tmuxOps is the slice of *tmux.Client the pane WebSocket needs. Narrowing it
// to an interface keeps the pane plumbing away from the real tmux binary.
type tmuxOps interface {
	GetPaneID(p tmux.Pane) (string, error)
	WindowPaneCount(p tmux.Pane) (int, error)
	IsZoomed(p tmux.Pane) (bool, error)
	ZoomPane(p tmux.Pane) error
	GetPaneSize(p tmux.Pane) (width, height int, err error)
	CapturePane(p tmux.Pane, lines int) (string, error)
	CapturePaneWithMode(p tmux.Pane, lines int) (tmux.CaptureResult, error)
	ForceRedraw(p tmux.Pane) error
	ListPanes(session string, window int) ([]tmux.PaneInfo, error)
	ListWindows(session string) ([]tmux.Window, error)
}

// paneSub is one subscriber's handle on a pane's output stream.
type paneSub interface {
	C() <-chan tmux.PaneEvent
}

// controlClientOps is the control-mode surface the pane WebSocket drives.
type controlClientOps interface {
	RunCommand(command string) (string, error)
	Subscribe(paneID string) paneSub
	Unsubscribe(paneID string, s paneSub)
	AckReseed(s paneSub)
	MarkPendingReseed(s paneSub)
	Done() <-chan struct{}
	SendKeys(paneID, text string) error
}

// controlManagerOps hands out ref-counted control clients per session.
type controlManagerOps interface {
	GetClient(session string) (controlClientOps, error)
	ReleaseClient(session string)
}

// wsWriter is the write half of a WebSocket connection. *websocket.Conn
// satisfies it directly.
type wsWriter interface {
	WriteMessage(messageType int, data []byte) error
}

// controlClientAdapter widens *tmux.ControlClient's *PaneSub parameters and
// results to the paneSub interface, which Go's exact-signature matching will
// not do on its own.
type controlClientAdapter struct {
	cc *tmux.ControlClient
}

func (a controlClientAdapter) RunCommand(command string) (string, error) {
	return a.cc.RunCommand(command)
}

func (a controlClientAdapter) Subscribe(paneID string) paneSub {
	return a.cc.Subscribe(paneID)
}

// The paneSub assertions below are safe: this adapter is the only source of
// paneSub values in a pane's call chain, and it only ever yields the real
// *tmux.PaneSub it created in Subscribe.
func (a controlClientAdapter) Unsubscribe(paneID string, s paneSub) {
	a.cc.Unsubscribe(paneID, s.(*tmux.PaneSub))
}

func (a controlClientAdapter) AckReseed(s paneSub) {
	a.cc.AckReseed(s.(*tmux.PaneSub))
}

func (a controlClientAdapter) MarkPendingReseed(s paneSub) {
	a.cc.MarkPendingReseed(s.(*tmux.PaneSub))
}

func (a controlClientAdapter) Done() <-chan struct{} {
	return a.cc.Done()
}

func (a controlClientAdapter) SendKeys(paneID, text string) error {
	return a.cc.SendKeys(paneID, text)
}

// controlManagerAdapter wraps *tmux.ControlManager so GetClient returns the
// controlClientOps interface instead of the concrete *tmux.ControlClient.
type controlManagerAdapter struct {
	mgr *tmux.ControlManager
}

func (a controlManagerAdapter) GetClient(session string) (controlClientOps, error) {
	cc, err := a.mgr.GetClient(session)
	if err != nil {
		return nil, err
	}
	return controlClientAdapter{cc: cc}, nil
}

func (a controlManagerAdapter) ReleaseClient(session string) {
	a.mgr.ReleaseClient(session)
}
