package memory

import (
	"slices"
	"testing"

	"github.com/labdrian-ai/memorycard/internal/embed"
)

func TestSearchLeavesSessionSummariesOutByDefault(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: topicVec})
	note := save(t, s, SaveInput{Project: "p", Type: "decision", Title: "Cache policy", Body: "cache with LRU"}).ID
	save(t, s, SaveInput{Project: "p", Type: "session_summary", Title: "Session summary", Body: "worked on the cache"})

	r := search(t, s, SearchInput{Query: "cache", Project: "p"})
	if got := ids(r); !slices.Equal(got, []int64{note}) {
		t.Errorf("hits = %v, want only the decision #%d", got, note)
	}
	if r.Widened || r.Confidence != ConfidenceOK {
		t.Errorf("widened=%v confidence=%q, want not widened, ok", r.Widened, r.Confidence)
	}
}

func TestSearchReturnsSessionSummariesWhenTheTypeFilterAsks(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: topicVec})
	save(t, s, SaveInput{Project: "p", Type: "decision", Title: "Cache policy", Body: "cache with LRU"})
	sum := save(t, s, SaveInput{Project: "p", Type: "session_summary", Title: "Session summary", Body: "worked on the cache"}).ID

	r := search(t, s, SearchInput{Query: "cache", Project: "p", Type: "session_summary"})
	if got := ids(r); !slices.Equal(got, []int64{sum}) {
		t.Errorf("hits = %v, want only the summary #%d", got, sum)
	}
}

func TestSearchStillFiltersOtherTypes(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: topicVec})
	dec := save(t, s, SaveInput{Project: "p", Type: "decision", Title: "Cache policy", Body: "cache with LRU"}).ID
	save(t, s, SaveInput{Project: "p", Type: "discovery", Title: "Cache finding", Body: "cache misses"})

	if got := ids(search(t, s, SearchInput{Query: "cache", Project: "p", Type: "decision"})); !slices.Equal(got, []int64{dec}) {
		t.Errorf("hits = %v, want only #%d", got, dec)
	}
}

func TestSearchCollapsesPartsToTheBestPart(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: topicVec})
	p1 := save(t, s, SaveInput{Project: "p", Title: "Design doc (part 1/3)", Body: "auth tokens"}).ID
	p2 := save(t, s, SaveInput{Project: "p", Title: "Design doc (part 2/3)", Body: "cache layer"}).ID
	p3 := save(t, s, SaveInput{Project: "p", Title: "Design doc (part 3/3)", Body: "cache and auth"}).ID
	other := save(t, s, SaveInput{Project: "p", Title: "Cache note", Body: "cache and auth again"}).ID

	r := search(t, s, SearchInput{Query: "cache", Project: "p"})
	if got := ids(r); len(got) != 2 || got[0] != p2 || !slices.Contains(got, other) {
		t.Fatalf("hits = %v, want part 2 (#%d) first and #%d, one hit per document", got, p2, other)
	}
	if !slices.Equal(r.Hits[0].Siblings, []int64{p1, p3}) {
		t.Errorf("siblings = %v, want [%d %d]", r.Hits[0].Siblings, p1, p3)
	}
	if r.Hits[1].Siblings != nil {
		t.Errorf("a whole document has siblings %v", r.Hits[1].Siblings)
	}
}

func TestSearchCollapseStillReturnsKDocuments(t *testing.T) {
	for _, mode := range []string{ModeDense, ModeHybrid, ModeLexical} {
		t.Run(mode, func(t *testing.T) {
			fe := &fakeEmbedder{fn: topicVec}
			s := withEmbedder(t, fe)
			s.SetHybrid(mode == ModeHybrid)
			doc := save(t, s, SaveInput{Project: "p", Title: "Design cache doc", Body: paragraphs(30, "cache"), TopicKey: "doc"})
			a := save(t, s, SaveInput{Project: "p", Title: "Cache alpha", Body: "cache alpha body"}).ID
			b := save(t, s, SaveInput{Project: "p", Title: "Cache beta", Body: "cache beta body"}).ID
			fe.fail = mode == ModeLexical

			r := search(t, s, SearchInput{Query: "cache", Project: "p", K: 3})
			if r.Mode != mode {
				t.Fatalf("mode = %q, want %q", r.Mode, mode)
			}
			got := ids(r)
			var docHit *Hit
			for i, h := range r.Hits {
				if slices.Contains(doc.Parts, h.ID) {
					if docHit != nil {
						t.Fatalf("hits = %v: two parts of one document", got)
					}
					docHit = &r.Hits[i]
				}
			}
			if len(got) != 3 || docHit == nil || !slices.Contains(got, a) || !slices.Contains(got, b) {
				t.Fatalf("hits = %v, want the document (parts %v), #%d and #%d", got, doc.Parts, a, b)
			}
			want := slices.DeleteFunc(slices.Clone(doc.Parts), func(id int64) bool { return id == docHit.ID })
			if !slices.Equal(docHit.Siblings, want) {
				t.Errorf("siblings = %v, want %v", docHit.Siblings, want)
			}
		})
	}
}

func TestSearchWidenedEmbedsTheQueryOnce(t *testing.T) {
	fe := &fakeEmbedder{fn: topicVec}
	s := withEmbedder(t, fe)
	save(t, s, SaveInput{Project: "p", Title: "Auth model", Body: "auth tokens"})
	other := save(t, s, SaveInput{Project: "q", Title: "Cache policy", Body: "cache with LRU"}).ID
	fe.kinds = nil

	r := search(t, s, SearchInput{Query: "cache", Project: "p"})
	if !r.Widened || len(r.Hits) == 0 || r.Hits[0].ID != other {
		t.Fatalf("widened=%v hits=%v, want widened with #%d first", r.Widened, ids(r), other)
	}
	queries := 0
	for _, k := range fe.kinds {
		if k == embed.KindQuery {
			queries++
		}
	}
	if queries != 1 {
		t.Errorf("query embedded %d times, want 1", queries)
	}
}

func TestSearchTopicKeyMatchDoesNotForceConfidence(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: topicVec})
	save(t, s, SaveInput{Project: "p", Title: "Cache policy", Body: "cache with LRU"})
	want := save(t, s, SaveInput{Project: "p", Title: "Misc notes", Body: "weather today", TopicKey: "notes/misc"}).ID

	r := search(t, s, SearchInput{Query: "notes/misc", Project: "p"})
	if len(r.Hits) == 0 || r.Hits[0].ID != want {
		t.Fatalf("hits = %v, want the topic-key match #%d first", ids(r), want)
	}
	if r.Confidence != ConfidenceLow {
		t.Errorf("confidence = %q, want low: only the topic key matched", r.Confidence)
	}
}

func TestSearchTopicKeyMatchWithAStrongAnswerStaysOK(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: topicVec})
	want := save(t, s, SaveInput{Project: "p", Title: "Cache policy", Body: "cache with LRU", TopicKey: "cache/policy"}).ID

	r := search(t, s, SearchInput{Query: "cache/policy", Project: "p"})
	if len(r.Hits) != 1 || r.Hits[0].ID != want || r.Confidence != ConfidenceOK {
		t.Errorf("hits=%v confidence=%q, want #%d and ok", ids(r), r.Confidence, want)
	}
}
