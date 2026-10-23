package accept

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labdrian-ai/memorycard/internal/embed"
	"github.com/labdrian-ai/memorycard/internal/memory"
	"github.com/labdrian-ai/memorycard/internal/store"
)

// topicEmbedder puts text on axes by keyword so rankings are predictable.
type topicEmbedder struct{ words []string }

func (e topicEmbedder) Model() string { return "topics" }
func (e topicEmbedder) Embed(_ context.Context, texts []string, _ embed.Kind) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, len(e.words))
		for j, w := range e.words {
			if strings.Contains(strings.ToLower(t), w) {
				v[j] = 1
			}
		}
		out[i] = v
	}
	return out, nil
}

type fixture struct {
	svc *memory.Service
	db  *store.DB
}

// newFixture imports five records and, optionally, embeds them:
//
//	10 Cache policy  [cache]           14 Cache notes [cache]  (same vector as 10)
//	11 Auth model    [auth, signed]    13 Auth model  [auth]   (same title as 11)
//	12 Deploy steps  [deploy]
func newFixture(t *testing.T, withEmbedder bool) fixture {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	svc := memory.New(db)
	at := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	mk := func(key, title, body string) memory.ImportRecord {
		return memory.ImportRecord{Source: "engram", Key: key, Project: "p", Type: "decision", Title: title, Body: body, CreatedAt: at, UpdatedAt: at}
	}
	if _, err := svc.Import(context.Background(), []memory.ImportRecord{
		mk("10", "Cache policy", "cache expiry is five minutes"),
		mk("11", "Auth model", "auth uses signed tokens"),
		mk("12", "Deploy steps", "deploy runs migrations first"),
		mk("13", "Auth model", "auth tokens rotate monthly"),
		mk("14", "Cache notes", "cache entries expire quickly"),
	}, nil); err != nil {
		t.Fatal(err)
	}
	if withEmbedder {
		svc.SetEmbedder(topicEmbedder{words: []string{"cache", "auth", "deploy", "signed"}})
		if _, err := svc.EmbedPending(context.Background(), 100); err != nil {
			t.Fatal(err)
		}
	}
	return fixture{svc, db}
}

