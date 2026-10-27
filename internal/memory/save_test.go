package memory

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labdrian-ai/memorycard/internal/store"
)

func newSvc(t *testing.T) *Service {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := New(db)
	tick := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { tick = tick.Add(time.Second); return tick }
	return s
}

func save(t *testing.T, s *Service, in SaveInput) SaveResult {
	t.Helper()
	r, err := s.Save(context.Background(), in)
	if err != nil {
		t.Fatalf("Save(%+v): %v", in, err)
	}
	return r
}

func count(t *testing.T, s *Service, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.SQL().QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSaveCreates(t *testing.T) {
	s := newSvc(t)
	r := save(t, s, SaveInput{Project: "p", Title: "Cache policy", Body: "Use LRU with a 5 minute TTL."})
	if r.Action != ActionCreated || r.ID != 1 {
		t.Fatalf("got %+v, want created id 1", r)
	}
	var scope, typ, status string
	if err := s.db.SQL().QueryRow(`select scope, type, status from memories where id=1`).Scan(&scope, &typ, &status); err != nil {
		t.Fatal(err)
	}
	if scope != "project" || typ != "manual" || status != "active" {
		t.Errorf("defaults scope=%q type=%q status=%q", scope, typ, status)
	}
}

func TestSaveRejectsInvalidInput(t *testing.T) {
	s := newSvc(t)
	cases := map[string]SaveInput{
		"empty title":   {Project: "p", Title: "  ", Body: "b"},
		"empty body":    {Project: "p", Title: "t", Body: "\n "},
		"no project":    {Title: "t", Body: "b"},
		"bad scope":     {Project: "p", Title: "t", Body: "b", Scope: "galaxy"},
		"only redacted": {Project: "p", Title: "t", Body: "<private>secret</private>"},
	}
	for name, in := range cases {
		if _, err := s.Save(context.Background(), in); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if n := count(t, s, `select count(*) from memories`); n != 0 {
		t.Errorf("rejected saves stored %d rows", n)
	}
}

func TestSaveSizeCaps(t *testing.T) {
	s := newSvc(t)
	// Bodies over the hard cap are split into parts (save_split_test.go); one
	// at the cap is still stored whole.
	r := save(t, s, SaveInput{Project: "p", Title: "at cap", Body: strings.Repeat("a", HardCapBytes)})
	if len(r.Warnings) == 0 {
		t.Error("body above the soft target must warn")
	}
	var got int
	if err := s.db.SQL().QueryRow(`select length(cast(body as blob)) from memories where id=?`, r.ID).Scan(&got); err != nil || got != HardCapBytes {
		t.Errorf("stored %d bytes err=%v, want %d (no silent truncation)", got, err, HardCapBytes)
	}

	small := save(t, s, SaveInput{Project: "p", Title: "small", Body: "short body"})
	if len(small.Warnings) != 0 {
		t.Errorf("small body warned: %v", small.Warnings)
	}
}

func TestSaveRedactsPrivateText(t *testing.T) {
	s := newSvc(t)
	r := save(t, s, SaveInput{Project: "p", Title: "Key <private>abc</private>", Body: "token=<private>hunter2</private> ok"})
	var title, body string
	if err := s.db.SQL().QueryRow(`select title, body from memories where id=?`, r.ID).Scan(&title, &body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(title+body, "abc") || strings.Contains(title+body, "hunter2") {
		t.Errorf("private text leaked: %q %q", title, body)
	}
	if !strings.Contains(body, "[REDACTED]") {
		t.Errorf("redaction marker missing: %q", body)
	}
}

func TestSaveExactDuplicateWritesNothing(t *testing.T) {
	s := newSvc(t)
	first := save(t, s, SaveInput{Project: "p", Title: "Same", Body: "Same body here"})
	again := save(t, s, SaveInput{Project: "p", Title: "same", Body: "  same   BODY here "})
	if again.Action != ActionDuplicate || again.ID != first.ID {
		t.Fatalf("got %+v, want duplicate of %d", again, first.ID)
	}
	if n := count(t, s, `select count(*) from memories`); n != 1 {
		t.Errorf("rows=%d, want 1", n)
	}
	other := save(t, s, SaveInput{Project: "q", Title: "Same", Body: "Same body here"})
	if other.Action != ActionCreated || other.ID == first.ID {
		t.Errorf("same text in another project must be created: %+v", other)
	}
}

func TestSaveTopicKeyUpsertAndVersions(t *testing.T) {
	s := newSvc(t)
	first := save(t, s, SaveInput{Project: "p", Title: "Auth model", Body: "v1 body", TopicKey: "My Topic  Key"})
	if n := count(t, s, `select count(*) from memories where topic_key='my-topic-key'`); n != 1 {
		t.Fatalf("topic_key not normalized: %d rows match", n)
	}

	upd := save(t, s, SaveInput{Project: "p", Title: "Auth model", Body: "v2 body", TopicKey: "my-topic-key"})
	if upd.Action != ActionUpdated || upd.ID != first.ID {
		t.Fatalf("got %+v, want updated id %d", upd, first.ID)
	}
	if n := count(t, s, `select count(*) from memories`); n != 1 {
		t.Errorf("rows=%d, want 1 (in-place update)", n)
	}
	var old string
	if err := s.db.SQL().QueryRow(`select body from memory_versions where memory_id=? and version=1`, first.ID).Scan(&old); err != nil || old != "v1 body" {
		t.Errorf("version snapshot body=%q err=%v, want v1 body", old, err)
	}

	same := save(t, s, SaveInput{Project: "p", Title: "Auth model", Body: "v2 body", TopicKey: "my-topic-key"})
	if same.Action != ActionUnchanged || same.ID != first.ID {
		t.Errorf("identical resave: %+v, want unchanged", same)
	}
	if n := count(t, s, `select count(*) from memory_versions`); n != 1 {
		t.Errorf("unchanged resave wrote a version: %d", n)
	}
}

func TestSaveKeepsOnlyRecentVersions(t *testing.T) {
	s := newSvc(t)
	id := save(t, s, SaveInput{Project: "p", Title: "t", Body: "b0", TopicKey: "k"}).ID
	for i := 1; i <= 7; i++ {
		save(t, s, SaveInput{Project: "p", Title: "t", Body: "b" + string(rune('0'+i)), TopicKey: "k"})
	}
	if n := count(t, s, `select count(*) from memory_versions where memory_id=?`, id); n != MaxVersions {
		t.Errorf("versions=%d, want %d", n, MaxVersions)
	}
	var oldest int
	if err := s.db.SQL().QueryRow(`select min(version) from memory_versions where memory_id=?`, id).Scan(&oldest); err != nil || oldest != 3 {
		t.Errorf("oldest kept version=%d err=%v, want 3 (7 snapshots, newest 5 kept)", oldest, err)
	}
}

func TestSaveSupersedes(t *testing.T) {
	s := newSvc(t)
	old := save(t, s, SaveInput{Project: "p", Title: "Plan A", Body: "do A"})
	neu := save(t, s, SaveInput{Project: "p", Title: "Plan B", Body: "do B", Supersedes: old.ID})
	var status string
	var by int64
	if err := s.db.SQL().QueryRow(`select status, superseded_by from memories where id=?`, old.ID).Scan(&status, &by); err != nil {
		t.Fatal(err)
	}
	if status != "superseded" || by != neu.ID {
		t.Errorf("old status=%q superseded_by=%d, want superseded by %d", status, by, neu.ID)
	}

	before := count(t, s, `select count(*) from memories`)
	if _, err := s.Save(context.Background(), SaveInput{Project: "p", Title: "C", Body: "c", Supersedes: 999}); !errors.Is(err, ErrSupersedeTarget) {
		t.Errorf("missing target: err=%v, want ErrSupersedeTarget", err)
	}
	other := save(t, s, SaveInput{Project: "q", Title: "Other", Body: "other project"})
	before++
	if _, err := s.Save(context.Background(), SaveInput{Project: "p", Title: "D", Body: "d", Supersedes: other.ID}); !errors.Is(err, ErrSupersedeTarget) {
		t.Errorf("cross-project target: err=%v, want ErrSupersedeTarget", err)
	}
	if n := count(t, s, `select count(*) from memories`); n != before {
		t.Errorf("failed supersede left rows behind: %d vs %d", n, before)
	}
}

func TestSupersededTopicKeyIsNotReused(t *testing.T) {
	s := newSvc(t)
	old := save(t, s, SaveInput{Project: "p", Title: "t", Body: "old", TopicKey: "k"})
	save(t, s, SaveInput{Project: "p", Title: "t2", Body: "replacement", Supersedes: old.ID})
	again := save(t, s, SaveInput{Project: "p", Title: "t", Body: "fresh", TopicKey: "k"})
	if again.Action != ActionCreated || again.ID == old.ID {
		t.Errorf("topic_key of a superseded row must not be upserted: %+v", again)
	}
}
