package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

var ErrGatewayConfigNotFound = errors.New("main environment gateway config not found")

type GatewayConfig struct {
	Workdir               string          `json:"workdir"`
	DefaultTimeoutSeconds int             `json:"default_timeout_seconds"`
	Commands              GatewayCommands `json:"commands"`
}

type GatewayCommands struct {
	RunFeatureSpecs []string          `json:"run_feature_specs"`
	DevServer       DevServerCommands `json:"dev_server"`
}

type DevServerCommands struct {
	Start   []string `json:"start"`
	Stop    []string `json:"stop"`
	Restart []string `json:"restart"`
	Status  []string `json:"status"`
	Logs    []string `json:"logs"`
}

type GatewayResult struct {
	Success     bool     `json:"success"`
	RequestID   int64    `json:"request_id"`
	AgentName   string   `json:"agent_name"`
	RequestType string   `json:"request_type"`
	Action      string   `json:"action,omitempty"`
	Command     []string `json:"command,omitempty"`
	Workdir     string   `json:"workdir,omitempty"`
	ExitCode    int      `json:"exit_code"`
	Stdout      string   `json:"stdout,omitempty"`
	Stderr      string   `json:"stderr,omitempty"`
	DurationMs  int64    `json:"duration_ms"`
	Error       string   `json:"error,omitempty"`
}

type devServerPayload struct {
	Action string `json:"action"`
}

func LoadGatewayConfig(path string) (GatewayConfig, error) {
	cfg := GatewayConfig{}

	if strings.TrimSpace(path) == "" {
		defaultPath, err := DefaultGatewayConfigPath()
		if err != nil {
			return cfg, err
		}
		path = defaultPath
	} else {
		expanded, err := ExpandPath(path)
		if err != nil {
			return cfg, err
		}
		path = expanded
	}

	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, fmt.Errorf("%w: %s", ErrGatewayConfigNotFound, path)
		}
		return cfg, fmt.Errorf("read gateway config: %w", err)
	}

	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse gateway config %s: %w", path, err)
	}

	if cfg.Workdir != "" {
		expanded, err := ExpandPath(cfg.Workdir)
		if err != nil {
			return cfg, fmt.Errorf("invalid workdir: %w", err)
		}
		cfg.Workdir = expanded
	}

	if cfg.DefaultTimeoutSeconds <= 0 {
		cfg.DefaultTimeoutSeconds = 600
	}

	return cfg, nil
}

func ExecuteMainEnvRequest(req MainEnvRequest, cfg GatewayConfig) GatewayResult {
	result := GatewayResult{
		RequestID:   req.ID,
		AgentName:   req.AgentName,
		RequestType: req.RequestType,
		Workdir:     cfg.Workdir,
		ExitCode:    -1,
	}

	command, action, err := commandForRequest(req, cfg)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Action = action
	result.Command = command

	timeout := time.Duration(cfg.DefaultTimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	if cfg.Workdir != "" {
		cmd.Dir = cfg.Workdir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err = cmd.Run()
	result.DurationMs = time.Since(start).Milliseconds()
	result.Stdout = strings.TrimSpace(stdout.String())
	result.Stderr = strings.TrimSpace(stderr.String())

	if err == nil {
		result.Success = true
		result.ExitCode = 0
		return result
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.Error = fmt.Sprintf("command timed out after %s", timeout)
		return result
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		result.Error = err.Error()
		return result
	}

	result.Error = err.Error()
	return result
}

func commandForRequest(req MainEnvRequest, cfg GatewayConfig) ([]string, string, error) {
	switch req.RequestType {
	case "run_feature_specs":
		if len(cfg.Commands.RunFeatureSpecs) == 0 {
			return nil, "", fmt.Errorf("run_feature_specs command is not configured")
		}
		return cfg.Commands.RunFeatureSpecs, "run_feature_specs", nil

	case "dev_server":
		action := ""
		if strings.TrimSpace(req.PayloadJSON) != "" {
			var payload devServerPayload
			if err := json.Unmarshal([]byte(req.PayloadJSON), &payload); err != nil {
				return nil, "", fmt.Errorf("invalid dev_server payload json: %w", err)
			}
			action = strings.TrimSpace(payload.Action)
		}
		if action == "" {
			action = "status"
		}

		switch action {
		case "start":
			if len(cfg.Commands.DevServer.Start) == 0 {
				return nil, "", fmt.Errorf("dev_server.start command is not configured")
			}
			return cfg.Commands.DevServer.Start, action, nil
		case "stop":
			if len(cfg.Commands.DevServer.Stop) == 0 {
				return nil, "", fmt.Errorf("dev_server.stop command is not configured")
			}
			return cfg.Commands.DevServer.Stop, action, nil
		case "restart":
			if len(cfg.Commands.DevServer.Restart) == 0 {
				return nil, "", fmt.Errorf("dev_server.restart command is not configured")
			}
			return cfg.Commands.DevServer.Restart, action, nil
		case "status":
			if len(cfg.Commands.DevServer.Status) == 0 {
				return nil, "", fmt.Errorf("dev_server.status command is not configured")
			}
			return cfg.Commands.DevServer.Status, action, nil
		case "logs":
			if len(cfg.Commands.DevServer.Logs) == 0 {
				return nil, "", fmt.Errorf("dev_server.logs command is not configured")
			}
			return cfg.Commands.DevServer.Logs, action, nil
		default:
			return nil, "", fmt.Errorf("unsupported dev_server action %q", action)
		}
	default:
		return nil, "", fmt.Errorf("unsupported request type %q", req.RequestType)
	}
}
