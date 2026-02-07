package claude

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SuggestionCache provides mtime-based caching for prompt suggestions
// from Claude Code's prompt_suggestion subagent JSONL files.
type SuggestionCache struct {
	mu          sync.Mutex
	suggestion  string
	lastMtime   time.Time
	lastChecked time.Time
	lastFile    string
}

// GetCachedSuggestion returns the latest prompt suggestion for a cwd.
// It rate-limits filesystem scanning to every 3 seconds and only re-reads
// the JSONL file when its mtime changes.
func (c *SuggestionCache) GetCachedSuggestion(cwd string) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if now.Sub(c.lastChecked) < 3*time.Second {
		return c.suggestion
	}
	c.lastChecked = now

	projectDir := ProjectDir(cwd)
	path, mtime := findLatestSuggestionFile(projectDir)
	if path == "" {
		return ""
	}

	// Only re-read if file changed
	if path == c.lastFile && mtime.Equal(c.lastMtime) {
		return c.suggestion
	}

	c.lastFile = path
	c.lastMtime = mtime
	c.suggestion = extractSuggestionText(path)
	return c.suggestion
}

// findLatestSuggestionFile scans all session subagents dirs in a project
// and returns the path and mtime of the newest prompt suggestion file.
func findLatestSuggestionFile(projectDir string) (string, time.Time) {
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return "", time.Time{}
	}

	var bestPath string
	var bestMtime time.Time

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		subagentsDir := filepath.Join(projectDir, e.Name(), "subagents")
		subs, err := os.ReadDir(subagentsDir)
		if err != nil {
			continue
		}
		for _, s := range subs {
			if s.IsDir() {
				continue
			}
			name := s.Name()
			if !strings.HasPrefix(name, "agent-aprompt_suggestion-") || !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			info, err := s.Info()
			if err != nil {
				continue
			}
			if info.ModTime().After(bestMtime) {
				bestMtime = info.ModTime()
				bestPath = filepath.Join(subagentsDir, name)
			}
		}
	}

	return bestPath, bestMtime
}

// extractSuggestionText reads the last assistant text block from a subagent JSONL.
func extractSuggestionText(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var lastAssistantLine string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if len(line) == 0 {
			continue
		}
		// Quick check before parsing JSON
		if !strings.Contains(line, `"assistant"`) {
			continue
		}

		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.Type == "assistant" || entry.Message.Role == "assistant" {
			lastAssistantLine = line
		}
	}

	if lastAssistantLine == "" {
		return ""
	}

	// Re-parse the last assistant line to extract text
	var entry struct {
		Message struct {
			Content any `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(lastAssistantLine), &entry); err != nil {
		return ""
	}

	return extractTextFromContent(entry.Message.Content)
}

// extractTextFromContent extracts the text from message content.
// Content may be a string or an array of blocks.
func extractTextFromContent(content any) string {
	if content == nil {
		return ""
	}

	// Simple string content
	if s, ok := content.(string); ok {
		return strings.TrimSpace(s)
	}

	// Array of blocks
	arr, ok := content.([]any)
	if !ok {
		return ""
	}

	// Find the last text block (skip thinking blocks)
	var lastText string
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		blockType, _ := m["type"].(string)
		if blockType == "text" {
			if text, ok := m["text"].(string); ok {
				lastText = text
			}
		}
	}

	return strings.TrimSpace(lastText)
}
