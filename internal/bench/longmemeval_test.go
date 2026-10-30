package bench

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labdrian-ai/memorycard/internal/embed"
)

// keywordEmbedder puts text on one axis per keyword, so rankings are predictable.
type keywordEmbedder struct{ words []string }

func (e keywordEmbedder) Model() string { return "keywords" }
func (e keywordEmbedder) Embed(_ context.Context, texts []string, _ embed.Kind) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, len(e.words)+1)
		v[len(e.words)] = 0.01 // keeps every vector non-zero
		for j, w := range e.words {
			if strings.Contains(strings.ToLower(t), w) {
				v[j] = 1
			}
		}
		out[i] = v
	}
	return out, nil
}

// fixture: the answer to q1 sits in the second round of session "ans", after
// 1,600 characters of filler, beyond the part of a memory that is embedded.
// Session "near" is about a related subject without the answer's words;
// session "far" is unrelated.
func fixture(t *testing.T) string {
	t.Helper()
	filler := strings.Repeat("we talked about the weather for a while. ", 40)
	qs := []map[string]any{
		{
			"question_id": "q1", "question_type": "single-session-user",
			"question": "Where did I say I moved to? denver", "answer": "Denver",
			"haystack_dates":       []string{"2023/05/01", "2023/05/02", "2023/05/03"},
			"haystack_session_ids": []string{"near", "ans", "far"},
			"answer_session_ids":   []string{"ans"},
			"haystack_sessions": [][]map[string]any{
				{{"role": "user", "content": "Is Colorado nice in spring?"}, {"role": "assistant", "content": "Yes."}},
				{
					{"role": "user", "content": filler}, {"role": "assistant", "content": "Indeed."},
					{"role": "user", "content": "I moved to Denver last month.", "has_answer": true},
					{"role": "assistant", "content": "Congratulations on the move!"},
				},
				{{"role": "user", "content": "Recommend a pizza place."}, {"role": "assistant", "content": "Try the corner one."}},
			},
		},
		{
			"question_id": "q2_abs", "question_type": "single-session-user",
			"question": "What is my cat called?", "answer": "unknown",
			"haystack_dates":       []string{"2023/05/01"},
			"haystack_session_ids": []string{"x"},
			"answer_session_ids":   []string{},
			"haystack_sessions":    [][]map[string]any{{{"role": "user", "content": "hi"}}},
		},
	}
	b, err := json.Marshal(qs)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "lme.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRoundGranularityFindsAnswersBeyondTheEmbedWindow(t *testing.T) {
	ctx := context.Background()
	qs, err := Load(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 2 || qs[0].ID != "q1" || !qs[1].Abstention() {
		t.Fatalf("loaded %+v", qs)
	}
	emb := keywordEmbedder{words: []string{"denver", "moved", "pizza"}}

	bySession, err := RunQuestion(ctx, qs[0], Setup{Granularity: Session, Embedder: emb}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if bySession.Docs != 3 || bySession.Mode != "dense" {
		t.Fatalf("session run: %+v, want 3 docs in dense mode", bySession)
	}
	if r := bySession.Ranks[0]; r <= 1 {
		t.Fatalf("session granularity ranked the answer %d; its answer lies beyond the embed window, so it must not be first", r)
	}

	byRound, err := RunQuestion(ctx, qs[0], Setup{Granularity: Round, Embedder: emb}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if byRound.Docs != 4 {
		t.Fatalf("round run has %d docs, want 4 (1 + 2 + 1 rounds)", byRound.Docs)
	}
	if r := byRound.Ranks[0]; r != 1 {
		t.Fatalf("round granularity ranked the answer %d, want 1", r)
	}

	abs, err := RunQuestion(ctx, qs[1], Setup{Granularity: Round, Embedder: emb}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !abs.Abstention || abs.Docs != 0 {
		t.Fatalf("abstention question must be skipped, got %+v", abs)
	}

	s := Summarize([]Result{bySession, byRound, abs}, []int{1, 5})
	if s.Scored != 2 || s.Abstentions != 1 {
		t.Fatalf("summary counts: %+v", s)
	}
	if s.Any[1] != 0.5 || s.Any[5] != 1 || s.All[1] != 0.5 {
		t.Fatalf("recall: any %v all %v, want any@1 0.5, any@5 1, all@1 0.5", s.Any, s.All)
	}
}

func TestLexicalRunWithoutEmbedder(t *testing.T) {
	qs, err := Load(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	r, err := RunQuestion(context.Background(), qs[0], Setup{Granularity: Round}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != "lexical" || r.Ranks[0] < 1 {
		t.Fatalf("lexical run: %+v, want the answer found in lexical mode", r)
	}
}

func TestHybridSessionRunFindsAnswersBeyondTheEmbedWindow(t *testing.T) {
	qs, err := Load(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	emb := keywordEmbedder{words: []string{"denver", "moved", "pizza"}}
	r, err := RunQuestion(context.Background(), qs[0], Setup{Granularity: Session, Embedder: emb, Hybrid: true}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != "hybrid" || r.Ranks[0] != 1 {
		t.Fatalf("hybrid session run: %+v, want the answer first in hybrid mode", r)
	}
}

func TestSampleIsDeterministic(t *testing.T) {
	qs := make([]Question, 10)
	for i := range qs {
		qs[i].ID = string(rune('a' + i))
	}
	a, b := Sample(qs, 4, 7), Sample(qs, 4, 7)
	if len(a) != 4 || a[0].ID != b[0].ID || a[3].ID != b[3].ID {
		t.Fatalf("same seed gave %v and %v", a, b)
	}
	if len(Sample(qs, 0, 7)) != 10 {
		t.Fatal("n = 0 must keep every question")
	}
}
