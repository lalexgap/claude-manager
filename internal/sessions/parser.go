package sessions

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// jsonlEntry represents a single line in a session JSONL file.
type jsonlEntry struct {
	Type      string          `json:"type"`
	SessionID string          `json:"sessionId"`
	CWD       string          `json:"cwd"`
	GitBranch string          `json:"gitBranch"`
	Timestamp string          `json:"timestamp"`
	IsMeta    bool            `json:"isMeta"`
	Summary   string          `json:"summary"`
	Message   json.RawMessage `json:"message"`
}

type messageContent struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// contentBlock represents a structured content block (text, tool_use, etc.)
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// claudeDir returns the path to ~/.claude/projects/
func claudeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// decodeProjectName converts an encoded directory name back to a readable name.
// e.g., "-Users-lagap-code-producthunt" -> "producthunt"
func decodeProjectName(dirName string) string {
	// The directory name is the path with / replaced by -
	// We want just the last meaningful segment as the project name
	parts := strings.Split(dirName, "-")
	// Filter out empty parts and common path segments
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return dirName
}

// LoadAll discovers and parses all session files.
func LoadAll() ([]Session, error) {
	dir, err := claudeDir()
	if err != nil {
		return nil, err
	}

	projectDirs, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var sessions []Session
	for _, pd := range projectDirs {
		if !pd.IsDir() {
			continue
		}
		projectDir := filepath.Join(dir, pd.Name())
		projectName := decodeProjectName(pd.Name())

		files, err := filepath.Glob(filepath.Join(projectDir, "*.jsonl"))
		if err != nil {
			continue
		}

		for _, f := range files {
			s, err := parseSessionFile(f, projectName)
			if err != nil || s == nil {
				continue
			}
			sessions = append(sessions, *s)
		}
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastActive.After(sessions[j].LastActive)
	})

	return sessions, nil
}

// parseSessionFile parses a single .jsonl file into a Session.
func parseSessionFile(path string, projectName string) (*Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	s := &Session{
		FilePath: path,
		Project:  projectName,
	}

	var firstUserMessage string
	var lastTimestamp time.Time

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // 10MB max line

	for scanner.Scan() {
		var entry jsonlEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}

		// Extract summary
		if entry.Type == "summary" && entry.Summary != "" {
			s.Summary = entry.Summary
		}

		// Extract session metadata from user/assistant messages
		if entry.Type == "user" || entry.Type == "assistant" {
			if entry.SessionID != "" && s.ID == "" {
				s.ID = entry.SessionID
			}
			if entry.CWD != "" {
				s.ProjectPath = entry.CWD
			}
			if entry.GitBranch != "" {
				s.GitBranch = entry.GitBranch
			}
			if entry.Timestamp != "" {
				if t, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
					if t.After(lastTimestamp) {
						lastTimestamp = t
					}
				}
			}

			if !entry.IsMeta {
				s.MessageCount++
			}

			// Capture first real user message as fallback summary
			if entry.Type == "user" && !entry.IsMeta && firstUserMessage == "" {
				firstUserMessage = extractTextContent(entry.Message)
			}
		}
	}

	// Skip sessions with no messages
	if s.ID == "" || s.MessageCount == 0 {
		return nil, nil
	}

	s.LastActive = lastTimestamp

	if s.Summary == "" {
		s.Summary = firstUserMessage
	}

	// Clean up summary: collapse whitespace, remove newlines
	s.Summary = strings.Join(strings.Fields(s.Summary), " ")

	// Truncate long summaries
	if len(s.Summary) > 200 {
		s.Summary = s.Summary[:197] + "..."
	}

	return s, nil
}

// extractTextContent gets the text from a message content field.
func extractTextContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var msg messageContent
	if err := json.Unmarshal(raw, &msg); err != nil {
		return ""
	}

	// Try string content first
	var str string
	if err := json.Unmarshal(msg.Content, &str); err == nil {
		// Skip command/meta messages
		if strings.HasPrefix(str, "<") {
			return ""
		}
		return strings.TrimSpace(str)
	}

	// Try array of content blocks
	var blocks []contentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err == nil {
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				text := strings.TrimSpace(b.Text)
				if !strings.HasPrefix(text, "<") {
					return text
				}
			}
		}
	}

	return ""
}
