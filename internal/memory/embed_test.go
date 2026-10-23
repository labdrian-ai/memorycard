package memory

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/labdrian-ai/memorycard/internal/embed"
)

// fakeEmbedder maps text to a vector with fn and records every input.
type fakeEmbedder struct {
	fn   func(string) []float32
	fail bool
	seen []string
}

func (f *fakeEmbedder) Model() string { return "fake" }
func (f *fakeEmbedder) Embed(_ context.Context, texts []string, _ embed.Kind) ([][]float32, error) {
	if f.fail {
		return nil, embed.ErrUnavailable
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		f.seen = append(f.seen, t)
		out[i] = f.fn(t)
	}
	return out, nil
}

func axis(text string) []float32 {
	switch {
	case strings.Contains(text, "NEAR"):
		return []float32{1, 0.1, 0}
	case strings.Contains(text, "ALPHA"):
		return []float32{4, 0, 0} // deliberately not unit length
	default:
		return []float32{0, 1, 0}
	}
}

func withEmbedder(t *testing.T, fe *fakeEmbedder) *Service {
	s := newSvc(t)
	s.SetEmbedder(fe)
	return s
}

func TestSaveStoresNormalizedVector(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: axis})
	r := save(t, s, SaveInput{Project: "p", Title: "ALPHA", Body: "body"})
	var model string
	var dim int
	var blob []byte
	if err := s.db.SQL().QueryRow(`select model, dim, vec from vectors where memory_id=?`, r.ID).Scan(&model, &dim, &blob); err != nil {
		t.Fatalf("no vector stored: %v", err)
	}
	if model != "fake" || dim != 3 || len(blob) != 12 {
		t.Errorf("model=%q dim=%d bytes=%d", model, dim, len(blob))
	}
	v := decodeVec(blob)
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if math.Abs(norm-1) > 1e-5 {
		t.Errorf("stored vector norm^2 = %f, want 1", norm)
	}
}

func TestEmbedInputIsTitlePlusTruncatedBody(t *testing.T) {
	fe := &fakeEmbedder{fn: axis}
	s := withEmbedder(t, fe)
	save(t, s, SaveInput{Project: "p", Title: "T", Body: strings.Repeat("é", 3000)})
	if len(fe.seen) != 1 {
		t.Fatalf("embed calls = %d", len(fe.seen))
	}
	want := "T\n"
	if got := fe.seen[0]; !strings.HasPrefix(got, want) || utf8.RuneCountInString(got) != len(want)+EmbedBodyRunes {
		t.Errorf("embed input has %d runes, want %d", utf8.RuneCountInString(got), len(want)+EmbedBodyRunes)
	}
}

func TestSaveSurvivesEmbedderDownAndPendingCatchesUp(t *testing.T) {
	fe := &fakeEmbedder{fn: axis, fail: true}
	s := withEmbedder(t, fe)
	r := save(t, s, SaveInput{Project: "p", Title: "ALPHA", Body: "body"})
	if r.Action != ActionCreated || len(r.Warnings) == 0 {
		t.Fatalf("got %+v, want created with an embedding-pending warning", r)
	}
	if n := count(t, s, `select count(*) from vectors`); n != 0 {
		t.Fatalf("vectors = %d, want 0 while embedder is down", n)
	}

	fe.fail = false
	n, err := s.EmbedPending(context.Background(), 10)
	if err != nil || n != 1 {
		t.Fatalf("EmbedPending = %d, %v; want 1", n, err)
	}
	if n, err := s.EmbedPending(context.Background(), 10); err != nil || n != 0 {
		t.Errorf("second EmbedPending = %d, %v; want 0", n, err)
	}
	if c := count(t, s, `select count(*) from vectors`); c != 1 {
		t.Errorf("vectors = %d, want 1", c)
	}
}

func TestEmbedPendingReportsEmbedderError(t *testing.T) {
	fe := &fakeEmbedder{fn: axis, fail: true}
	s := withEmbedder(t, fe)
	save(t, s, SaveInput{Project: "p", Title: "t", Body: "b"})
	if _, err := s.EmbedPending(context.Background(), 10); !errors.Is(err, embed.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestUpdateReplacesVector(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: axis})
	id := save(t, s, SaveInput{Project: "p", Title: "ALPHA", Body: "one", TopicKey: "k"}).ID
	save(t, s, SaveInput{Project: "p", Title: "plain", Body: "two", TopicKey: "k"})
	var blob []byte
	if err := s.db.SQL().QueryRow(`select vec from vectors where memory_id=?`, id).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if v := decodeVec(blob); v[1] < 0.99 {
		t.Errorf("vector not refreshed after update: %v", v)
	}
	if c := count(t, s, `select count(*) from vectors`); c != 1 {
		t.Errorf("vectors = %d, want 1", c)
	}
}

func TestSaveHintsPossibleDuplicateWithoutBlocking(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: axis})
	first := save(t, s, SaveInput{Project: "p", Title: "ALPHA design", Body: "text one"})
	near := save(t, s, SaveInput{Project: "p", Title: "NEAR design", Body: "text two"})
	if near.Action != ActionCreated {
		t.Fatalf("near-duplicate must still be saved: %+v", near)
	}
	if near.Hint == nil || near.Hint.ID != first.ID || near.Hint.Score < DuplicateHintCosine {
		t.Fatalf("hint = %+v, want pointer to %d with score >= %v", near.Hint, first.ID, DuplicateHintCosine)
	}
	far := save(t, s, SaveInput{Project: "p", Title: "unrelated", Body: "text three"})
	if far.Hint != nil {
		t.Errorf("unrelated memory got a hint: %+v", far.Hint)
	}
	other := save(t, s, SaveInput{Project: "q", Title: "NEAR elsewhere", Body: "text four"})
	if other.Hint != nil {
		t.Errorf("hint crossed projects: %+v", other.Hint)
	}
	if n := count(t, s, `select count(*) from memories`); n != 4 {
		t.Errorf("rows = %d, want 4 (hint creates no rows)", n)
	}
}

func TestHintIgnoresSupersededMemories(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: axis})
	old := save(t, s, SaveInput{Project: "p", Title: "ALPHA design", Body: "x"})
	save(t, s, SaveInput{Project: "p", Title: "unrelated", Body: "y", Supersedes: old.ID})
	r := save(t, s, SaveInput{Project: "p", Title: "NEAR design", Body: "z"})
	if r.Hint != nil {
		t.Errorf("hint pointed at a superseded memory: %+v", r.Hint)
	}
}
