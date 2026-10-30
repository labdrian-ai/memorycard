package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSearchHybridOption checks that MEMORYCARD_SEARCH=hybrid turns on fused
// search, and that without it search stays dense. Every text embeds to the same
// vector, so dense ranking is a tie broken by save order, while the lexical
// ranking puts the only memory holding the query word first.
func TestSearchHybridOption(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []string }
		json.NewDecoder(r.Body).Decode(&req)
		vecs := make([][]float32, len(req.Input))
		for i := range vecs {
			vecs[i] = []float32{1, 0, 0}
		}
		json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
	}))
	defer srv.Close()

	db := newDB(t)
	on := map[string]string{"MEMORYCARD_EMBED": "on", "MEMORYCARD_OLLAMA_URL": srv.URL}
	for _, m := range [][2]string{{"First note", "nothing here"}, {"Second note", "nothing either"}, {"Third note", "the zebracode lives here"}} {
		if code, _, errOut := runEnv(t, db, on, "", "save", "--title", m[0], "--body", m[1]); code != 0 {
			t.Fatalf("save %q: code=%d err=%q", m[0], code, errOut)
		}
	}

	code, out, errOut := runEnv(t, db, on, "", "search", "--k", "1", "zebracode")
	if code != 0 || !strings.Contains(out, "#1 ") {
		t.Errorf("dense search: code=%d out=%q err=%q", code, out, errOut)
	}

	hybrid := map[string]string{"MEMORYCARD_EMBED": "on", "MEMORYCARD_OLLAMA_URL": srv.URL, "MEMORYCARD_SEARCH": "hybrid"}
	code, out, errOut = runEnv(t, db, hybrid, "", "search", "--k", "1", "zebracode")
	if code != 0 || !strings.Contains(out, "#3 ") || strings.Contains(out, "#1 ") {
		t.Errorf("hybrid search: code=%d out=%q err=%q", code, out, errOut)
	}
}
