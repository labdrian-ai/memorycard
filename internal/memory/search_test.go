package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/labdrian-ai/memorycard/internal/embed"
)

// topicVec places text on three topic axes; text with no keyword is the zero vector.
func topicVec(text string) []float32 {
	t := strings.ToLower(text)
	v := make([]float32, 3)
	for i, w := range []string{"cache", "auth", "deploy"} {
		if strings.Contains(t, w) {
			v[i] = 1
		}
	}
	return v
}

func search(t *testing.T, s *Service, in SearchInput) SearchResult {
	t.Helper()
	r, err := s.Search(context.Background(), in)
	if err != nil {
		t.Fatalf("Search(%+v): %v", in, err)
	}
	return r
}

func ids(r SearchResult) []int64 {
	var out []int64
	for _, h := range r.Hits {
		out = append(out, h.ID)
	}
	return out
}

func seeded(t *testing.T) (*Service, map[string]int64) {
	fe := &fakeEmbedder{fn: topicVec}
	s := withEmbedder(t, fe)
	m := map[string]int64{}
	for _, d := range []struct{ key, title, body string }{
		{"cache", "Cache policy", "Use LRU with a five minute TTL for the cache layer."},
		{"auth", "Auth model", "Sessions use signed tokens; auth rotates keys monthly."},
		{"deploy", "Deploy steps", "The deploy pipeline runs migrations first."},
	} {
		m[d.key] = save(t, s, SaveInput{Project: "p", Type: "decision", Title: d.title, Body: d.body}).ID
	}
	return s, m
}

func TestSearchDenseRanksBySimilarity(t *testing.T) {
	s, m := seeded(t)
	r := search(t, s, SearchInput{Query: "how does the cache expire", Project: "p"})
	if r.Mode != ModeDense {
		t.Fatalf("mode = %q, want dense", r.Mode)
	}
	if len(r.Hits) == 0 || r.Hits[0].ID != m["cache"] {
		t.Fatalf("hits = %v, want cache first (%d)", ids(r), m["cache"])
	}
	if r.Confidence != ConfidenceOK {
		t.Errorf("confidence = %q, want ok", r.Confidence)
	}
}

func TestSearchEmbedsQueryAsQuery(t *testing.T) {
	fe := &fakeEmbedder{fn: topicVec}
	s := withEmbedder(t, fe)
	save(t, s, SaveInput{Project: "p", Title: "cache", Body: "cache body"})
	fe.kinds = nil
	search(t, s, SearchInput{Query: "cache", Project: "p"})
	if len(fe.kinds) != 1 || fe.kinds[0] != embed.KindQuery {
		t.Errorf("kinds = %v, want one KindQuery", fe.kinds)
	}
}

func TestSearchLowConfidenceWhenNothingMatches(t *testing.T) {
	s, _ := seeded(t)
	r := search(t, s, SearchInput{Query: "weather forecast", Project: "p"})
	if r.Confidence != ConfidenceLow {
		t.Errorf("confidence = %q, want low", r.Confidence)
	}
}

func TestSearchLimits(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: topicVec})
	for i := 0; i < 8; i++ {
		save(t, s, SaveInput{Project: "p", Title: "cache note " + string(rune('a'+i)), Body: "cache detail " + string(rune('a'+i))})
	}
	if n := len(search(t, s, SearchInput{Query: "cache", Project: "p"}).Hits); n != DefaultK {
		t.Errorf("default hits = %d, want %d", n, DefaultK)
	}
	if n := len(search(t, s, SearchInput{Query: "cache", Project: "p", K: 2}).Hits); n != 2 {
		t.Errorf("K=2 hits = %d", n)
	}
	if n := len(search(t, s, SearchInput{Query: "cache", Project: "p", K: 500}).Hits); n != 8 {
		t.Errorf("K above max hits = %d, want 8 (capped at %d)", n, MaxK)
	}
}

func TestSearchFilters(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: topicVec})
	a := save(t, s, SaveInput{Project: "p", Type: "decision", Title: "cache a", Body: "cache"}).ID
	b := save(t, s, SaveInput{Project: "p", Type: "bugfix", Title: "cache b", Body: "cache fix"}).ID
	c := save(t, s, SaveInput{Project: "q", Type: "decision", Title: "cache c", Body: "cache other project"}).ID
	g := save(t, s, SaveInput{Project: "q", Scope: "global", Type: "decision", Title: "cache g", Body: "cache global note"}).ID

	got := ids(search(t, s, SearchInput{Query: "cache", Project: "p"}))
	if !contains(got, a) || !contains(got, b) || contains(got, c) || !contains(got, g) {
		t.Errorf("project filter hits = %v (want a,b,g and not c)", got)
	}
	got = ids(search(t, s, SearchInput{Query: "cache", Project: "p", Type: "bugfix"}))
	if len(got) != 1 || got[0] != b {
		t.Errorf("type filter hits = %v, want only %d", got, b)
	}
}

