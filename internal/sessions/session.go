package sessions

import (
	"fmt"
	"time"
)

// Session represents a parsed Claude Code session.
type Session struct {
	ID           string
	Project      string    // Human-readable project name (decoded from directory)
	ProjectPath  string    // Actual filesystem path (cwd from session data)
	Summary      string    // From summary line, or first user message as fallback
	GitBranch    string    // Git branch at time of session
	LastActive   time.Time // Timestamp of last message
	MessageCount int       // Total user + assistant messages
	FilePath     string    // Path to the .jsonl file
}

// TimeAgo returns a human-readable relative time string.
func (s Session) TimeAgo() string {
	d := time.Since(s.LastActive)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		return fmt.Sprintf("%dm ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		return fmt.Sprintf("%dh ago", h)
	case d < 30*24*time.Hour:
		days := int(d.Hours() / 24)
		return fmt.Sprintf("%dd ago", days)
	default:
		months := int(d.Hours() / 24 / 30)
		return fmt.Sprintf("%dmo ago", months)
	}
}