func run(t *testing.T, f fixture, g Gold, o Options) []QueryResult {
	t.Helper()
	rs, err := Run(context.Background(), f.svc, f.db, g, o)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func TestSummarizeComputesHitRatesMRRAndMedian(t *testing.T) {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	rs := []QueryResult{
		{Rank: 1, Tokens: 100, Latency: ms(10), Lang: "en"},
		{Rank: 0, Tokens: 200, Latency: ms(30), Lang: "es"},
		{Rank: 3, Tokens: 300, Latency: ms(20), Lang: "es"},
		{Rank: 5, Tokens: 400, Latency: ms(40), Lang: "en"},
		{Rank: 7, Tokens: 500, Latency: ms(50), Lang: "en"},
	}
	s := Summarize(rs)
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	if s.N != 5 || !near(s.Hit1, 0.2) || !near(s.Hit3, 0.4) || !near(s.Hit5, 0.6) || !near(s.Hit10, 0.8) {
		t.Errorf("hit rates: %+v", s)
	}
	if want := (1 + 1.0/3 + 1.0/5 + 1.0/7) / 5; !near(s.MRR, want) {
		t.Errorf("MRR = %f, want %f", s.MRR, want)
	}
	if !near(s.AvgTokens, 300) || s.P50 != ms(30) {
		t.Errorf("avg tokens %f, p50 %v", s.AvgTokens, s.P50)
	}
	if !near(s.Hit5ByLang["en"], 2.0/3) || !near(s.Hit5ByLang["es"], 0.5) {
		t.Errorf("by language: %v", s.Hit5ByLang)
	}
}

func TestRunRanksThroughTheImportMap(t *testing.T) {
	f := newFixture(t, true)
	rs := run(t, f, Gold{Queries: []Query{
		{ID: "10", Text: "how long does the cache keep entries", Lang: "en"},
		{ID: "12", Text: "what runs first in a deploy", Lang: "en"},
	}}, Options{K: 10})
	if rs[0].Rank != 1 || rs[1].Rank != 1 {
		t.Errorf("ranks = %d, %d, want 1 and 1", rs[0].Rank, rs[1].Rank)
	}
	if rs[0].Mode != memory.ModeDense || rs[0].Tokens <= 0 || rs[0].Latency <= 0 {
		t.Errorf("mode %q tokens %f latency %v: must be dense and measured", rs[0].Mode, rs[0].Tokens, rs[0].Latency)
	}
}

func TestRelevantListReplacesTheDefaultTarget(t *testing.T) {
	f := newFixture(t, true)
	g := Gold{Queries: []Query{{ID: "11", Text: "signed tokens for auth", Lang: "en"}}}
	if r := run(t, f, g, Options{K: 10}); r[0].Rank != 1 {
		t.Fatalf("setup: default target rank = %d, want 1", r[0].Rank)
	}
	g.Relevant = map[string][]string{"11": {"12"}} // only the deploy note counts
	r := run(t, f, g, Options{K: 10})
	if r[0].Rank < 3 {
		t.Errorf("rank = %d, want the deploy note far down (>= 3): the override must replace the target", r[0].Rank)
	}
}

func TestSameTitleCountsUnlessDisabled(t *testing.T) {
	f := newFixture(t, true)
	g := Gold{Queries: []Query{{ID: "13", Text: "auth signed tokens", Lang: "en"}}} // best match is 11, target is 13
	if r := run(t, f, g, Options{K: 1}); r[0].Rank != 1 {
		t.Errorf("with title acceptance, rank = %d, want 1 (11 shares 13's title)", r[0].Rank)
	}
	if r := run(t, f, g, Options{K: 1, NoTitleAccept: true}); r[0].Rank != 0 {
		t.Errorf("without title acceptance and K=1, rank = %d, want a miss", r[0].Rank)
	}
	if r := run(t, f, g, Options{K: 2, NoTitleAccept: true}); r[0].Rank != 2 {
		t.Errorf("without title acceptance and K=2, rank = %d, want 2", r[0].Rank)
	}
}

func TestSimilarityAcceptance(t *testing.T) {
	f := newFixture(t, true)
	g := Gold{Queries: []Query{{ID: "14", Text: "cache entries", Lang: "en"}}} // 10 and 14 have the same vector
	if r := run(t, f, g, Options{K: 1}); r[0].Rank != 0 {
		t.Fatalf("setup: without similarity acceptance rank = %d, want a miss at K=1", r[0].Rank)
	}
	if r := run(t, f, g, Options{K: 1, SimilarityAccept: 0.97}); r[0].Rank != 1 {
		t.Errorf("with similarity acceptance rank = %d, want 1", r[0].Rank)
	}
}

func TestIncludeOldControlsSupersededTargets(t *testing.T) {
	f := newFixture(t, true)
	if _, err := f.svc.Import(context.Background(), nil, []memory.ImportLink{{Source: "engram", Replacement: "13", Replaced: "12"}}); err != nil {
		t.Fatal(err)
	}
	g := Gold{Queries: []Query{{ID: "12", Text: "deploy migrations", Lang: "en"}}}
	if r := run(t, f, g, Options{K: 5}); r[0].Rank != 0 {
		t.Errorf("superseded target found without IncludeOld: rank %d", r[0].Rank)
	}
	if r := run(t, f, g, Options{K: 5, IncludeOld: true}); r[0].Rank != 1 {
		t.Errorf("IncludeOld rank = %d, want 1", r[0].Rank)
	}
}

func TestRunWithoutEmbedderIsLexicalAndFlagged(t *testing.T) {
	f := newFixture(t, false)
	g := Gold{Queries: []Query{{ID: "12", Text: "deploy migrations", Lang: "en"}, {ID: "10", Text: "zzzz qqqq", Lang: "en"}}}
	rs := run(t, f, g, Options{K: 5})
	for _, r := range rs {
		if r.Mode != memory.ModeLexical {
			t.Errorf("mode = %q, want lexical", r.Mode)
		}
	}
	if s := Summarize(rs); s.Lexical != 2 || s.NonEmpty != 1 {
		t.Errorf("lexical %d non-empty %d, want 2 and 1", s.Lexical, s.NonEmpty)
	}
}

func TestCheckReportsEachMissedThreshold(t *testing.T) {
	good := Summary{N: 10, Hit1: 0.7, Hit5: 0.9, AvgTokens: 300, P50: 200 * time.Millisecond}
	if f := good.Check(); len(f) != 0 {
		t.Errorf("good summary failed: %v", f)
	}
	bad := Summary{N: 10, Hit1: 0.5, Hit5: 0.8, AvgTokens: 400, P50: 900 * time.Millisecond}
	f := bad.Check()
	for _, want := range []string{"hit@1", "hit@5", "tokens", "p50"} {
		found := false
		for _, line := range f {
			found = found || strings.Contains(line, want)
		}
		if !found {
			t.Errorf("no failure mentions %q: %v", want, f)
		}
	}
}

func TestLoadGoldReadsQueriesAndRelevant(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) { os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644) }
	write("queries.tsv", "id\tquery\tlang\tsplit\n5\thello there\ten\tdev\n6\thola\tes\ttest\n")
	write("relevant.tsv", "5\t5,7\n")
	g, err := LoadGold(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Queries) != 2 || g.Queries[0].Text != "hello there" || g.Queries[1].Split != "test" {
		t.Errorf("queries = %+v", g.Queries)
	}
	if got := g.Relevant["5"]; len(got) != 2 || got[1] != "7" {
		t.Errorf("relevant = %v", g.Relevant)
	}
	if _, err := LoadGold(filepath.Join(dir, "missing")); err == nil {
		t.Error("expected an error for a missing directory")
	}
}
