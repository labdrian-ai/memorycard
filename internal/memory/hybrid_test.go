package memory

import (
	"strings"
	"testing"
)

// hybridSeeded saves three topic memories and, last, one whose only distinctive
// words sit after the part of the body that is embedded.
func hybridSeeded(t *testing.T) (*Service, map[string]int64) {
	s, m := seeded(t)
	filler := strings.Repeat("notes about nothing in particular. ", 50)
	m["late"] = save(t, s, SaveInput{Project: "p", Title: "Old chat",
		Body: filler + "The zephyrine passcode is kept in the blue binder."}).ID
	return s, m
}

func TestHybridFindsWordsBeyondTheEmbedWindow(t *testing.T) {
	s, m := hybridSeeded(t)
	q := SearchInput{Query: "where is the zephyrine passcode", Project: "p"}

	dense := search(t, s, q)
	if dense.Mode != ModeDense || (len(dense.Hits) > 0 && dense.Hits[0].ID == m["late"]) {
		t.Fatalf("dense-only search: mode %q, hits %v; the late words must be invisible to it", dense.Mode, ids(dense))
	}

	s.SetHybrid(true)
	r := search(t, s, q)
	if r.Mode != ModeHybrid {
		t.Fatalf("mode = %q, want hybrid", r.Mode)
	}
	if len(r.Hits) == 0 || r.Hits[0].ID != m["late"] {
		t.Fatalf("hybrid hits = %v, want the late memory (%d) first", ids(r), m["late"])
	}
}

func TestHybridKeepsDenseRankingAndCosineConfidence(t *testing.T) {
	s, m := hybridSeeded(t)
	s.SetHybrid(true)
	r := search(t, s, SearchInput{Query: "how does the cache expire", Project: "p"})
	if len(r.Hits) == 0 || r.Hits[0].ID != m["cache"] || r.Confidence != ConfidenceOK {
		t.Fatalf("hits %v confidence %q, want cache (%d) first with ok", ids(r), r.Confidence, m["cache"])
	}
	off := search(t, s, SearchInput{Query: "weather forecast", Project: "p"})
	if off.Confidence != ConfidenceLow {
		t.Fatalf("off-topic confidence = %q, want low as in dense mode", off.Confidence)
	}
	if got := search(t, s, SearchInput{Query: "cache", Project: "p", K: 2}); len(got.Hits) != 2 {
		t.Fatalf("k=2 returned %d hits", len(got.Hits))
	}
}

// "authorization" shares no indexed word with any memory, so only the dense
// list can surface the auth memory: fusion must keep it.
func TestHybridKeepsMemoriesOnlyTheDenseListFinds(t *testing.T) {
	s, m := hybridSeeded(t)
	s.SetHybrid(true)
	r := search(t, s, SearchInput{Query: "authorization", Project: "p"})
	if len(r.Hits) == 0 || r.Hits[0].ID != m["auth"] {
		t.Fatalf("hits = %v, want the auth memory (%d) first from the dense list", ids(r), m["auth"])
	}
}

func TestHybridFallsBackToLexicalWhenEmbedderDown(t *testing.T) {
	fe := &fakeEmbedder{fn: topicVec}
	s := withEmbedder(t, fe)
	save(t, s, SaveInput{Project: "p", Title: "Cache policy", Body: "LRU cache"})
	s.SetHybrid(true)
	fe.fail = true
	if r := search(t, s, SearchInput{Query: "cache", Project: "p"}); r.Mode != ModeLexical || len(r.Hits) != 1 {
		t.Fatalf("mode %q hits %v, want lexical with 1 hit", r.Mode, ids(r))
	}
}
