package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultOllamaURL is where a local ollama listens.
const DefaultOllamaURL = "http://localhost:11434"

// Ollama embeds through a local ollama server.
type Ollama struct {
	BaseURL     string
	ModelName   string
	DocPrefix   string // prepended to documents; empty for bge-m3
	QueryPrefix string // prepended to queries; empty for bge-m3
	HTTP        *http.Client
}

// NewOllama returns a client for model at baseURL (DefaultOllamaURL if empty).
func NewOllama(baseURL, model string) *Ollama {
	if baseURL == "" {
		baseURL = DefaultOllamaURL
	}
	return &Ollama{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		ModelName: model,
		HTTP:      &http.Client{Timeout: 60 * time.Second},
	}
}

// Model implements Embedder.
func (o *Ollama) Model() string { return o.ModelName }

// Embed implements Embedder.
func (o *Ollama) Embed(ctx context.Context, texts []string, kind Kind) ([][]float32, error) {
	prefix := o.DocPrefix
	if kind == KindQuery {
		prefix = o.QueryPrefix
	}
	input := make([]string, len(texts))
	for i, t := range texts {
		input[i] = prefix + t
	}
	body, err := json.Marshal(map[string]any{"model": o.ModelName, "input": input})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("%w: HTTP %d %s", ErrUnavailable, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: bad response: %v", ErrUnavailable, err)
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("embedder returned %d vectors for %d texts", len(out.Embeddings), len(texts))
	}
	return out.Embeddings, nil
}
