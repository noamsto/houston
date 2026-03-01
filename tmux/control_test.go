package tmux

import "testing"

func TestParseControlLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want ControlEvent
	}{
		{
			"pane output",
			`%output %42 hello\015\012`,
			ControlEvent{Type: EventOutput, PaneID: "%42", Data: "hello\r\n"},
		},
		{
			"begin block",
			"%begin 1578920019 258 0",
			ControlEvent{Type: EventBegin, CmdNumber: 258},
		},
		{
			"end block",
			"%end 1578920019 258 0",
			ControlEvent{Type: EventEnd, CmdNumber: 258},
		},
		{
			"error block",
			"%error 1578920019 258 0",
			ControlEvent{Type: EventError, CmdNumber: 258},
		},
		{
			"window renamed",
			"%window-renamed @1 vim",
			ControlEvent{Type: EventWindowRenamed, WindowID: "@1", Data: "vim"},
		},
		{
			"sessions changed",
			"%sessions-changed",
			ControlEvent{Type: EventSessionsChanged},
		},
		{
			"session changed",
			"%session-changed $1 mysession",
			ControlEvent{Type: EventSessionChanged, Data: "mysession"},
		},
		{
			"window add",
			"%window-add @5",
			ControlEvent{Type: EventWindowAdd, WindowID: "@5"},
		},
		{
			"window close",
			"%window-close @5",
			ControlEvent{Type: EventWindowClose, WindowID: "@5"},
		},
		{
			"pane mode changed",
			"%pane-mode-changed %3",
			ControlEvent{Type: EventPaneModeChanged, PaneID: "%3"},
		},
		{
			"unknown line",
			"some random output",
			ControlEvent{Type: EventData, Data: "some random output"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseControlLine(tt.line)
			if got.Type != tt.want.Type {
				t.Errorf("Type = %v, want %v", got.Type, tt.want.Type)
			}
			if got.PaneID != tt.want.PaneID {
				t.Errorf("PaneID = %q, want %q", got.PaneID, tt.want.PaneID)
			}
			if got.WindowID != tt.want.WindowID {
				t.Errorf("WindowID = %q, want %q", got.WindowID, tt.want.WindowID)
			}
			if got.Data != tt.want.Data {
				t.Errorf("Data = %q, want %q", got.Data, tt.want.Data)
			}
			if got.CmdNumber != tt.want.CmdNumber {
				t.Errorf("CmdNumber = %d, want %d", got.CmdNumber, tt.want.CmdNumber)
			}
		})
	}
}
