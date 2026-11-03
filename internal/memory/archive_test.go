package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func statusOf(t *testing.T, s *Service, id int64) (status, updated string) {
	t.Helper()
	if err := s.db.SQL().QueryRow(`select status, updated_at from memories where id=?`, id).Scan(&status, &updated); err != nil {
		t.Fatal(err)
	}
	return status, updated
}

func archive(t *testing.T, s *Service, id int64) {
	t.Helper()
	if _, err := s.Update(context.Background(), id, UpdateInput{Archive: ptr(true)}); err != nil {
		t.Fatal(err)
	}
}

func restoredWarning(r SaveResult) bool {
	for _, w := range r.Warnings {
		if strings.Contains(w, "restored") {
			return true
		}
	}
	return false
}

func TestArchiveAndRestoreRecordTheirTime(t *testing.T) {
	s := newSvc(t)
	id := save(t, s, SaveInput{Project: "p", Title: "t", Body: "b"}).ID
	_, saved := statusOf(t, s, id)
	archive(t, s, id)
	status, archived := statusOf(t, s, id)
	if status != "archived" || archived <= saved {
		t.Fatalf("after archive: status %q updated_at %q, want archived and later than %q", status, archived, saved)
	}
	if _, err := s.Update(context.Background(), id, UpdateInput{Archive: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if status, restored := statusOf(t, s, id); status != "active" || restored <= archived {
		t.Errorf("after restore: status %q updated_at %q, want active and later than %q", status, restored, archived)
	}
}

func TestSaveIdenticalToAnArchivedMemoryRestoresIt(t *testing.T) {
	s := newSvc(t)
	id := save(t, s, SaveInput{Project: "p", Title: "Cache policy", Body: "Use LRU."}).ID
	archive(t, s, id)

	r := save(t, s, SaveInput{Project: "p", Title: "Cache policy", Body: "Use LRU."})
	if r.ID != id || r.Action != ActionUpdated || !restoredWarning(r) {
		t.Fatalf("got %+v, want updated #%d with a restored warning", r, id)
	}
	if status, _ := statusOf(t, s, id); status != "active" {
		t.Errorf("status %q, want active", status)
	}
	if n := count(t, s, `select count(*) from memories`); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
	hits, err := s.Search(context.Background(), SearchInput{Project: "p", Query: "cache policy"})
	if err != nil || len(hits.Hits) != 1 || hits.Hits[0].ID != id {
		t.Errorf("search after restore = %+v, %v; want #%d", hits.Hits, err, id)
	}
}

func TestTopicUpsertOnAnArchivedMemoryRestoresIt(t *testing.T) {
	s := newSvc(t)
	id := save(t, s, SaveInput{Project: "p", Title: "Plan", Body: "v1", TopicKey: "k"}).ID
	archive(t, s, id)

	r := save(t, s, SaveInput{Project: "p", Title: "Plan", Body: "v2", TopicKey: "k"})
	if r.ID != id || r.Action != ActionUpdated || !restoredWarning(r) {
		t.Fatalf("got %+v, want updated #%d with a restored warning", r, id)
	}
	var body string
	if err := s.db.SQL().QueryRow(`select body from memories where id=?`, id).Scan(&body); err != nil || body != "v2" {
		t.Errorf("body %q, %v; want v2", body, err)
	}
	if status, _ := statusOf(t, s, id); status != "active" {
		t.Errorf("status %q, want active", status)
	}
	if n := count(t, s, `select count(*) from memory_versions where memory_id=? and body='v1'`, id); n != 1 {
		t.Errorf("v1 snapshots = %d, want 1", n)
	}
}

func TestTopicResaveOfAnArchivedMemoryRestoresIt(t *testing.T) {
	s := newSvc(t)
	id := save(t, s, SaveInput{Project: "p", Title: "Plan", Body: "same", TopicKey: "k"}).ID
	archive(t, s, id)

	r := save(t, s, SaveInput{Project: "p", Title: "Plan", Body: "same", TopicKey: "k"})
	if r.ID != id || r.Action != ActionUpdated || !restoredWarning(r) {
		t.Fatalf("got %+v, want updated #%d with a restored warning, not unchanged", r, id)
	}
	if status, _ := statusOf(t, s, id); status != "active" {
		t.Errorf("status %q, want active", status)
	}
}

func TestSaveOlderThanTheArchivingFailsAndChangesNothing(t *testing.T) {
	s := newSvc(t)
	keyed := save(t, s, SaveInput{Project: "p", Title: "Plan", Body: "v1", TopicKey: "k"}).ID
	plain := save(t, s, SaveInput{Project: "p", Title: "Note", Body: "n1"}).ID
	archive(t, s, keyed)
	archive(t, s, plain)
	_, keyedAt := statusOf(t, s, keyed)
	_, plainAt := statusOf(t, s, plain)

	for in, id := range map[SaveInput]int64{
		{Project: "p", Title: "Plan", Body: "v2", TopicKey: "k"}: keyed, // by topic key
		{Project: "p", Title: "Note", Body: "n1"}:                plain, // by identical content
	} {
		// A clock behind that memory's archiving, down to the nanosecond.
		at, err := time.Parse(time.RFC3339Nano, map[int64]string{keyed: keyedAt, plain: plainAt}[id])
		if err != nil {
			t.Fatal(err)
		}
		s.now = func() time.Time { return at.Add(-time.Nanosecond) }
		_, err = s.Save(context.Background(), in)
		if !errors.Is(err, ErrArchivedLater) || !strings.Contains(err.Error(), fmt.Sprintf("#%d", id)) {
			t.Errorf("Save(%+v) err = %v, want ErrArchivedLater naming #%d", in, err, id)
		}
	}
	for id, was := range map[int64]string{keyed: keyedAt, plain: plainAt} {
		if status, upd := statusOf(t, s, id); status != "archived" || upd != was {
			t.Errorf("#%d changed: status %q updated_at %q", id, status, upd)
		}
	}
	if n := count(t, s, `select count(*) from memories`); n != 2 {
		t.Errorf("rows = %d, want 2", n)
	}
}

func TestSaveIdenticalToAnArchivedMemoryOfAnotherKeyCreatesItsOwn(t *testing.T) {
	s := newSvc(t)
	other := save(t, s, SaveInput{Project: "p", Title: "Plan", Body: "same", TopicKey: "a"}).ID
	archive(t, s, other)
	r := save(t, s, SaveInput{Project: "p", Title: "Plan", Body: "same", TopicKey: "b"})
	if r.Action != ActionCreated || r.ID == other || restoredWarning(r) {
		t.Fatalf("got %+v, want a new memory under key b", r)
	}
	if status, _ := statusOf(t, s, other); status != "archived" {
		t.Errorf("key a memory status %q, want archived", status)
	}
}

func TestSaveIdenticalToAnActiveMemoryIsStillADuplicate(t *testing.T) {
	s := newSvc(t)
	id := save(t, s, SaveInput{Project: "p", Title: "Same", Body: "body"}).ID
	r := save(t, s, SaveInput{Project: "p", Title: "Same", Body: "body"})
	if r.ID != id || r.Action != ActionDuplicate || restoredWarning(r) {
		t.Errorf("got %+v, want duplicate of #%d", r, id)
	}
}

func TestSaveIdenticalToASupersededMemoryIsStillADuplicate(t *testing.T) {
	s := newSvc(t)
	old := save(t, s, SaveInput{Project: "p", Title: "Plan A", Body: "do A"}).ID
	save(t, s, SaveInput{Project: "p", Title: "Plan B", Body: "do B", Supersedes: old})
	r := save(t, s, SaveInput{Project: "p", Title: "Plan A", Body: "do A"})
	if r.ID != old || r.Action != ActionDuplicate {
		t.Errorf("got %+v, want duplicate of superseded #%d", r, old)
	}
	if status, _ := statusOf(t, s, old); status != "superseded" {
		t.Errorf("status %q, want superseded", status)
	}
}
