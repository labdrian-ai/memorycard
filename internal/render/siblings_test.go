package render

import (
	"testing"

	"github.com/labdrian-ai/memorycard/internal/memory"
)

func TestSearchListsTheOtherPartsOfASplitDocument(t *testing.T) {
	out := Search(memory.SearchResult{Mode: memory.ModeDense, Confidence: memory.ConfidenceOK,
		Hits: []memory.Hit{
			{ID: 3447, Type: "architecture", Title: "odd/x/tasks (part 1/4)", Siblings: []int64{3744, 3745, 3746}},
			{ID: 9, Type: "decision", Title: "Whole"},
		}})
	want := "#3447 architecture: odd/x/tasks (part 1/4) [other parts: #3744, #3745, #3746]\n#9 decision: Whole\n"
	if out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}
