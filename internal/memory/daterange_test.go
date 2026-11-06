package memory

import (
	"context"
	"errors"
	"testing"
)

// dated saves a memory and moves its created_at to stamp.
func dated(t *testing.T, s *Service, in SaveInput, stamp string) int64 {
	t.Helper()
	id := save(t, s, in).ID
	if _, err := s.db.SQL().Exec(`update memories set created_at=? where id=?`, stamp, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSearchSinceUntilKeepsOnlyTheUTCDay(t *testing.T) {
	s := newSvc(t)
	before := dated(t, s, SaveInput{Project: "p", Type: "note", Title: "zebra before", Body: "zebra"}, "2026-11-04T23:59:59.999999999Z")
	first := dated(t, s, SaveInput{Project: "p", Type: "note", Title: "zebra first", Body: "zebra"}, "2026-11-05T00:00:00Z")
	last := dated(t, s, SaveInput{Project: "p", Type: "note", Title: "zebra last", Body: "zebra"}, "2026-11-05T23:59:59.999999999Z")
	after := dated(t, s, SaveInput{Project: "p", Type: "note", Title: "zebra after", Body: "zebra"}, "2026-11-06T00:00:00Z")

	got := ids(search(t, s, SearchInput{Query: "zebra", Project: "p", Since: "2026-11-05", Until: "2026-11-05"}))
	if len(got) != 2 || !(has(got, first) && has(got, last)) {
		t.Errorf("one day = %v, want exactly #%d and #%d", got, first, last)
	}
	got = ids(search(t, s, SearchInput{Query: "zebra", Project: "p", Since: "2026-11-05"}))
	if len(got) != 3 || has(got, before) {
		t.Errorf("since only = %v, want first, last, after (not #%d)", got, before)
	}
	got = ids(search(t, s, SearchInput{Query: "zebra", Project: "p", Until: "2026-11-05"}))
	if len(got) != 3 || has(got, after) {
		t.Errorf("until only = %v, want before, first, last (not #%d)", got, after)
	}
}

func has(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func TestSearchRejectsInvalidDates(t *testing.T) {
	s := newSvc(t)
	save(t, s, SaveInput{Project: "p", Title: "zebra", Body: "zebra"})
	for field, in := range map[string]SearchInput{
		"since": {Query: "zebra", Project: "p", Since: "2026-13-40"},
		"until": {Query: "zebra", Project: "p", Until: "yesterday"},
	} {
		r, err := s.Search(context.Background(), in)
		var de *DateError
		if !errors.As(err, &de) || de.Field != field {
			t.Errorf("%s: err = %v, want *DateError for %s", field, err, field)
		}
		if len(r.Hits) != 0 {
			t.Errorf("%s: %d hits with an invalid date", field, len(r.Hits))
		}
	}
}

func TestDateGivenLetsLexicalSearchSeeSessionSummaries(t *testing.T) {
	s := newSvc(t)
	id := dated(t, s, SaveInput{Project: "p", Type: "session_summary", Title: "Session", Body: "quokkaterm recap", DeferEmbed: true}, "2026-11-05T10:00:00Z")

	if got := ids(search(t, s, SearchInput{Query: "quokkaterm", Project: "p", Since: "2026-11-05"})); !has(got, id) {
		t.Errorf("--since: summary #%d not found, got %v", id, got)
	}
	if got := ids(search(t, s, SearchInput{Query: "quokkaterm", Project: "p", Until: "2026-11-05"})); !has(got, id) {
		t.Errorf("--until: summary #%d not found, got %v", id, got)
	}
	if got := ids(search(t, s, SearchInput{Query: "quokkaterm", Project: "p", Type: "session_summary"})); !has(got, id) {
		t.Errorf("type filter: summary #%d not found, got %v", id, got)
	}
	if got := ids(search(t, s, SearchInput{Query: "quokkaterm", Project: "p"})); has(got, id) {
		t.Errorf("default search must keep excluding session summaries, got %v", got)
	}
	if got := ids(search(t, s, SearchInput{Query: "quokkaterm", Project: "p", Since: "2026-11-06"})); has(got, id) {
		t.Errorf("summary from 11-05 returned for --since 2026-11-06: %v", got)
	}
}
