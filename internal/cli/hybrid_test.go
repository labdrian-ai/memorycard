package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSearchModes checks that search is hybrid by default and with
// MEMORYCARD_SEARCH=hybrid, and dense with MEMORYCARD_SEARCH=dense. Every text
// embeds to the same vector, so dense ranking is a tie broken by save order,
// while the keyword signals put the only memory holding the query word first.
func TestSearchModes(t *testing.T) {
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
	env := func(mode string) map[string]string {
		e := map[string]string{"MEMORYCARD_EMBED": "on", "MEMORYCARD_OLLAMA_URL": srv.URL}
		if mode != "" {
			e["MEMORYCARD_SEARCH"] = mode
		}
		return e
	}
	for _, m := range [][2]string{{"First note", "nothing here"}, {"Second note", "nothing either"}, {"Third note", "the zebracode lives here"}} {
		if code, _, errOut := runEnv(t, db, env(""), "", "save", "--title", m[0], "--body", m[1]); code != 0 {
			t.Fatalf("save %q: code=%d err=%q", m[0], code, errOut)
		}
	}

	for _, c := range []struct{ mode, want, not string }{
		{"", "#3 ", "#1 "},
		{"hybrid", "#3 ", "#1 "},
		{"dense", "#1 ", "#3 "},
	} {
		code, out, errOut := runEnv(t, db, env(c.mode), "", "search", "--k", "1", "zebracode")
		if code != 0 || !strings.Contains(out, c.want) || strings.Contains(out, c.not) {
			t.Errorf("MEMORYCARD_SEARCH=%q: code=%d out=%q err=%q, want %q", c.mode, code, out, errOut, c.want)
		}
	}
}
