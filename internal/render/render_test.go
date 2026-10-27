package render

import (
	"strings"
	"testing"

	"github.com/labdrian-ai/memorycard/internal/memory"
)

func TestSearchStaysSilentWhenEverythingIsFine(t *testing.T) {
	out := Search(memory.SearchResult{Mode: memory.ModeDense, Confidence: memory.ConfidenceOK,
		Hits: []memory.Hit{{ID: 3, Type: "decision", Title: "Cache policy"}}})
	if out != "#3 decision: Cache policy\n" {
		t.Errorf("got %q, want only the hit line", out)
	}
}

func TestSearchFlagsLowConfidenceStaleAndSnippet(t *testing.T) {
	out := Search(memory.SearchResult{Mode: memory.ModeDense, Confidence: memory.ConfidenceLow,
		Hits: []memory.Hit{{ID: 1, Type: "policy", Title: "Old", Stale: true, Snippet: "body text"}}})
	for _, want := range []string{"confidence: low", "#1 policy: Old [stale]", "  body text"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "mode: lexical") {
		t.Errorf("dense result claimed lexical mode: %q", out)
	}
}

func TestSaveExplainsDuplicatesWarningsAndHints(t *testing.T) {
	if out := Save(memory.SaveResult{ID: 7, Action: memory.ActionDuplicate}); out != "duplicate of #7 (nothing written)\n" {
		t.Errorf("duplicate: %q", out)
	}
	out := Save(memory.SaveResult{ID: 8, Action: memory.ActionCreated, Warnings: []string{"big"},
		Hint: &memory.Hint{ID: 2, Title: "Similar", Score: 0.981}})
	for _, want := range []string{"created #8", "warning: big", "possible duplicate of #2 (0.98): Similar"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	if out := Save(memory.SaveResult{ID: 9, Action: memory.ActionCreated, Parts: []int64{9, 10, 11}}); out != "created #9\nparts: #9 #10 #11\n" {
		t.Errorf("split save: %q", out)
	}
}
