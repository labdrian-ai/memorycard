package memory

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/labdrian-ai/memorycard/internal/embed"
)

// batchCounter records the size of every embedder request.
type batchCounter struct {
	inner      *fakeEmbedder
	max, texts int
}

func (b *batchCounter) Model() string { return b.inner.Model() }
func (b *batchCounter) Embed(ctx context.Context, texts []string, kind embed.Kind) ([][]float32, error) {
	if len(texts) > b.max {
		b.max = len(texts)
	}
	b.texts += len(texts)
	return b.inner.Embed(ctx, texts, kind)
}

// longBody is n runes of filler with marker placed at rune offset at.
func longBody(n, at int, marker string) string {
	r := []rune(strings.Repeat("x", n))
	if marker != "" {
		copy(r[at:], []rune(marker))
	}
	return string(r)
}

func chunkCount(t *testing.T, s *Service, id int64) int {
	t.Helper()
	return count(t, s, `select count(*) from vector_chunks where memory_id=?`, id)
}

// wantChunks is how many vectors a body of n runes gets: one window, then one
// more per step of EmbedBodyRunes-ChunkOverlapRunes until the end is covered.
func wantChunks(n int) int {
	if n <= EmbedBodyRunes {
		return 1
	}
	step := EmbedBodyRunes - ChunkOverlapRunes
	return 1 + (n-EmbedBodyRunes+step-1)/step
}

func TestLongMemoryGetsOneVectorPerChunk(t *testing.T) {
	fe := &fakeEmbedder{fn: axis}
	s := withEmbedder(t, fe)
	n := 4000
	body := longBody(n, n-10, "TAIL")
	id := save(t, s, SaveInput{Project: "p", Title: "T", Body: body}).ID

	chunks := wantChunks(n)
	if chunks < 3 {
		t.Fatalf("test body too short: %d chunks", chunks)
	}
	if len(fe.seen) != chunks {
		t.Fatalf("embedded %d texts, want %d chunks", len(fe.seen), chunks)
	}
	for i, text := range fe.seen {
		if !strings.HasPrefix(text, "T\n") {
			t.Errorf("chunk %d not prefixed with the title: %q", i, text[:10])
		}
		if r := utf8.RuneCountInString(text) - 2; r > EmbedBodyRunes {
			t.Errorf("chunk %d has %d body runes, window is %d", i, r, EmbedBodyRunes)
		}
	}
	// Chunk 0 is exactly what the single vector always embedded.
	if fe.seen[0] != embedText("T", body) {
		t.Error("first chunk differs from the single-vector embed text")
	}
	if !strings.HasSuffix(fe.seen[chunks-1], "TAIL"+strings.Repeat("x", 6)) {
		t.Error("last chunk does not reach the end of the body")
	}
	// Consecutive chunks overlap by ChunkOverlapRunes.
	b := []rune(body)
	step := EmbedBodyRunes - ChunkOverlapRunes
	if want := "T\n" + string(b[step:step+EmbedBodyRunes]); fe.seen[1] != want {
		t.Error("second chunk does not start ChunkOverlapRunes before the end of the first")
	}

	if c := count(t, s, `select count(*) from vectors where memory_id=?`, id); c != 1 {
		t.Errorf("vectors rows = %d, want 1 (chunk 0)", c)
	}
	if c := chunkCount(t, s, id); c != chunks-1 {
		t.Errorf("vector_chunks rows = %d, want %d", c, chunks-1)
	}
}

// Previous behaviour: a memory that fits the window gets one vector, no chunks.
func TestShortMemoryKeepsOneVectorAndNoChunks(t *testing.T) {
	fe := &fakeEmbedder{fn: axis}
	s := withEmbedder(t, fe)
	id := save(t, s, SaveInput{Project: "p", Title: "T", Body: longBody(EmbedBodyRunes, 0, "")}).ID
	if len(fe.seen) != 1 || fe.seen[0] != embedText("T", longBody(EmbedBodyRunes, 0, "")) {
		t.Fatalf("embedded %d texts, want 1 whole-memory text", len(fe.seen))
	}
	if c := count(t, s, `select count(*) from vectors where memory_id=?`, id); c != 1 {
		t.Errorf("vectors rows = %d, want 1", c)
	}
	if c := chunkCount(t, s, id); c != 0 {
		t.Errorf("vector_chunks rows = %d, want 0", c)
	}
}

func TestDenseScoresAMemoryByItsBestChunk(t *testing.T) {
	fe := &fakeEmbedder{fn: axis}
	s := withEmbedder(t, fe)
	s.SetHybrid(false)
	// The answer sits past the first window; only a later chunk carries it.
	deep := save(t, s, SaveInput{Project: "p", Title: "deep", Body: longBody(3000, 2500, "NEAR")}).ID
	plain := save(t, s, SaveInput{Project: "p", Title: "plain", Body: "nothing"}).ID

	r := search(t, s, SearchInput{Project: "p", Query: "ALPHA"})
	if r.Mode != ModeDense {
		t.Fatalf("mode = %s, want dense", r.Mode)
	}
	if got := ids(r); len(got) != 2 || got[0] != deep {
		t.Fatalf("ranking = %v, want %d (best chunk) before %d", got, deep, plain)
	}
	if r.Hits[0].Score < 0.99 {
		t.Errorf("top score = %f, want the best chunk's cosine (~0.995)", r.Hits[0].Score)
	}
	if r.Confidence != ConfidenceOK {
		t.Errorf("confidence = %s, want ok from the best chunk", r.Confidence)
	}
}

