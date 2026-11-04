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

func TestSearchNamesTheOtherPartsOfASplitDocument(t *testing.T) {
	out := Search(memory.SearchResult{Mode: memory.ModeDense, Confidence: memory.ConfidenceOK,
		Hits: []memory.Hit{{ID: 10, Type: "architecture", Title: "Doc (part 1/3)", Siblings: []int64{11, 12}}}})
	if out != "#10 architecture: Doc (part 1/3) [other parts: #11, #12]\n" {
		t.Errorf("got %q, want the hit line naming parts #11 and #12", out)
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

func TestSaveNamesSimilarMemoriesAsASupersedeHint(t *testing.T) {
	out := Save(memory.SaveResult{ID: 12, Action: memory.ActionCreated,
		Hint:       &memory.Hint{ID: 2, Title: "Same", Score: 0.981},
		Neighbours: []memory.Hint{{ID: 5, Title: "Close one", Score: 0.912}, {ID: 3, Title: "Less close", Score: 0.764}}})
	want := "created #12\n" +
		"possible duplicate of #2 (0.98): Same\n" +
		"similar active memories (if one is outdated, save again with supersedes=<id>):\n" +
		"  #5 (0.91): Close one\n" +
		"  #3 (0.76): Less close\n"
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
	if out := Save(memory.SaveResult{ID: 1, Action: memory.ActionCreated}); out != "created #1\n" {
		t.Errorf("without neighbours: %q", out)
	}
}
