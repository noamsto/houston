package amp

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/noamsto/houston/parser"
)

// Thread represents an Amp thread file structure.
type Thread struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	AgentMode string    `json:"agentMode"`
	Created   int64     `json:"created"`
	Env       ThreadEnv `json:"env"`
	Messages  []Message `json:"messages"`
}

// ThreadEnv contains environment information.
type ThreadEnv struct {
	Initial InitialEnv `json:"initial"`
}

// InitialEnv contains initial workspace trees.
type InitialEnv struct {
	Trees []WorkspaceTree `json:"trees"`
}

// WorkspaceTree represents a workspace in the thread.
type WorkspaceTree struct {
	DisplayName string `json:"displayName"`
	URI         string `json:"uri"`
}

// Message represents a thread message.
type Message struct {
	Role      string        `json:"role"`
	MessageID int           `json:"messageId"`
	Content   []any `json:"content"`
	State     MessageState  `json:"state"`
	Usage     Usage         `json:"usage"`
}

// MessageState represents the state of a message.
type MessageState struct {
	Type       string `json:"type"` // "complete", "cancelled", "running"
	StopReason string `json:"stopReason"`
}

// Usage tracks token/model usage.
type Usage struct {
	Model        string `json:"model"`
	InputTokens  int    `json:"inputTokens"`
	OutputTokens int    `json:"outputTokens"`
}

// getThreadsDir returns the Amp threads directory.
func getThreadsDir() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".local", "share", "amp", "threads")
}

// getStateDir returns the Amp state directory.
func getStateDir() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".local", "state", "amp")
}

// GetStateFromFiles reads state from Amp's thread JSON files.
func GetStateFromFiles(threadsDir, stateDir, cwd string) (*parser.Result, error) {
	// Normalize cwd
	cwd = filepath.Clean(cwd)
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}

	// Find matching thread
	thread, err := findThreadForCwd(threadsDir, stateDir, cwd)
	if err != nil {
		return nil, err
	}

	return analyzeThread(thread), nil
}

// findThreadForCwd finds the thread that matches the given working directory.
func findThreadForCwd(threadsDir, stateDir, cwd string) (*Thread, error) {
	// First, try the last-thread-id if it matches our cwd
	lastThreadID, _ := readLastThreadID(stateDir)
	if lastThreadID != "" {
		thread, err := readThread(threadsDir, lastThreadID)
		if err == nil && threadMatchesCwd(thread, cwd) {
			return thread, nil
		}
	}

	// Otherwise, find most recent thread matching cwd
	entries, err := os.ReadDir(threadsDir)
	if err != nil {
		return nil, fmt.Errorf("reading threads dir: %w", err)
	}

	var matchingThreads []struct {
		thread  *Thread
		created int64
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		threadPath := filepath.Join(threadsDir, entry.Name())
		thread, err := readThreadFile(threadPath)
		if err != nil {
			continue
		}

		if threadMatchesCwd(thread, cwd) {
			matchingThreads = append(matchingThreads, struct {
				thread  *Thread
				created int64
			}{thread, thread.Created})
		}
	}

	if len(matchingThreads) == 0 {
		return nil, fmt.Errorf("no thread found for cwd: %s", cwd)
	}

	// Sort by created (newest first)
	sort.Slice(matchingThreads, func(i, j int) bool {
		return matchingThreads[i].created > matchingThreads[j].created
	})

	return matchingThreads[0].thread, nil
}

// readLastThreadID reads the last-thread-id from state directory.
func readLastThreadID(stateDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, "last-thread-id"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// readThread reads a thread by ID.
func readThread(threadsDir, threadID string) (*Thread, error) {
	threadPath := filepath.Join(threadsDir, threadID+".json")
	return readThreadFile(threadPath)
}

// readThreadFile reads and parses a thread JSON file.
func readThreadFile(path string) (*Thread, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var thread Thread
	if err := json.Unmarshal(data, &thread); err != nil {
		return nil, err
	}

	return &thread, nil
}

// threadMatchesCwd checks if thread's workspace matches the given cwd.
func threadMatchesCwd(thread *Thread, cwd string) bool {
	for _, tree := range thread.Env.Initial.Trees {
		// Parse URI and extract path
		treePath := uriToPath(tree.URI)
		if treePath == "" {
			continue
		}

		// Normalize
		treePath = filepath.Clean(treePath)
		if resolved, err := filepath.EvalSymlinks(treePath); err == nil {
			treePath = resolved
		}

		// Check exact match or if cwd is under the workspace
		if treePath == cwd || strings.HasPrefix(cwd, treePath+"/") {
			return true
		}
	}
	return false
}

// uriToPath converts a file:// URI to a path.
func uriToPath(uri string) string {
	if !strings.HasPrefix(uri, "file://") {
		return ""
	}

	parsed, err := url.Parse(uri)
	if err != nil {
		return ""
	}

	return parsed.Path
}

// analyzeThread analyzes thread messages to determine current state.
func analyzeThread(thread *Thread) *parser.Result {
	if len(thread.Messages) == 0 {
		return &parser.Result{Type: parser.TypeIdle}
	}

	lastMsg := thread.Messages[len(thread.Messages)-1]

	switch lastMsg.State.Type {
	case "running":
		return &parser.Result{
			Type:     parser.TypeWorking,
			Activity: "Working",
		}
	case "cancelled":
		return &parser.Result{
			Type:     parser.TypeIdle,
			Activity: "Cancelled",
		}
	case "complete":
		if lastMsg.State.StopReason == "tool_use" {
			return &parser.Result{
				Type:     parser.TypeWorking,
				Activity: "Running tool",
			}
		}
		if lastMsg.Role == "user" {
			return &parser.Result{
				Type:     parser.TypeWorking,
				Activity: "Processing",
			}
		}
		// Check if the last assistant message is waiting for input
		if isWaitingForInput(lastMsg) {
			return &parser.Result{
				Type: parser.TypeQuestion,
			}
		}
		return &parser.Result{Type: parser.TypeIdle}
	default:
		return &parser.Result{Type: parser.TypeIdle}
	}
}

// isWaitingForInput checks if the message indicates waiting for user input.
func isWaitingForInput(msg Message) bool {
	// Check if the message was recently completed and is an assistant turn
	if msg.Role != "assistant" {
		return false
	}

	// Check message age - if completed recently, might be waiting
	// This is a heuristic; actual detection may need more context
	msgTime := time.UnixMilli(0) // Would need timestamp from message
	return time.Since(msgTime) < 5*time.Minute
}
