package mcpserver

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/labdrian-ai/memorycard/internal/embed"
	"github.com/labdrian-ai/memorycard/internal/memory"
	"github.com/labdrian-ai/memorycard/internal/store"
)

// keywordEmbedder points "alpha" texts along x, "beta" texts at cosine 0.8 to
// them, and everything else away from both.
type keywordEmbedder struct{}

func (keywordEmbedder) Model() string { return "kw" }
func (keywordEmbedder) Embed(_ context.Context, texts []string, _ embed.Kind) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		switch {
		case strings.Contains(t, "alpha"):
			out[i] = []float32{1, 0, 0}
		case strings.Contains(t, "beta"):
			out[i] = []float32{0.8, 0.6, 0}
		default:
			out[i] = []float32{0, 0, 1}
		}
	}
	return out, nil
}

func connectEmbedded(t *testing.T) *mcp.ClientSession {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	svc := memory.New(db)
	svc.SetEmbedder(keywordEmbedder{})
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := New(svc, "p").Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestSaveResultNamesSimilarActiveMemories(t *testing.T) {
	cs := connectEmbedded(t)
	call(t, cs, "mem_save", map[string]any{"title": "alpha: dense search by default", "body": "x", "type": "decision"})
	out, isErr := call(t, cs, "mem_save", map[string]any{"title": "beta: hybrid search by default", "body": "y", "type": "decision"})
	want := "created #2\nsimilar active memories (if one is outdated, save again with supersedes=<id>):\n  #1 (0.80): alpha: dense search by default\n"
	if isErr || out != want {
		t.Fatalf("save = %q (error=%v), want %q", out, isErr, want)
	}
	// Following the hint: saving again with supersedes retires the old one.
	out, isErr = call(t, cs, "mem_save", map[string]any{"title": "beta: hybrid search by default", "body": "y", "type": "decision", "supersedes": 1})
	if isErr || !strings.Contains(out, "duplicate of #2") {
		t.Fatalf("supersede save = %q (error=%v)", out, isErr)
	}
	out, _ = call(t, cs, "mem_get", map[string]any{"id": 1})
	if !strings.Contains(out, "status=superseded superseded_by=#2") {
		t.Errorf("get 1 = %q, want it superseded by #2", out)
	}
	out, _ = call(t, cs, "mem_save", map[string]any{"title": "beta again, other words", "body": "z", "type": "decision"})
	if strings.Contains(out, "#1 ") {
		t.Errorf("superseded #1 still offered: %q", out)
	}
}
