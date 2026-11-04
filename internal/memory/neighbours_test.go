package memory

import (
	"context"
	"math"
	"regexp"
	"strconv"
	"testing"
)

var angleTag = regexp.MustCompile(`ANG(\d+)`)

// angle embeds a text holding "ANG<deg>" as the unit vector at that angle, so
// the cosine of two memories is cos(deg1-deg2). Texts without a tag point
// orthogonally away from every tagged one.
func angle(text string) []float32 {
	m := angleTag.FindStringSubmatch(text)
	if m == nil {
		return []float32{0, 0, 1}
	}
	d, _ := strconv.Atoi(m[1])
	r := float64(d) * math.Pi / 180
	return []float32{float32(math.Cos(r)), float32(math.Sin(r)), 0}
}

func hintIDs(hs []Hint) []int64 {
	var out []int64
	for _, h := range hs {
		out = append(out, h.ID)
	}
	return out
}

func TestSaveNamesClosestActiveMemoriesOfSameProjectAndType(t *testing.T) {
	ctx := context.Background()
	s := withEmbedder(t, &fakeEmbedder{fn: angle})
	at := func(project, typ, title string) int64 {
		return save(t, s, SaveInput{Project: project, Type: typ, Title: title, Body: "body " + title}).ID
	}
	a30 := at("p", "decision", "ANG30 thirty")      // cos 0.866
	a20 := at("p", "decision", "ANG20 twenty")      // cos 0.940
	a35 := at("p", "decision", "ANG35 thirty-five") // cos 0.819
	a40 := at("p", "decision", "ANG40 forty")       // cos 0.766, fourth: cut by the cap
	at("p", "decision", "ANG45 forty-five")         // cos 0.707, under the threshold
	at("other", "decision", "ANG21 other project")  // other project
	at("p", "discovery", "ANG22 other type")        // other type
	old := at("p", "decision", "ANG23 superseded")  // not active
	arch := at("p", "decision", "ANG24 archived")   // not active
	at("p", "decision", "plain, no tag")            // orthogonal
	yes := true
	if _, err := s.Update(ctx, arch, UpdateInput{Archive: &yes}); err != nil {
		t.Fatal(err)
	}
	newer := save(t, s, SaveInput{Project: "p", Type: "decision", Title: "ANG25 newer", Body: "replaces", Supersedes: old}).ID

	r := save(t, s, SaveInput{Project: "p", Type: "decision", Title: "ANG0 the new one", Body: "new"})
	if r.Action != ActionCreated {
		t.Fatalf("action = %s", r.Action)
	}
	got := hintIDs(r.Neighbours)
	// newer is ANG25 (cos 0.906); the cap keeps the closest three.
	want := []int64{a20, newer, a30}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("neighbours = %v, want %v", got, want)
	}
	if r.Neighbours[0].Title != "ANG20 twenty" || math.Abs(r.Neighbours[0].Score-math.Cos(20*math.Pi/180)) > 1e-4 {
		t.Errorf("first neighbour = %+v", r.Neighbours[0])
	}
	for i := 1; i < len(r.Neighbours); i++ {
		if r.Neighbours[i].Score > r.Neighbours[i-1].Score {
			t.Errorf("not closest first: %+v", r.Neighbours)
		}
	}
	_, _ = a35, a40
}

func TestNeighboursSkipTheDuplicateHintAndTheMemoryItself(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: angle})
	near := save(t, s, SaveInput{Project: "p", Type: "decision", Title: "ANG5 almost the same", Body: "x"}).ID
	far := save(t, s, SaveInput{Project: "p", Type: "decision", Title: "ANG30 related", Body: "y"}).ID
	r := save(t, s, SaveInput{Project: "p", Type: "decision", Title: "ANG0 new", Body: "z"})
	if r.Hint == nil || r.Hint.ID != near {
		t.Fatalf("hint = %+v, want #%d", r.Hint, near)
	}
	if got := hintIDs(r.Neighbours); len(got) != 1 || got[0] != far {
		t.Errorf("neighbours = %v, want [%d] (the hint is not repeated)", got, far)
	}
}

func TestNoNeighboursWhenNothingIsClose(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: angle})
	save(t, s, SaveInput{Project: "p", Type: "decision", Title: "ANG60 far", Body: "x"})
	r := save(t, s, SaveInput{Project: "p", Type: "decision", Title: "ANG0 new", Body: "z"})
	if r.Neighbours != nil || r.Hint != nil {
		t.Errorf("neighbours = %v hint = %v, want none", r.Neighbours, r.Hint)
	}
}

func TestDuplicateAndUnchangedSavesNameNoNeighbours(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: angle})
	save(t, s, SaveInput{Project: "p", Type: "decision", Title: "ANG30 related", Body: "y"})
	in := SaveInput{Project: "p", Type: "decision", Title: "ANG0 new", Body: "z", TopicKey: "k"}
	save(t, s, in)
	if r := save(t, s, in); r.Action != ActionUnchanged || r.Neighbours != nil {
		t.Errorf("unchanged: %+v", r)
	}
	in.TopicKey = ""
	if r := save(t, s, in); r.Action != ActionDuplicate || r.Neighbours != nil {
		t.Errorf("duplicate: %+v", r)
	}
}

