package ai

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// SummaryProvider controls which API is used for summary generation.
type SummaryProvider string

const (
	ProviderAnthropic        SummaryProvider = "anthropic"
	ProviderOpenAICompatible SummaryProvider = "openai-compatible"
	ProviderNone             SummaryProvider = "none"
)

// AnthropicConfig holds resolved Anthropic API settings.
type AnthropicConfig struct {
	APIKey string
	Model  string
}

// OpenAIConfig holds resolved OpenAI API settings.
type OpenAIConfig struct {
	APIKey  string
	Model   string
	BaseURL string // custom base URL for OpenAI-compatible endpoints
}

// OpenAICompatibleConfig holds settings for OpenAI-compatible summary provider.
type OpenAICompatibleConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

// Settings represents the claude-manager-specific settings stored in
// ~/.claude/claude-manager-settings.json.
type Settings struct {
	SummaryProvider    SummaryProvider `json:"summaryProvider,omitempty"`    // "anthropic", "openai-compatible", "none"
	AnthropicAPIKey    string          `json:"anthropicApiKey,omitempty"`    // overrides ANTHROPIC_API_KEY env
	OpenAIAPIKey       string          `json:"openaiApiKey,omitempty"`       // overrides OPENAI_API_KEY env
	CompatibleBaseURL  string          `json:"compatibleBaseUrl,omitempty"`  // e.g. https://api.moonshot.cn/v1
	CompatibleAPIKey   string          `json:"compatibleApiKey,omitempty"`   // API key for compatible endpoint
	CompatibleModel    string          `json:"compatibleModel,omitempty"`    // model name for compatible endpoint
	EmbeddingModel     string          `json:"embeddingModel,omitempty"`     // override embedding model name
}

func settingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "claude-manager-settings.json")
}

// LoadSettings reads settings from disk.
func LoadSettings() Settings {
	data, err := os.ReadFile(settingsPath())
	if err != nil {
		return Settings{}
	}
	var s Settings
	json.Unmarshal(data, &s)
	return s
}

// SaveSettings writes settings to disk.
func SaveSettings(s Settings) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(settingsPath()), 0755)
	return os.WriteFile(settingsPath(), data, 0644)
}

// ResolveAnthropic returns config or nil if no API key is found.
// Resolution order: settings.anthropicApiKey, ANTHROPIC_API_KEY env,
// ~/.claude/settings.local.json apiKey, ~/.claude/settings.json apiKey.
func ResolveAnthropic() *AnthropicConfig {
	s := LoadSettings()
	if s.AnthropicAPIKey != "" {
		return &AnthropicConfig{APIKey: s.AnthropicAPIKey, Model: "claude-haiku-4-5-20251001"}
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		return &AnthropicConfig{APIKey: key, Model: "claude-haiku-4-5-20251001"}
	}
	if key := readSettingsKey("settings.local.json", "apiKey"); key != "" {
		return &AnthropicConfig{APIKey: key, Model: "claude-haiku-4-5-20251001"}
	}
	if key := readSettingsKey("settings.json", "apiKey"); key != "" {
		return &AnthropicConfig{APIKey: key, Model: "claude-haiku-4-5-20251001"}
	}
	return nil
}

// ResolveOpenAI returns config or nil if no API key is found.
// Falls back to the OpenAI-compatible endpoint if no direct OpenAI key is set.
func ResolveOpenAI() *OpenAIConfig {
	s := LoadSettings()
	if s.OpenAIAPIKey != "" {
		cfg := &OpenAIConfig{APIKey: s.OpenAIAPIKey, Model: "text-embedding-3-small"}
		if s.EmbeddingModel != "" {
			cfg.Model = s.EmbeddingModel
		}
		return cfg
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		return &OpenAIConfig{APIKey: key, Model: "text-embedding-3-small"}
	}
	// Fall back to compatible endpoint
	if s.CompatibleBaseURL != "" && s.CompatibleAPIKey != "" {
		model := "text-embedding-3-small"
		if s.EmbeddingModel != "" {
			model = s.EmbeddingModel
		}
		return &OpenAIConfig{
			APIKey:  s.CompatibleAPIKey,
			Model:   model,
			BaseURL: s.CompatibleBaseURL,
		}
	}
	return nil
}

// ResolveOpenAICompatible returns config for OpenAI-compatible summary provider, or nil.
func ResolveOpenAICompatible() *OpenAICompatibleConfig {
	s := LoadSettings()
	if s.CompatibleBaseURL == "" || s.CompatibleAPIKey == "" {
		return nil
	}
	model := s.CompatibleModel
	if model == "" {
		model = "gpt-4o-mini"
	}
	return &OpenAICompatibleConfig{
		APIKey:  s.CompatibleAPIKey,
		Model:   model,
		BaseURL: s.CompatibleBaseURL,
	}
}

// ResolveSummaryProvider determines which summary provider to use.
func ResolveSummaryProvider() SummaryProvider {
	s := LoadSettings()
	if s.SummaryProvider != "" {
		return s.SummaryProvider
	}
	// Auto-detect: prefer anthropic if key exists, then compatible, then none
	if ResolveAnthropic() != nil {
		return ProviderAnthropic
	}
	if ResolveOpenAICompatible() != nil {
		return ProviderOpenAICompatible
	}
	return ProviderNone
}

func readSettingsKey(filename, key string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", filename))
	if err != nil {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	raw, ok := m[key]
	if !ok {
		return ""
	}
	var val string
	if err := json.Unmarshal(raw, &val); err != nil {
		return ""
	}
	return val
}
