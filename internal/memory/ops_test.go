package memory

import (
	"context"
	"errors"
	"fmt"
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

func digest(t *testing.T, s *Service, project string) Digest {
	t.Helper()
	d, err := s.Context(context.Background(), project, ContextBudgetBytes)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestContextShowsOneLinePerDocumentNotPerPart(t *testing.T) {
	s := newSvc(t)
	var body strings.Builder
	for i := 0; body.Len() < 3*HardCapBytes; i++ {
		body.WriteString(strings.Repeat("paragraph text about the long design ", 20) + "\n\n")
	}
	r := save(t, s, SaveInput{Project: "p", Type: "architecture", Title: "Long design", Body: body.String(), TopicKey: "design/long"})
	if len(r.Parts) < 3 {
		t.Fatalf("parts = %v, want the body split in 3 or more", r.Parts)
	}
	note := save(t, s, SaveInput{Project: "p", Type: "discovery", Title: "short note", Body: "n"}).ID

	d := digest(t, s, "p")
	if len(d.Entries) != 2 {
		t.Fatalf("entries = %+v, want the document once plus the note", d.Entries)
	}
	doc := d.Entries[1]
	if d.Entries[0].ID != note || doc.ID != r.Parts[0] || doc.Parts != len(r.Parts) || doc.Title != "Long design" {
		t.Errorf("entries = %+v, want note #%d then part 1 #%d titled %q with %d parts", d.Entries, note, r.Parts[0], "Long design", len(r.Parts))
	}
	text := d.Text()
	want := fmt.Sprintf("#%d [recent] architecture: Long design (%d parts)\n", r.Parts[0], len(r.Parts))
	if !strings.Contains(text, want) || strings.Contains(text, "(part ") {
		t.Errorf("text = %q, want the line %q and no per-part line", text, want)
	}
	if d.Omitted != 0 {
		t.Errorf("omitted = %d, want 0", d.Omitted)
	}
}

func TestContextCapsSessionSummariesAndMirrorsAtOneLineEach(t *testing.T) {
	s := newSvc(t)
	var sums, mirrors []int64
	for i := 0; i < 3; i++ {
		sums = append(sums, save(t, s, SaveInput{Project: "p", Type: "session_summary", Title: fmt.Sprintf("Session summary %d", i), Body: fmt.Sprintf("s%d", i)}).ID)
	}
	for i, key := range []string{"sdd/change-a/design", "odd/feature-b/tasks", "project/app/manifest/context"} {
		mirrors = append(mirrors, save(t, s, SaveInput{Project: "p", Type: "architecture", Title: key, Body: fmt.Sprintf("m%d", i), TopicKey: key}).ID)
	}
	real := save(t, s, SaveInput{Project: "p", Type: "decision", Title: "Pivot to demo-first", Body: "d", TopicKey: "project/app/demo-first"}).ID
	note := save(t, s, SaveInput{Project: "p", Type: "discovery", Title: "plain note", Body: "n"}).ID

	d := digest(t, s, "p")
	var got []int64
	for _, e := range d.Entries {
		got = append(got, e.ID)
	}
	want := []int64{note, real, mirrors[2], sums[2]}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("entries = %v, want %v (newest of each kind once, other notes kept)", got, want)
	}
	text := d.Text()
	for _, line := range []string{
		fmt.Sprintf("#%d [recent] architecture: project/app/manifest/context (+2 more mirrors)\n", mirrors[2]),
		fmt.Sprintf("#%d [recent] session_summary: Session summary 2 (+2 more session summaries)\n", sums[2]),
		fmt.Sprintf("#%d [recent] decision: Pivot to demo-first\n", real),
	} {
		if !strings.Contains(text, line) {
			t.Errorf("text = %q, missing %q", text, line)
		}
	}
	if d.Omitted != 0 {
		t.Errorf("omitted = %d, want 0 (capped kinds are summarised, not omitted)", d.Omitted)
	}
}

func TestContextKeepsTitledArchitectureNotesUnderProject(t *testing.T) {
	s := newSvc(t)
	a := save(t, s, SaveInput{Project: "p", Type: "architecture", Title: "ADR-001: data in three layers", Body: "a", TopicKey: "project/app/architecture/adr-001"}).ID
	b := save(t, s, SaveInput{Project: "p", Type: "architecture", Title: "Technical architecture", Body: "b", TopicKey: "project/app/architecture"}).ID
	d := digest(t, s, "p")
	var got []int64
	for _, e := range d.Entries {
		got = append(got, e.ID)
		if e.MoreKind != "" {
			t.Errorf("entry %d folded as %q, want a plain line", e.ID, e.MoreKind)
		}
	}
	if want := []int64{b, a}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("entries = %v, want %v (hand-titled architecture notes under project/ each keep a line)", got, want)
	}
}

func TestContextCapsTheStaleTierAtThreeLines(t *testing.T) {
	s := newSvc(t)
	long := s.now().Add(-StaleAfter - time.Hour).UTC().Format(time.RFC3339Nano)
	var stale []int64
	for i := 0; i < 5; i++ {
		id := save(t, s, SaveInput{Project: "p", Type: "decision", Title: fmt.Sprintf("old decision %d", i), Body: fmt.Sprintf("d%d", i)}).ID
		if _, err := s.db.SQL().Exec(`update memories set updated_at=? where id=?`, long, id); err != nil {
			t.Fatal(err)
		}
		stale = append(stale, id)
	}
	recent := save(t, s, SaveInput{Project: "p", Type: "discovery", Title: "fresh note", Body: "f"}).ID

	d := digest(t, s, "p")
	var groups []string
	for _, e := range d.Entries {
		groups = append(groups, e.Group)
	}
	if fmt.Sprint(groups) != "[stale stale stale recent]" || d.Entries[3].ID != recent {
		t.Fatalf("entries = %+v, want 3 stale lines then the recent note", d.Entries)
	}
	if d.StaleOmitted != 2 || d.Omitted != 0 {
		t.Errorf("stale omitted = %d, omitted = %d, want 2 and 0", d.StaleOmitted, d.Omitted)
	}
	text := d.Text()
	footer := "... 2 more stale (use mem_search)\n"
	if i, j := strings.Index(text, footer), strings.Index(text, "fresh note"); i < 0 || j < i {
		t.Errorf("text = %q, want %q after the stale lines and before the recent ones", text, footer)
	}
	if strings.Contains(text, "... 2 more (use") {
		t.Errorf("text = %q, the stale rest must not count as budget-omitted", text)
	}
}

func TestContextSearchReturnsTheTopFiveHitsOfTheProject(t *testing.T) {
	s := newSvc(t)
	for i := 0; i < 7; i++ {
		save(t, s, SaveInput{Project: "p", Title: fmt.Sprintf("canary step %d", i), Body: fmt.Sprintf("canary rollout %d", i)})
	}
	save(t, s, SaveInput{Project: "q", Title: "canary elsewhere", Body: "canary in another project"})
	r, err := s.ContextSearch(context.Background(), "p", "canary rollout")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Hits) != DigestHitsK {
		t.Fatalf("hits = %d, want %d", len(r.Hits), DigestHitsK)
	}
	for _, h := range r.Hits {
		if h.Project != "p" || h.Snippet != "" {
			t.Errorf("hit %+v, want project p with title only", h)
		}
	}
	if _, err := s.ContextSearch(context.Background(), "p", "  "); !errors.Is(err, ErrEmptyQuery) {
		t.Errorf("blank query err = %v, want ErrEmptyQuery", err)
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
