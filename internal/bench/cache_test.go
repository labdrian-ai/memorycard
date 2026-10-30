package bench

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/labdrian-ai/memorycard/internal/embed"
)

// countingEmbedder returns a vector derived from the text and records every
// text it was asked to embed.
type countingEmbedder struct {
	model string
	seen  []string
}

func (c *countingEmbedder) Model() string { return c.model }
func (c *countingEmbedder) Embed(_ context.Context, texts []string, kind embed.Kind) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		c.seen = append(c.seen, t)
		out[i] = []float32{float32(len(t)), float32(kind), 1}
	}
	return out, nil
}

func TestCacheEmbedsEachTextOnceAcrossCallsAndReopens(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	inner := &countingEmbedder{model: "m1"}
	c, err := OpenCache(path, inner)
	if err != nil {
		t.Fatal(err)
	}
	first, err := c.Embed(ctx, []string{"alpha", "beta", "alpha"}, embed.KindDocument)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inner.seen, []string{"alpha", "beta"}) {
		t.Fatalf("inner saw %q, want each new text once", inner.seen)
	}
	if !reflect.DeepEqual(first[0], first[2]) || reflect.DeepEqual(first[0], first[1]) {
		t.Fatalf("vectors not matched to their texts: %v", first)
	}
	if c.Model() != "m1" {
		t.Fatalf("Model() = %q, want the inner model", c.Model())
	}
	c.Close()

	inner.seen = nil
	c, err = OpenCache(path, inner)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	again, err := c.Embed(ctx, []string{"beta", "gamma", "alpha"}, embed.KindDocument)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inner.seen, []string{"gamma"}) {
		t.Fatalf("after reopen inner saw %q, want only the new text", inner.seen)
	}
	if !reflect.DeepEqual(again[0], first[1]) || !reflect.DeepEqual(again[2], first[0]) {
		t.Fatalf("cached vectors changed: %v vs %v", again, first)
	}
	if h, m := c.Stats(); h != 2 || m != 1 {
		t.Fatalf("stats over this handle = %d hits, %d misses; want 2, 1", h, m)
	}
}

func TestCacheKeysIncludeModelAndKind(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	inner := &countingEmbedder{model: "m1"}
	c, err := OpenCache(path, inner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Embed(ctx, []string{"same"}, embed.KindDocument); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Embed(ctx, []string{"same"}, embed.KindQuery); err != nil {
		t.Fatal(err)
	}
	c.Close()

	other := &countingEmbedder{model: "m2"}
	c, err = OpenCache(path, other)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Embed(ctx, []string{"same"}, embed.KindDocument); err != nil {
		t.Fatal(err)
	}
	if len(inner.seen) != 2 || len(other.seen) != 1 {
		t.Fatalf("a different kind or model must miss: m1 saw %q, m2 saw %q", inner.seen, other.seen)
	}
}
