package render

import (
	"strings"
	"testing"

	"github.com/labdrian-ai/memorycard/internal/memory"
)

func TestSearchShowsProjectOnlyWhenWidened(t *testing.T) {
	hit := memory.Hit{ID: 7, Type: "decision", Title: "Cache policy", Project: "other"}
	plain := Search(memory.SearchResult{Mode: memory.ModeDense, Confidence: memory.ConfidenceOK, Hits: []memory.Hit{hit}})
	if strings.Contains(plain, "other") || strings.Contains(plain, "note:") {
		t.Errorf("a normal result must not mention projects: %q", plain)
	}
	wide := Search(memory.SearchResult{Mode: memory.ModeDense, Confidence: memory.ConfidenceOK, Widened: true, Hits: []memory.Hit{hit}})
	if !strings.Contains(wide, "note: nothing relevant in this project; showing all projects") ||
		!strings.Contains(wide, "#7 decision: Cache policy [other]") {
		t.Errorf("widened result = %q", wide)
	}
}
