package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultRelativeDBPath = ".claude/claude-manager/orchestrator.db"

func DefaultDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, defaultRelativeDBPath), nil
}

func ExpandPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}

	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if path == "~" {
			path = home
		} else if strings.HasPrefix(path, "~/") {
			path = filepath.Join(home, path[2:])
		}
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}
	return abs, nil
}
