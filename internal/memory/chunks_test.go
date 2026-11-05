package memory

import (
	"context"
	"fmt"
	"math"
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

func TestDeferredLongMemoryGetsItsChunksInTheOneBatch(t *testing.T) {
	fe := &fakeEmbedder{fn: axis}
	s := withEmbedder(t, fe)
	n := 4000
	long := save(t, s, SaveInput{Project: "p", Title: "T", Body: longBody(n, 0, ""), DeferEmbed: true}).ID
	short := save(t, s, SaveInput{Project: "p", Title: "S", Body: "short", DeferEmbed: true}).ID
	got, err := s.EmbedMemories(context.Background(), []int64{long, short})
	if err != nil || got != 2 {
		t.Fatalf("EmbedMemories = %d, %v, want 2", got, err)
	}
	chunks := wantChunks(n)
	if len(fe.kinds) != 1 || len(fe.seen) != chunks+1 {
		t.Errorf("embed calls = %d texts = %d, want one call with %d texts", len(fe.kinds), len(fe.seen), chunks+1)
	}
	if c := chunkCount(t, s, long); c != chunks-1 {
		t.Errorf("long memory vector_chunks rows = %d, want %d", c, chunks-1)
	}
	if c := chunkCount(t, s, short); c != 0 {
		t.Errorf("short memory vector_chunks rows = %d, want 0", c)
	}
	if c := count(t, s, `select count(*) from vectors`); c != 2 {
		t.Errorf("vectors rows = %d, want 2", c)
	}
}

func TestDeferredBatchSplitsRequestsAtCaptureEmbedBatch(t *testing.T) {
	fe := &fakeEmbedder{fn: axis}
	calls := &batchCounter{inner: fe}
	s := withEmbedder(t, fe)
	s.SetEmbedder(calls)
	var ids []int64
	for i := 0; i < 3; i++ {
		ids = append(ids, save(t, s, SaveInput{Project: "p", Title: fmt.Sprintf("L%d", i), Body: longBody(16000, 0, ""), DeferEmbed: true}).ID)
	}
	got, err := s.EmbedMemories(context.Background(), ids)
	if err != nil || got != 3 {
		t.Fatalf("EmbedMemories = %d, %v, want 3", got, err)
	}
	if calls.max > CaptureEmbedBatch {
		t.Errorf("largest request = %d texts, want at most %d", calls.max, CaptureEmbedBatch)
	}
	for _, id := range ids {
		if c := chunkCount(t, s, id); c != wantChunks(16000)-1 {
			t.Errorf("memory %d vector_chunks rows = %d, want %d", id, c, wantChunks(16000)-1)
		}
	}
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

func TestDenseLiftsAMemoryByItsBestChunk(t *testing.T) {
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
		t.Fatalf("ranking = %v, want %d (lifted by its best chunk) before %d", got, deep, plain)
	}
	// First window cosine 0, best chunk cosine ~0.995: the score moves a
	// ChunkLift share of the way from the first to the best.
	best := 1 / math.Sqrt(1.01)
	if want := ChunkLift * best; math.Abs(r.Hits[0].Score-want) > 1e-6 {
		t.Errorf("top score = %f, want %f (first window + ChunkLift x the gap to the best chunk)", r.Hits[0].Score, want)
	}
	if r.Hits[1].Score != 0 {
		t.Errorf("plain score = %f, want its own cosine 0", r.Hits[1].Score)
	}
	// The score stays on the first-window scale the confidence threshold was
	// calibrated on, so a match that only a late chunk carries is low confidence.
	if r.Confidence != ConfidenceLow {
		t.Errorf("confidence = %s, want low", r.Confidence)
	}
}

// halfAxis extends axis with HALF, a text half-way between the ALPHA query and
// filler (cosine ~0.707 to ALPHA).
func halfAxis(text string) []float32 {
	if strings.Contains(text, "HALF") {
		return []float32{1, 1, 0}
	}
	return axis(text)
}

// Regression for the T6 benchmark loss: scored by the plain maximum over its
// chunks, a long note with one strong late chunk overtook a short note that
// answers the query as a whole. A late chunk may lift a long note above other
// long notes, never past a note matched better by its first window.
func TestLateChunkDoesNotOvertakeABetterFirstWindow(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: halfAxis})
	s.SetHybrid(false)
	short := save(t, s, SaveInput{Project: "p", Title: "short", Body: "HALF"}).ID
	deep := save(t, s, SaveInput{Project: "p", Title: "deep", Body: longBody(3000, 2500, "NEAR")}).ID
	other := save(t, s, SaveInput{Project: "p", Title: "other", Body: longBody(3000, 0, "")}).ID

	r := search(t, s, SearchInput{Project: "p", Query: "ALPHA"})
	if got := ids(r); len(got) != 3 || got[0] != short || got[1] != deep || got[2] != other {
		t.Fatalf("ranking = %v, want short %d, deep %d, other %d", got, short, deep, other)
	}
	// A memory without chunks keeps exactly its single-vector cosine.
	if want := 1 / math.Sqrt2; math.Abs(r.Hits[0].Score-want) > 1e-6 {
		t.Errorf("short score = %f, want its cosine %f", r.Hits[0].Score, want)
	}
	if r.Hits[2].Score != 0 {
		t.Errorf("other score = %f, want 0: none of its chunks match", r.Hits[2].Score)
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
