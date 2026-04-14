package ai

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"claude-manager/internal/sessions"

	_ "modernc.org/sqlite"
)

// SearchResult pairs a session with its cosine similarity score.
type SearchResult struct {
	Session sessions.Session
	Score   float64
}

// EmbeddingStore manages vector embeddings in SQLite.
type EmbeddingStore struct {
	mu       sync.Mutex
	db       *sql.DB
	inFlight map[string]struct{}
}

// NewEmbeddingStore opens or creates the embedding database.
func NewEmbeddingStore() (*EmbeddingStore, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dbPath := filepath.Join(home, ".claude", "claude-manager-embeddings.db")
	os.MkdirAll(filepath.Dir(dbPath), 0755)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	// Enable WAL mode for better concurrent access
	db.Exec("PRAGMA journal_mode=WAL")

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS embeddings (
		session_id   TEXT PRIMARY KEY,
		embedding    BLOB NOT NULL,
		content_hash TEXT NOT NULL,
		embedded_at  INTEGER NOT NULL
	)`)
	if err != nil {
		db.Close()
		return nil, err
	}

	return &EmbeddingStore{
		db:       db,
		inFlight: make(map[string]struct{}),
	}, nil
}

// ClearAll removes all stored embeddings.
func (s *EmbeddingStore) ClearAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db.Exec("DELETE FROM embeddings")
}

// Close closes the database.
func (s *EmbeddingStore) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// BuildEmbedText constructs the text to embed for a session.
func BuildEmbedText(sess sessions.Session) string {
	parts := []string{}
	add := func(s string, maxLen int) {
		if s == "" {
			return
		}
		if len(s) > maxLen {
			s = s[:maxLen]
		}
		parts = append(parts, s)
	}

	add(sess.LLMSummary, 300)
	add(sess.Project, 100)
	add(sess.FirstMessage, 400)
	if len(sess.LastMessages) > 0 {
		add(sess.LastMessages[len(sess.LastMessages)-1], 200)
	}
	add(sess.Summary, 300)

	result := ""
	for i, p := range parts {
		if i > 0 {
			result += " | "
		}
		result += p
	}
	return result
}

// shortHash produces a simple 31-bit hash string for staleness detection.
// Matches claudesole's algorithm.
func shortHash(text string) string {
	var h int32
	for _, c := range text {
		h = 31*h + c
	}
	// Convert to unsigned 32-bit then hex
	return fmt.Sprintf("%x", uint32(h))
}

// EnsureEmbeddings indexes any sessions whose content has changed.
// Calls OpenAI API in batches of 50.
func (s *EmbeddingStore) EnsureEmbeddings(config *OpenAIConfig, allSessions []sessions.Session) (int, error) {
	if config == nil {
		return 0, fmt.Errorf("no openai config")
	}

	// Find stale sessions
	type pending struct {
		sess sessions.Session
		text string
		hash string
	}
	var stale []pending

	for _, sess := range allSessions {
		text := BuildEmbedText(sess)
		if text == "" {
			continue
		}
		hash := shortHash(text)

		// Check if already indexed with same hash
		var storedHash string
		err := s.db.QueryRow("SELECT content_hash FROM embeddings WHERE session_id = ?", sess.ID).Scan(&storedHash)
		if err == nil && storedHash == hash {
			continue
		}

		s.mu.Lock()
		if _, ok := s.inFlight[sess.ID]; ok {
			s.mu.Unlock()
			continue
		}
		s.inFlight[sess.ID] = struct{}{}
		s.mu.Unlock()

		stale = append(stale, pending{sess: sess, text: text, hash: hash})
	}

	if len(stale) == 0 {
		return 0, nil
	}

	indexed := 0
	// Process in batches of 50
	for i := 0; i < len(stale); i += 50 {
		end := i + 50
		if end > len(stale) {
			end = len(stale)
		}
		batch := stale[i:end]

		texts := make([]string, len(batch))
		for j, p := range batch {
			texts[j] = p.text
		}

		embeddings, err := callOpenAIEmbed(config, texts)
		if err != nil {
			// Clean up in-flight
			s.mu.Lock()
			for _, p := range stale[i:] {
				delete(s.inFlight, p.sess.ID)
			}
			s.mu.Unlock()
			return indexed, err
		}

		for j, emb := range embeddings {
			blob := float32ToBlob(emb)
			_, err := s.db.Exec(
				`INSERT OR REPLACE INTO embeddings (session_id, embedding, content_hash, embedded_at) VALUES (?, ?, ?, ?)`,
				batch[j].sess.ID, blob, batch[j].hash, time.Now().Unix(),
			)
			if err == nil {
				indexed++
			}
			s.mu.Lock()
			delete(s.inFlight, batch[j].sess.ID)
			s.mu.Unlock()
		}
	}

	return indexed, nil
}

// SemanticSearch embeds the query and returns top-K sessions by cosine similarity.
func (s *EmbeddingStore) SemanticSearch(config *OpenAIConfig, query string, allSessions []sessions.Session, topK int) ([]SearchResult, error) {
	if config == nil {
		return nil, fmt.Errorf("no openai config")
	}

	// Embed the query
	embeddings, err := callOpenAIEmbed(config, []string{query})
	if err != nil {
		return nil, err
	}
	if len(embeddings) == 0 {
		return nil, fmt.Errorf("empty embedding response")
	}
	queryVec := embeddings[0]

	// Build session ID -> session map
	sessMap := make(map[string]sessions.Session)
	for _, sess := range allSessions {
		sessMap[sess.ID] = sess
	}

	// Load all stored embeddings
	rows, err := s.db.Query("SELECT session_id, embedding FROM embeddings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var id string
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			continue
		}
		sess, ok := sessMap[id]
		if !ok {
			continue
		}
		stored := blobToFloat32(blob)
		if len(stored) != len(queryVec) {
			continue
		}
		score := cosineSimilarity(queryVec, stored)
		results = append(results, SearchResult{Session: sess, Score: score})
	}

	// Sort by score descending
	for i := 0; i < len(results)-1; i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].Score > results[i].Score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	if len(results) > topK {
		results = results[:topK]
	}
	return results, nil
}

// IndexedCount returns the number of indexed sessions.
func (s *EmbeddingStore) IndexedCount() int {
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM embeddings").Scan(&count)
	return count
}

// OpenAI embeddings API types
type openAIEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type openAIEmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func callOpenAIEmbed(config *OpenAIConfig, texts []string) ([][]float32, error) {
	reqBody := openAIEmbedRequest{
		Model: config.Model,
		Input: texts,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	baseURL := "https://api.openai.com/v1"
	if config.BaseURL != "" {
		baseURL = strings.TrimRight(config.BaseURL, "/")
	}
	req, err := http.NewRequest("POST", baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result openAIEmbedResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	if result.Error != nil {
		return nil, fmt.Errorf("openai: %s", result.Error.Message)
	}

	embeddings := make([][]float32, len(result.Data))
	for i, d := range result.Data {
		embeddings[i] = d.Embedding
	}
	return embeddings, nil
}

func cosineSimilarity(a, b []float32) float64 {
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}

func float32ToBlob(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

func blobToFloat32(b []byte) []float32 {
	n := len(b) / 4
	v := make([]float32, n)
	for i := range n {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
