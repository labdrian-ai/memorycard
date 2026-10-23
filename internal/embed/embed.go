// Package embed turns text into vectors. Implementations sit behind the
// Embedder interface so the model can change without touching the store.
package embed

import (
	"context"
	"errors"
)

// Kind says whether text is a stored document or a search query; some models
// want different prefixes for each.
type Kind int

const (
	KindDocument Kind = iota
	KindQuery
)

// ErrUnavailable means the embedder could not be reached or refused the
// request. Callers fall back to lexical search or retry later.
var ErrUnavailable = errors.New("embedder unavailable")

// Embedder produces one vector per input text, in order.
type Embedder interface {
	// Model identifies the model; stored vectors are only comparable within a model.
	Model() string
	Embed(ctx context.Context, texts []string, kind Kind) ([][]float32, error)
}
