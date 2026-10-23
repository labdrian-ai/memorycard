package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

func TestGetReturnsMemoryWithHistoryAndCountsUse(t *testing.T) {
	s := newSvc(t)
	id := save(t, s, SaveInput{Project: "p", Title: "Auth", Body: "v1", TopicKey: "arch/auth"}).ID
	save(t, s, SaveInput{Project: "p", Title: "Auth", Body: "v2", TopicKey: "arch/auth"})

	m, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if m.Body != "v2" || m.Status != "active" || m.TopicKey != "arch/auth" {
		t.Errorf("got %+v", m)
	}
	if len(m.History) != 1 || m.History[0].Body != "v1" || m.History[0].Version != 1 {
		t.Errorf("history = %+v, want the v1 snapshot", m.History)
	}
	again, _ := s.Get(context.Background(), id)
	if again.UseCount != 2 {
		t.Errorf("use_count = %d, want 2 after two reads", again.UseCount)
	}
	if _, err := s.Get(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestUpdateEditSnapshotsAndKeepsRules(t *testing.T) {
	s := withEmbedder(t, &fakeEmbedder{fn: axis})
	id := save(t, s, SaveInput{Project: "p", Title: "ALPHA", Body: "old"}).ID

	if _, err := s.Update(context.Background(), id, UpdateInput{Body: ptr("new body")}); err != nil {
		t.Fatal(err)
	}
	m, _ := s.Get(context.Background(), id)
	if m.Body != "new body" || len(m.History) != 1 || m.History[0].Body != "old" {
		t.Errorf("after edit: %+v", m)
	}
	var blob []byte
	if err := s.db.SQL().QueryRow(`select vec from vectors where memory_id=?`, id).Scan(&blob); err != nil {
		t.Fatalf("vector missing after edit: %v", err)
	}

	big := strings.Repeat("x", HardCapBytes+1)
	if _, err := s.Update(context.Background(), id, UpdateInput{Body: &big}); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversize edit err = %v, want ErrTooLarge", err)
	}
	if _, err := s.Update(context.Background(), id, UpdateInput{Title: ptr("  ")}); !errors.Is(err, ErrEmpty) {
		t.Errorf("blank title err = %v, want ErrEmpty", err)
	}
	r, err := s.Update(context.Background(), id, UpdateInput{Body: ptr("a <private>secret</private> b")})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(context.Background(), r.ID); strings.Contains(got.Body, "secret") {
		t.Errorf("private text survived an edit: %q", got.Body)
	}
}

func TestUpdateEditToExistingContentIsRejectedAsDuplicate(t *testing.T) {
	s := newSvc(t)
	save(t, s, SaveInput{Project: "p", Title: "A", Body: "same"})
	b := save(t, s, SaveInput{Project: "p", Title: "B", Body: "other"}).ID
	_, err := s.Update(context.Background(), b, UpdateInput{Title: ptr("A"), Body: ptr("same")})
	if !errors.Is(err, ErrDuplicateContent) {
		t.Errorf("err = %v, want ErrDuplicateContent", err)
	}
}

func TestUpdatePinArchiveRestoreAndReview(t *testing.T) {
	s := newSvc(t)
	id := save(t, s, SaveInput{Project: "p", Type: "decision", Title: "cache", Body: "cache"}).ID
	ctx := context.Background()

	if _, err := s.Update(ctx, id, UpdateInput{Pinned: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.Get(ctx, id); !m.Pinned {
		t.Error("not pinned")
	}
	if _, err := s.Update(ctx, id, UpdateInput{Archive: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if hits := search(t, s, SearchInput{Query: "cache", Project: "p"}).Hits; len(hits) != 0 {
		t.Errorf("archived memory still searchable: %v", hits)
	}
	if _, err := s.Update(ctx, id, UpdateInput{Archive: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if hits := search(t, s, SearchInput{Query: "cache", Project: "p"}).Hits; len(hits) != 1 {
		t.Errorf("restored memory not searchable: %v", hits)
	}

	long := s.now().Add(-StaleAfter - time.Hour).UTC().Format(time.RFC3339Nano)
	s.db.SQL().Exec(`update memories set updated_at=? where id=?`, long, id)
	if h := search(t, s, SearchInput{Query: "cache", Project: "p"}).Hits; !h[0].Stale {
		t.Fatal("setup: expected stale")
	}
	if _, err := s.Update(ctx, id, UpdateInput{Reviewed: true}); err != nil {
		t.Fatal(err)
	}
	if h := search(t, s, SearchInput{Query: "cache", Project: "p"}).Hits; h[0].Stale {
		t.Error("mark reviewed did not clear the stale flag")
	}
	if m, _ := s.Get(ctx, id); len(m.History) != 0 {
		t.Errorf("review created a version: %+v", m.History)
	}
}

func TestUpdateUnknownIDAndEmptyChange(t *testing.T) {
	s := newSvc(t)
	if _, err := s.Update(context.Background(), 42, UpdateInput{Pinned: ptr(true)}); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	id := save(t, s, SaveInput{Project: "p", Title: "t", Body: "b"}).ID
	if _, err := s.Update(context.Background(), id, UpdateInput{}); !errors.Is(err, ErrNoChange) {
		t.Errorf("err = %v, want ErrNoChange", err)
	}
}

func TestContextOrdersPinnedStaleRecentWithinBudget(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	recent := save(t, s, SaveInput{Project: "p", Type: "discovery", Title: "recent note", Body: "r"}).ID
	stale := save(t, s, SaveInput{Project: "p", Type: "decision", Title: "stale decision", Body: "s"}).ID
	pinned := save(t, s, SaveInput{Project: "p", Type: "manual", Title: "pinned rule", Body: "p"}).ID
	other := save(t, s, SaveInput{Project: "q", Title: "other project", Body: "o"}).ID
	long := s.now().Add(-StaleAfter - time.Hour).UTC().Format(time.RFC3339Nano)
	s.db.SQL().Exec(`update memories set updated_at=? where id=?`, long, stale)
	if _, err := s.Update(ctx, pinned, UpdateInput{Pinned: ptr(true)}); err != nil {
		t.Fatal(err)
	}

	d, err := s.Context(ctx, "p", ContextBudgetBytes)
	if err != nil {
		t.Fatal(err)
	}
	var order []int64
	for _, e := range d.Entries {
		order = append(order, e.ID)
	}
	want := []int64{pinned, stale, recent}
	if len(order) != 3 || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] {
		t.Errorf("order = %v, want %v (and not %d)", order, want, other)
	}
	if d.Entries[0].Group != "pinned" || d.Entries[1].Group != "stale" || d.Entries[2].Group != "recent" {
		t.Errorf("groups = %+v", d.Entries)
	}
}

func TestContextRespectsByteBudgetAndReportsOmitted(t *testing.T) {
	s := newSvc(t)
	for i := 0; i < 40; i++ {
		save(t, s, SaveInput{Project: "p", Title: strings.Repeat("title ", 8) + string(rune('a'+i%26)) + string(rune('a'+i/26)), Body: "b" + string(rune('a'+i%26)) + string(rune('a'+i/26))})
	}
	d, err := s.Context(context.Background(), "p", 600)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(d.Text()); n > 600 {
		t.Errorf("digest is %d bytes, budget 600", n)
	}
	if d.Omitted == 0 || len(d.Entries)+d.Omitted != 40 {
		t.Errorf("entries=%d omitted=%d, want them to add up to 40", len(d.Entries), d.Omitted)
	}
}

func TestSessionLifecycleAndSummaryUpsert(t *testing.T) {
	s := newSvc(t)
	ctx := context.Background()
	if err := s.SessionStart(ctx, "s1", "p"); err != nil {
		t.Fatal(err)
	}
	if err := s.SessionStart(ctx, "s1", "p"); err != nil {
		t.Errorf("restarting a session must be idempotent: %v", err)
	}
	if n := count(t, s, `select count(*) from sessions`); n != 1 {
		t.Errorf("sessions = %d, want 1", n)
	}

	a, err := s.SessionSummary(ctx, "s1", "first draft")
	if err != nil || a.Action != ActionCreated {
		t.Fatalf("summary 1 = %+v, %v", a, err)
	}
	b, err := s.SessionSummary(ctx, "s1", "final summary")
	if err != nil || b.ID != a.ID || b.Action != ActionUpdated {
		t.Fatalf("summary 2 = %+v, %v; want an update of %d", b, err, a.ID)
	}
	m, _ := s.Get(ctx, a.ID)
	if m.TopicKey != "session/s1" || m.Type != "session_summary" || m.Body != "final summary" {
		t.Errorf("summary memory = %+v", m)
	}

	if err := s.SessionEnd(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	var ended *string
	s.db.SQL().QueryRow(`select ended_at from sessions where id='s1'`).Scan(&ended)
	if ended == nil {
		t.Error("ended_at not set")
	}
	if err := s.SessionEnd(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ending an unknown session err = %v, want ErrNotFound", err)
	}
	if _, err := s.SessionSummary(ctx, "nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("summary for an unknown session err = %v, want ErrNotFound", err)
	}
}