func TestEditReplacesAllChunks(t *testing.T) {
	ctx := context.Background()
	s := withEmbedder(t, &fakeEmbedder{fn: axis})
	id := save(t, s, SaveInput{Project: "p", Title: "T", Body: longBody(5000, 0, ""), TopicKey: "k"}).ID
	if c := chunkCount(t, s, id); c != wantChunks(5000)-1 {
		t.Fatalf("chunks = %d, want %d", c, wantChunks(5000)-1)
	}

	// Shorter but still long: exactly the new number of chunks remains.
	save(t, s, SaveInput{Project: "p", Title: "T", Body: longBody(2000, 0, ""), TopicKey: "k"})
	if c := chunkCount(t, s, id); c != wantChunks(2000)-1 {
		t.Errorf("chunks after shrinking = %d, want %d", c, wantChunks(2000)-1)
	}

	// Short: no chunk is left behind.
	body := "short now"
	if _, err := s.Update(ctx, id, UpdateInput{Body: &body}); err != nil {
		t.Fatal(err)
	}
	if c := chunkCount(t, s, id); c != 0 {
		t.Errorf("chunks after the body became short = %d, want 0", c)
	}
	if c := count(t, s, `select count(*) from vectors where memory_id=?`, id); c != 1 {
		t.Errorf("vectors rows = %d, want 1", c)
	}
}

// A failed re-embed after an edit leaves neither the old vector nor its chunks.
func TestFailedReembedDropsChunksAndPendingRestoresThem(t *testing.T) {
	ctx := context.Background()
	fe := &fakeEmbedder{fn: axis}
	s := withEmbedder(t, fe)
	id := save(t, s, SaveInput{Project: "p", Title: "T", Body: longBody(3000, 0, "")}).ID

	fe.fail = true
	body := longBody(4000, 0, "")
	r, err := s.Update(ctx, id, UpdateInput{Body: &body})
	if err != nil || len(r.Warnings) == 0 {
		t.Fatalf("update = %+v, %v; want an embedding-pending warning", r, err)
	}
	if c := chunkCount(t, s, id); c != 0 {
		t.Errorf("stale chunks kept after a failed re-embed: %d", c)
	}

	fe.fail = false
	if n, err := s.EmbedPending(ctx, 10); err != nil || n != 1 {
		t.Fatalf("EmbedPending = %d, %v; want 1", n, err)
	}
	if c := chunkCount(t, s, id); c != wantChunks(4000)-1 {
		t.Errorf("chunks after EmbedPending = %d, want %d", c, wantChunks(4000)-1)
	}
}

// A memory migrated with only its single vector gets its remaining chunks from
// EmbedPending, without re-embedding the window it already has.
func TestEmbedPendingAddsChunksToSingleVectorMemories(t *testing.T) {
	ctx := context.Background()
	fe := &fakeEmbedder{fn: axis}
	s := withEmbedder(t, fe)
	long := save(t, s, SaveInput{Project: "p", Title: "T", Body: longBody(3000, 0, "")}).ID
	save(t, s, SaveInput{Project: "p", Title: "short", Body: "b"})
	if _, err := s.db.SQL().Exec(`delete from vector_chunks`); err != nil {
		t.Fatal(err)
	}
	var before []byte
	if err := s.db.SQL().QueryRow(`select vec from vectors where memory_id=?`, long).Scan(&before); err != nil {
		t.Fatal(err)
	}

	fe.seen = nil
	if n, err := s.EmbedPending(ctx, 10); err != nil || n != 1 {
		t.Fatalf("EmbedPending = %d, %v; want 1", n, err)
	}
	if len(fe.seen) != wantChunks(3000)-1 {
		t.Errorf("embedded %d texts, want only the %d missing chunks", len(fe.seen), wantChunks(3000)-1)
	}
	if c := chunkCount(t, s, long); c != wantChunks(3000)-1 {
		t.Errorf("chunks = %d, want %d", c, wantChunks(3000)-1)
	}
	var after []byte
	if err := s.db.SQL().QueryRow(`select vec from vectors where memory_id=?`, long).Scan(&after); err != nil || string(after) != string(before) {
		t.Errorf("single vector changed by chunk backfill (%v)", err)
	}
	if n, err := s.EmbedPending(ctx, 10); err != nil || n != 0 {
		t.Errorf("second EmbedPending = %d, %v; want 0", n, err)
	}
}

// EmbedPending never sends more than limit texts in one embedder request.
func TestEmbedPendingBatchesChunksByLimit(t *testing.T) {
	ctx := context.Background()
	fe := &fakeEmbedder{fn: axis, fail: true}
	s := withEmbedder(t, fe)
	save(t, s, SaveInput{Project: "p", Title: "T", Body: longBody(6000, 0, "")})
	fe.fail = false
	calls := &batchCounter{inner: fe}
	s.SetEmbedder(calls)
	if n, err := s.EmbedPending(ctx, 2); err != nil || n != 1 {
		t.Fatalf("EmbedPending = %d, %v; want 1", n, err)
	}
	if calls.max > 2 {
		t.Errorf("largest embedder request = %d texts, limit is 2", calls.max)
	}
	if calls.texts != wantChunks(6000) {
		t.Errorf("embedded %d texts, want %d", calls.texts, wantChunks(6000))
	}
}
