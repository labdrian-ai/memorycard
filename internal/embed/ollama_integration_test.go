//go:build integration

package embed

import (
	"context"
	"math"
	"testing"
)

// Run with: go test -tags integration ./internal/embed
// Needs a local ollama with the bge-m3 model.
func TestOllamaRealBGEM3(t *testing.T) {
	o := NewOllama("", "bge-m3")
	vecs, err := o.Embed(context.Background(),
		[]string{"cache policy for sessions", "política de caché de sesiones", "banana bread recipe"}, KindDocument)
	if err != nil {
		t.Skipf("ollama not available: %v", err)
	}
	if len(vecs) != 3 || len(vecs[0]) != 1024 {
		t.Fatalf("got %d vectors of dim %d, want 3 of 1024", len(vecs), len(vecs[0]))
	}
	cos := func(a, b []float32) float64 {
		var d, na, nb float64
		for i := range a {
			d += float64(a[i]) * float64(b[i])
			na += float64(a[i]) * float64(a[i])
			nb += float64(b[i]) * float64(b[i])
		}
		return d / (math.Sqrt(na) * math.Sqrt(nb))
	}
	cross, unrelated := cos(vecs[0], vecs[1]), cos(vecs[0], vecs[2])
	t.Logf("es-en same meaning cos=%.3f, unrelated cos=%.3f", cross, unrelated)
	if cross <= unrelated {
		t.Errorf("cross-language pair (%.3f) should be closer than unrelated (%.3f)", cross, unrelated)
	}
}
