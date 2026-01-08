package claudelog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectDir(t *testing.T) {
	tests := []struct {
		cwd      string
		expected string
	}{
		{
			cwd:      "/home/noams/Data/git/tmux-dashboard",
			expected: "-home-noams-Data-git-tmux-dashboard",
		},
		{
			cwd:      "/tmp/test",
			expected: "-tmp-test",
		},
	}

	homeDir, _ := os.UserHomeDir()

	for _, tt := range tests {
		got := ProjectDir(tt.cwd)
		expectedFull := filepath.Join(homeDir, ".claude", "projects", tt.expected)
		if got != expectedFull {
			t.Errorf("ProjectDir(%q) = %q, want %q", tt.cwd, got, expectedFull)
		}
	}
}

func TestFindLatestSession(t *testing.T) {
	// Test with the actual tmux-dashboard project
	projectDir := ProjectDir("/home/noams/Data/git/tmux-dashboard")

	session, err := FindLatestSession(projectDir)
	if err != nil {
		t.Skipf("No session found (expected in CI): %v", err)
	}

	if session == "" {
		t.Error("FindLatestSession returned empty path")
	}

	if !filepath.IsAbs(session) {
		t.Errorf("FindLatestSession returned relative path: %s", session)
	}

	t.Logf("Found session: %s", session)
}

func TestReadSession(t *testing.T) {
	projectDir := ProjectDir("/home/noams/Data/git/tmux-dashboard")

	session, err := FindLatestSession(projectDir)
	if err != nil {
		t.Skipf("No session found: %v", err)
	}

	messages, err := ReadSession(session)
	if err != nil {
		t.Fatalf("ReadSession failed: %v", err)
	}

	if len(messages) == 0 {
		t.Error("ReadSession returned no messages")
	}

	t.Logf("Read %d messages from session", len(messages))

	// Check that we have both user and assistant messages
	var userCount, assistantCount int
	for _, msg := range messages {
		switch msg.Type {
		case "user":
			userCount++
		case "assistant":
			assistantCount++
		}
	}

	t.Logf("User messages: %d, Assistant messages: %d", userCount, assistantCount)

	if userCount == 0 {
		t.Error("No user messages found")
	}
	if assistantCount == 0 {
		t.Error("No assistant messages found")
	}
}

func TestGetSessionState(t *testing.T) {
	projectDir := ProjectDir("/home/noams/Data/git/tmux-dashboard")

	session, err := FindLatestSession(projectDir)
	if err != nil {
		t.Skipf("No session found: %v", err)
	}

	messages, err := ReadLastMessages(session, 50)
	if err != nil {
		t.Fatalf("ReadLastMessages failed: %v", err)
	}

	state := GetSessionState(messages)

	t.Logf("Session state:")
	t.Logf("  SessionID: %s", state.SessionID)
	t.Logf("  CWD: %s", state.CWD)
	t.Logf("  GitBranch: %s", state.GitBranch)
	t.Logf("  IsWorking: %v", state.IsWorking)
	t.Logf("  IsWaiting: %v", state.IsWaiting)
	t.Logf("  CurrentTool: %s", state.CurrentTool)
	t.Logf("  LastToolName: %s", state.LastToolName)
	t.Logf("  Todos: %d items", len(state.Todos))
	t.Logf("  Question: %s", state.Question)
	t.Logf("  Choices: %v", state.Choices)
	t.Logf("  LastActivity: %s", state.LastActivity)

	if state.CWD == "" {
		t.Error("CWD should not be empty")
	}
}

func TestGetStateForPane(t *testing.T) {
	state, err := GetStateForPane("/home/noams/Data/git/tmux-dashboard")
	if err != nil {
		t.Skipf("No session found: %v", err)
	}

	t.Logf("State for pane: %+v", state)

	if state.CWD != "/home/noams/Data/git/tmux-dashboard" {
		t.Errorf("CWD = %q, want /home/noams/Data/git/tmux-dashboard", state.CWD)
	}
}
