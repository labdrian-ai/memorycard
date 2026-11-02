package memory

import (
	"context"
	"strings"
	"testing"
)

func TestSaveEmbedsEveryChunkOfALongBody(t *testing.T) {
	fe := &fakeEmbedder{fn: axis}
	s := withEmbedder(t, fe)
	body := strings.Repeat("a", EmbedBodyRunes) + strings.Repeat("é", EmbedBodyRunes) + "tail"
	id := save(t, s, SaveInput{Project: "p", Title: "T", Body: body}).ID

	want := []string{
		"T\n" + strings.Repeat("a", EmbedBodyRunes),
		"T\n" + strings.Repeat("é", EmbedBodyRunes),
		"T\ntail",
	}
	if len(fe.seen) != len(want) {
		t.Fatalf("embedded %d texts, want %d", len(fe.seen), len(want))
	}
	for i := range want {
		if fe.seen[i] != want[i] {
			t.Errorf("chunk %d embedded %.20q..., want %.20q...", i, fe.seen[i], want[i])
		}
	}
	if n := count(t, s, `select count(*) from vectors where memory_id = ?`, id); n != 1 {
		t.Errorf("vectors rows = %d, want 1 (chunk 0)", n)
	}
	if n := count(t, s, `select count(*) from chunk_vectors where memory_id = ? and model = 'fake' and chunk in (1, 2)`, id); n != 2 {
		t.Errorf("chunk_vectors rows = %d, want chunks 1 and 2", n)
	}
}

func TestSearchFindsEvidenceBeyondTheFirstChunk(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: axis})
	deep := save(t, s, SaveInput{Project: "p", Title: "long chat", Body: strings.Repeat("x ", EmbedBodyRunes) + "NEAR the workshop"}).ID
	save(t, s, SaveInput{Project: "p", Title: "short chat", Body: "nothing here"})

	r, err := s.Search(context.Background(), SearchInput{Project: "p", Query: "NEAR"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Hits) == 0 || r.Hits[0].ID != deep {
		t.Fatalf("hits %+v, want memory %d first", r.Hits, deep)
	}
	if r.Hits[0].Score < 0.99 {
		t.Errorf("score %.3f, want the cosine of its best chunk", r.Hits[0].Score)
	}
	if r.Confidence != ConfidenceOK {
		t.Errorf("confidence %q, want ok", r.Confidence)
	}
}

func TestUpdateReplacesOldChunks(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: axis})
	id := save(t, s, SaveInput{Project: "p", Title: "T", Body: strings.Repeat("a", 3*EmbedBodyRunes), TopicKey: "k"}).ID
	if n := count(t, s, `select count(*) from chunk_vectors where memory_id = ?`, id); n != 2 {
		t.Fatalf("chunk_vectors = %d, want 2", n)
	}
	save(t, s, SaveInput{Project: "p", Title: "T", Body: "short now", TopicKey: "k"})
	if n := count(t, s, `select count(*) from chunk_vectors where memory_id = ?`, id); n != 0 {
		t.Errorf("chunk_vectors after shortening = %d, want 0", n)
	}
}

func TestEmbedPendingAddsMissingChunksToAnExistingStore(t *testing.T) {
	fe := &fakeEmbedder{fn: axis}
	s := withEmbedder(t, fe)
	long := save(t, s, SaveInput{Project: "p", Title: "T", Body: strings.Repeat("a", 2*EmbedBodyRunes+1)}).ID
	save(t, s, SaveInput{Project: "p", Title: "S", Body: "short"})
	// What a store embedded by the v3 build looks like: chunk 0 only.
	if _, err := s.db.SQL().Exec(`delete from chunk_vectors`); err != nil {
		t.Fatal(err)
	}
	st, err := s.Stats(context.Background())
	if err != nil || st.WithoutEmbedding != 1 {
		t.Fatalf("without embedding = %d, %v; want 1", st.WithoutEmbedding, err)
	}

	n, err := s.EmbedPending(context.Background(), 10)
	if err != nil || n != 1 {
		t.Fatalf("EmbedPending = %d, %v; want 1", n, err)
	}
	if c := count(t, s, `select count(*) from chunk_vectors where memory_id = ?`, long); c != 2 {
		t.Errorf("chunk_vectors = %d, want 2", c)
	}
	if n, err := s.EmbedPending(context.Background(), 10); err != nil || n != 0 {
		t.Errorf("second EmbedPending = %d, %v; want 0", n, err)
	}
	if st, _ := s.Stats(context.Background()); st.WithoutEmbedding != 0 {
		t.Errorf("without embedding after catching up = %d, want 0", st.WithoutEmbedding)
	}
}

func TestMaxChunksCapsHowMuchOfTheBodyIsEmbedded(t *testing.T) {
	fe := &fakeEmbedder{fn: axis}
	s := withEmbedder(t, fe)
	s.SetMaxChunks(2)
	id := save(t, s, SaveInput{Project: "p", Title: "T", Body: strings.Repeat("a", 4*EmbedBodyRunes)}).ID
	if len(fe.seen) != 2 {
		t.Errorf("embedded %d texts, want 2", len(fe.seen))
	}
	if c := count(t, s, `select count(*) from chunk_vectors where memory_id = ?`, id); c != 1 {
		t.Errorf("chunk_vectors = %d, want 1", c)
	}
	if n, err := s.EmbedPending(context.Background(), 10); err != nil || n != 0 {
		t.Errorf("EmbedPending = %d, %v; want 0 under the cap", n, err)
	}
}
