package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labdrian-ai/memorycard/internal/store"
)

// embedServer answers every ollama embed request and records the batch sizes.
func embedServer(t *testing.T, batches *[]int) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []string }
		json.NewDecoder(r.Body).Decode(&req)
		*batches = append(*batches, len(req.Input))
		vecs := make([][]float32, len(req.Input))
		for i := range vecs {
			vecs[i] = []float32{1, float32(i + 1), 0}
		}
		json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEmbedIndexesEveryChunkOfALongMemory(t *testing.T) {
	var batches []int
	srv := embedServer(t, &batches)
	db := newDB(t)
	run(t, db, "", "save", "--title", "long", "--body", strings.Repeat("word ", 1000)) // 5000 runes: 4 chunks
	run(t, db, "", "save", "--title", "short", "--body", "body")

	on := map[string]string{"MEMORYCARD_EMBED": "on", "MEMORYCARD_OLLAMA_URL": srv.URL}
	code, out, errOut := runEnv(t, db, on, "", "embed", "--limit", "3")
	if code != 0 || out != "embedded 2\n" {
		t.Fatalf("embed: code=%d out=%q err=%q", code, out, errOut)
	}
	total := 0
	for _, b := range batches {
		if b > 3 {
			t.Errorf("embedder request of %d texts, --limit is 3", b)
		}
		total += b
	}
	if total != 5 {
		t.Errorf("embedded %d texts, want 5 (4 chunks + 1 short memory)", total)
	}
	if _, out, _ := run(t, db, "", "stats"); !strings.Contains(out, "without embedding: 0") {
		t.Errorf("stats after embed = %q", out)
	}
	d, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var chunks int
	if err := d.SQL().QueryRow(`select count(*) from vector_chunks`).Scan(&chunks); err != nil || chunks != 3 {
		t.Errorf("vector_chunks rows = %d (%v), want 3", chunks, err)
	}

	// Nothing left: a second run embeds nothing.
	batches = nil
	if code, out, _ := runEnv(t, db, on, "", "embed"); code != 0 || out != "embedded 0\n" || len(batches) != 0 {
		t.Errorf("second embed: code=%d out=%q requests=%d", code, out, len(batches))
	}
}

func TestUsageDescribesEmbedChunks(t *testing.T) {
	_, out, _ := run(t, newDB(t), "", "help")
	if !strings.Contains(out, "long memories get one vector per chunk") {
		t.Errorf("usage does not describe chunked embedding:\n%s", out)
	}
}
