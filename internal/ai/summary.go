package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SummaryCache manages LLM-generated session summaries with a JSON file cache.
type SummaryCache struct {
	mu       sync.Mutex
	path     string
	entries  map[string]cacheEntry
	loaded   bool
	inFlight map[string]struct{}
}

type cacheEntry struct {
	Title       string `json:"title"`
	GeneratedAt int64  `json:"generatedAt"`
}

func NewSummaryCache() *SummaryCache {
	home, _ := os.UserHomeDir()
	return &SummaryCache{
		path:     filepath.Join(home, ".claude", "claude-manager-titles.json"),
		entries:  make(map[string]cacheEntry),
		inFlight: make(map[string]struct{}),
	}
}

func (c *SummaryCache) load() {
	if c.loaded {
		return
	}
	c.loaded = true
	data, err := os.ReadFile(c.path)
	if err != nil {
		return
	}
	json.Unmarshal(data, &c.entries)
}

func (c *SummaryCache) save() {
	data, err := json.Marshal(c.entries)
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(c.path), 0755)
	os.WriteFile(c.path, data, 0644)
}

// Get returns a cached summary or "".
func (c *SummaryCache) Get(sessionID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	if e, ok := c.entries[sessionID+":summary"]; ok {
		return e.Title
	}
	return ""
}

// Generate calls the configured LLM API to generate a summary.
// context is the pre-built numbered message list from BuildSummaryContext.
// Returns the summary string, or "" on failure.
func (c *SummaryCache) Generate(sessionID, context string) (string, error) {
	// Check cache first
	c.mu.Lock()
	c.load()
	if e, ok := c.entries[sessionID+":summary"]; ok {
		c.mu.Unlock()
		return e.Title, nil
	}
	// In-flight dedup
	if _, ok := c.inFlight[sessionID]; ok {
		c.mu.Unlock()
		return "", nil
	}
	c.inFlight[sessionID] = struct{}{}
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.inFlight, sessionID)
		c.mu.Unlock()
	}()

	prompt := fmt.Sprintf(
		"The following are the user's messages throughout a Claude Code session (an AI coding assistant):\n\n%s\nWrite a 1-2 sentence summary of what was worked on. Be very concise and direct. Start with a verb (e.g. \"Fixed...\", \"Added...\", \"Refactored...\"). Do not start with \"The session\" or \"The user\". Reply with only the summary.",
		context,
	)

	var summary string
	var err error

	provider := ResolveSummaryProvider()
	switch provider {
	case ProviderAnthropic:
		config := ResolveAnthropic()
		if config == nil {
			return "", fmt.Errorf("no anthropic config")
		}
		summary, err = callAnthropic(config, prompt, 80)
	case ProviderOpenAICompatible:
		config := ResolveOpenAICompatible()
		if config == nil {
			return "", fmt.Errorf("no openai-compatible config")
		}
		summary, err = callOpenAIChat(config, prompt, 80)
	default:
		return "", fmt.Errorf("no summary provider configured")
	}

	if err != nil {
		return "", err
	}

	// Cache the result
	c.mu.Lock()
	c.entries[sessionID+":summary"] = cacheEntry{
		Title:       summary,
		GeneratedAt: time.Now().Unix(),
	}
	c.save()
	c.mu.Unlock()

	return summary, nil
}

// ClearAll removes all cached summaries.
func (c *SummaryCache) ClearAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]cacheEntry)
	c.save()
}

// SummaryAvailable returns true if any summary provider is configured.
func SummaryAvailable() bool {
	return ResolveSummaryProvider() != ProviderNone
}

// anthropic API types
type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func callAnthropic(config *AnthropicConfig, prompt string, maxTokens int) (string, error) {
	reqBody := anthropicRequest{
		Model:     config.Model,
		MaxTokens: maxTokens,
		Messages:  []anthropicMessage{{Role: "user", Content: prompt}},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("x-api-key", config.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var result anthropicResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", err
	}
	if result.Error != nil {
		return "", fmt.Errorf("anthropic: %s", result.Error.Message)
	}
	if len(result.Content) == 0 {
		return "", fmt.Errorf("anthropic: empty response")
	}

	text := strings.TrimSpace(result.Content[0].Text)
	// Strip surrounding quotes if present
	if len(text) > 1 && text[0] == '"' && text[len(text)-1] == '"' {
		text = text[1 : len(text)-1]
	}
	if len(text) > 300 {
		text = text[:300]
	}
	return text, nil
}

// OpenAI-compatible chat completion types
type openAIChatRequest struct {
	Model     string              `json:"model"`
	MaxTokens int                 `json:"max_tokens"`
	Messages  []openAIChatMessage `json:"messages"`
}

type openAIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func callOpenAIChat(config *OpenAICompatibleConfig, prompt string, maxTokens int) (string, error) {
	reqBody := openAIChatRequest{
		Model:     config.Model,
		MaxTokens: maxTokens,
		Messages:  []openAIChatMessage{{Role: "user", Content: prompt}},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	baseURL := strings.TrimRight(config.BaseURL, "/")
	req, err := http.NewRequest("POST", baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var result openAIChatResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", err
	}
	if result.Error != nil {
		return "", fmt.Errorf("openai-compatible: %s", result.Error.Message)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("openai-compatible: empty response")
	}

	text := strings.TrimSpace(result.Choices[0].Message.Content)
	if len(text) > 1 && text[0] == '"' && text[len(text)-1] == '"' {
		text = text[1 : len(text)-1]
	}
	if len(text) > 300 {
		text = text[:300]
	}
	return text, nil
}
