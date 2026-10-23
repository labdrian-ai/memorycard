package embed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaEmbed(t *testing.T) {
	var got struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{1, 2}, {3, 4}}})
	}))
	defer srv.Close()

	o := NewOllama(srv.URL, "bge-m3")
	vecs, err := o.Embed(context.Background(), []string{"a", "b"}, KindDocument)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "bge-m3" || len(got.Input) != 2 || got.Input[0] != "a" {
		t.Errorf("request body = %+v", got)
	}
	if len(vecs) != 2 || vecs[1][1] != 4 {
		t.Errorf("vectors = %v", vecs)
	}
	if o.Model() != "bge-m3" {
		t.Errorf("Model() = %q", o.Model())
	}
}

func TestOllamaAppliesKindPrefixes(t *testing.T) {
	var got struct{ Input []string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{1}}})
	}))
	defer srv.Close()

	o := NewOllama(srv.URL, "nomic-embed-text")
	o.DocPrefix, o.QueryPrefix = "search_document: ", "search_query: "
	o.Embed(context.Background(), []string{"x"}, KindQuery)
	if got.Input[0] != "search_query: x" {
		t.Errorf("query input = %q", got.Input[0])
	}
	o.Embed(context.Background(), []string{"x"}, KindDocument)
	if got.Input[0] != "search_document: x" {
		t.Errorf("document input = %q", got.Input[0])
	}
}

func TestOllamaUnavailable(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := down.URL
	down.Close() // nothing listens any more
	if _, err := NewOllama(url, "m").Embed(context.Background(), []string{"a"}, KindQuery); !errors.Is(err, ErrUnavailable) {
		t.Errorf("connection refused: err = %v, want ErrUnavailable", err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"model not found"}`, http.StatusNotFound)
	}))
	defer bad.Close()
	if _, err := NewOllama(bad.URL, "m").Embed(context.Background(), []string{"a"}, KindQuery); !errors.Is(err, ErrUnavailable) {
		t.Errorf("HTTP 404: err = %v, want ErrUnavailable", err)
	}

	short := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{1}}})
	}))
	defer short.Close()
	if _, err := NewOllama(short.URL, "m").Embed(context.Background(), []string{"a", "b"}, KindQuery); err == nil {
		t.Error("mismatched embedding count must be an error")
	}
}