func contains(xs []int64, x int64) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func TestSearchHidesSupersededAndArchived(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: topicVec})
	old := save(t, s, SaveInput{Project: "p", Title: "cache v1", Body: "cache old"}).ID
	neu := save(t, s, SaveInput{Project: "p", Title: "cache v2", Body: "cache new", Supersedes: old}).ID
	arch := save(t, s, SaveInput{Project: "p", Title: "cache v3", Body: "cache archived"}).ID
	if _, err := s.db.SQL().Exec(`update memories set status='archived' where id=?`, arch); err != nil {
		t.Fatal(err)
	}

	got := ids(search(t, s, SearchInput{Query: "cache", Project: "p"}))
	if len(got) != 1 || got[0] != neu {
		t.Errorf("default hits = %v, want only %d", got, neu)
	}
	got = ids(search(t, s, SearchInput{Query: "cache", Project: "p", IncludeOld: true}))
	if len(got) != 3 {
		t.Errorf("IncludeOld hits = %v, want 3", got)
	}
}

func TestSearchFallsBackToLexicalWhenEmbedderDown(t *testing.T) {
	fe := &fakeEmbedder{fn: topicVec}
	s := withEmbedder(t, fe)
	id := save(t, s, SaveInput{Project: "p", Title: "Decisión de caché", Body: "Usamos expiración corta para la sesión."}).ID
	save(t, s, SaveInput{Project: "p", Title: "Other thing", Body: "nothing relevant here"})
	fe.fail = true

	r := search(t, s, SearchInput{Query: "que decision tomamos sobre la expiracion de la cache?", Project: "p"})
	if r.Mode != ModeLexical {
		t.Fatalf("mode = %q, want lexical", r.Mode)
	}
	if len(r.Hits) == 0 || r.Hits[0].ID != id {
		t.Errorf("hits = %v, want %d first (accents folded, OR semantics)", ids(r), id)
	}
	if r.Confidence != ConfidenceUnknown {
		t.Errorf("lexical confidence = %q, want unknown", r.Confidence)
	}
}

func TestSearchWithoutEmbedderIsLexical(t *testing.T) {
	s := newSvc(t)
	id := save(t, s, SaveInput{Project: "p", Title: "Rollout plan", Body: "canary first"}).ID
	r := search(t, s, SearchInput{Query: "rollout", Project: "p"})
	if r.Mode != ModeLexical || len(r.Hits) != 1 || r.Hits[0].ID != id {
		t.Errorf("got %+v", r)
	}
	if none := search(t, s, SearchInput{Query: "zzzzzz", Project: "p"}); len(none.Hits) != 0 || none.Confidence != ConfidenceLow {
		t.Errorf("no match: %+v, want empty with low confidence", none)
	}
}

func TestSearchTopicKeyShortcut(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: topicVec})
	save(t, s, SaveInput{Project: "p", Title: "cache a", Body: "cache one"})
	want := save(t, s, SaveInput{Project: "p", Title: "Auth model", Body: "auth tokens", TopicKey: "architecture/auth-model"}).ID
	r := search(t, s, SearchInput{Query: "architecture/auth-model", Project: "p"})
	if len(r.Hits) == 0 || r.Hits[0].ID != want {
		t.Errorf("hits = %v, want %d first", ids(r), want)
	}
}

func TestSearchDetailLevels(t *testing.T) {
	s := newSvc(t)
	body := strings.Repeat("palabra ", 100)
	save(t, s, SaveInput{Project: "p", Title: "Largo", Body: body})
	title := search(t, s, SearchInput{Query: "largo", Project: "p"})
	if title.Hits[0].Snippet != "" {
		t.Errorf("title detail returned a snippet: %q", title.Hits[0].Snippet)
	}
	snip := search(t, s, SearchInput{Query: "largo", Project: "p", Detail: DetailSnippet})
	if n := len([]rune(snip.Hits[0].Snippet)); n == 0 || n > SnippetRunes {
		t.Errorf("snippet has %d runes, want 1..%d", n, SnippetRunes)
	}
}

func TestSearchFlagsStaleDecisions(t *testing.T) {
	s := newSvc(t)
	old := save(t, s, SaveInput{Project: "p", Type: "decision", Title: "cache old decision", Body: "cache"}).ID
	fresh := save(t, s, SaveInput{Project: "p", Type: "decision", Title: "cache fresh decision", Body: "cache"}).ID
	note := save(t, s, SaveInput{Project: "p", Type: "discovery", Title: "cache old discovery", Body: "cache"}).ID
	long := s.now().Add(-StaleAfter - time.Hour).UTC().Format(time.RFC3339Nano)
	for _, id := range []int64{old, note} {
		if _, err := s.db.SQL().Exec(`update memories set updated_at=? where id=?`, long, id); err != nil {
			t.Fatal(err)
		}
	}
	stale := map[int64]bool{}
	for _, h := range search(t, s, SearchInput{Query: "cache", Project: "p"}).Hits {
		stale[h.ID] = h.Stale
	}
	if !stale[old] || stale[fresh] || stale[note] {
		t.Errorf("stale flags = %v (old=%d fresh=%d note=%d)", stale, old, fresh, note)
	}
}

func TestSearchRejectsEmptyQuery(t *testing.T) {
	s := newSvc(t)
	if _, err := s.Search(context.Background(), SearchInput{Query: "  ", Project: "p"}); !errors.Is(err, ErrEmptyQuery) {
		t.Errorf("err = %v, want ErrEmptyQuery", err)
	}
}