func TestTopicUpdateAlsoNamesNeighbours(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: angle})
	rel := save(t, s, SaveInput{Project: "p", Type: "decision", Title: "ANG30 related", Body: "y"}).ID
	save(t, s, SaveInput{Project: "p", Type: "decision", Title: "plain", Body: "v1", TopicKey: "k"})
	r := save(t, s, SaveInput{Project: "p", Type: "decision", Title: "ANG0 now close", Body: "v2", TopicKey: "k"})
	if r.Action != ActionUpdated {
		t.Fatalf("action = %s", r.Action)
	}
	if got := hintIDs(r.Neighbours); len(got) != 1 || got[0] != rel {
		t.Errorf("neighbours = %v, want [%d]", got, rel)
	}
}

func TestSessionSummariesNameNoNeighbours(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: angle})
	save(t, s, SaveInput{Project: "p", Type: "session_summary", Title: "ANG10 earlier session", Body: "x"})
	r := save(t, s, SaveInput{Project: "p", Type: "session_summary", Title: "ANG0 this session", Body: "y"})
	if r.Neighbours != nil {
		t.Errorf("neighbours = %v, want none for session summaries", r.Neighbours)
	}
}

func TestNeighboursOfASplitSaveSkipItsOwnParts(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: angle})
	big := make([]byte, 0, HardCapBytes+2000)
	for len(big) < HardCapBytes+1000 {
		big = append(big, "ANG0 paragraph of text that repeats.\n\n"...)
	}
	r := save(t, s, SaveInput{Project: "p", Type: "decision", Title: "ANG0 big", Body: string(big), TopicKey: "big"})
	if len(r.Parts) < 2 {
		t.Fatalf("parts = %v", r.Parts)
	}
	if r.Neighbours != nil {
		t.Errorf("neighbours = %v, want none (own parts excluded)", r.Neighbours)
	}
}

func TestDeferEmbedThenOneBatch(t *testing.T) {
	ctx := context.Background()
	fe := &fakeEmbedder{fn: angle}
	s := withEmbedder(t, fe)
	var saved []int64
	for _, title := range []string{"ANG0 one", "ANG10 two", "ANG20 three"} {
		r := save(t, s, SaveInput{Project: "p", Type: "learning", Title: title, Body: "b " + title, DeferEmbed: true})
		if r.Hint != nil || r.Neighbours != nil || len(r.Warnings) != 0 {
			t.Errorf("deferred save reported %+v", r)
		}
		saved = append(saved, r.ID)
	}
	if len(fe.kinds) != 0 {
		t.Fatalf("deferred saves called the embedder %d times", len(fe.kinds))
	}
	var n int
	s.db.SQL().QueryRow(`select count(*) from vectors`).Scan(&n)
	if n != 0 {
		t.Fatalf("vectors after deferred saves = %d", n)
	}
	got, err := s.EmbedMemories(ctx, saved)
	if err != nil || got != 3 {
		t.Fatalf("EmbedMemories = %d, %v", got, err)
	}
	if len(fe.kinds) != 1 || len(fe.seen) != 3 {
		t.Errorf("embed calls = %d texts = %d, want one call with 3 texts", len(fe.kinds), len(fe.seen))
	}
	s.db.SQL().QueryRow(`select count(*) from vectors`).Scan(&n)
	if n != 3 {
		t.Errorf("vectors = %d, want 3", n)
	}
	// Already embedded: a second call embeds nothing and calls nothing.
	if got, err := s.EmbedMemories(ctx, saved); err != nil || got != 0 || len(fe.kinds) != 1 {
		t.Errorf("second EmbedMemories = %d, %v, calls %d", got, err, len(fe.kinds))
	}
}

func TestEmbedMemoriesFailureLeavesThemPending(t *testing.T) {
	ctx := context.Background()
	fe := &fakeEmbedder{fn: angle}
	s := withEmbedder(t, fe)
	id := save(t, s, SaveInput{Project: "p", Title: "ANG0 one", Body: "b", DeferEmbed: true}).ID
	fe.fail = true
	if _, err := s.EmbedMemories(ctx, []int64{id}); err == nil {
		t.Fatal("want the embedder error")
	}
	fe.fail = false
	if n, err := s.EmbedPending(ctx, 10); err != nil || n != 1 {
		t.Errorf("EmbedPending = %d, %v; want the memory still pending", n, err)
	}
}

func TestEmbedMemoriesWithoutEmbedderIsANoOp(t *testing.T) {
	s := newSvc(t)
	id := save(t, s, SaveInput{Project: "p", Title: "t", Body: "b", DeferEmbed: true}).ID
	if n, err := s.EmbedMemories(context.Background(), []int64{id}); n != 0 || err != nil {
		t.Errorf("EmbedMemories = %d, %v", n, err)
	}
}
