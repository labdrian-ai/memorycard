package memory

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func listKeys(t *testing.T, s *Service, project string, prefixes ...string) []string {
	t.Helper()
	got, err := s.List(context.Background(), ListInput{Project: project, TopicPrefixes: prefixes})
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, m := range got {
		keys = append(keys, m.TopicKey)
	}
	return keys
}

func TestListMatchesPrefixesLiterallyInTopicOrder(t *testing.T) {
	s := newSvc(t)
	for _, k := range []string{"proc_x/b", "proc_x/a", "procXx/c", "proc%/d", "procedural/e", "other/f"} {
		save(t, s, SaveInput{Project: "p", Title: "T " + k, Body: "body " + k, TopicKey: k})
	}
	save(t, s, SaveInput{Project: "p", Title: "no topic", Body: "untopiced"})

	// "_" and "%" are literal: "proc_x/" must not match "procXx/", and
	// "proc%" must match only the key that holds a percent sign.
	if got, want := listKeys(t, s, "p", "proc_x/"), []string{"proc_x/a", "proc_x/b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("proc_x/ = %v, want %v", got, want)
	}
	if got, want := listKeys(t, s, "p", "proc%"), []string{"proc%/d"}; !reflect.DeepEqual(got, want) {
		t.Errorf("proc%% = %v, want %v", got, want)
	}
	// Two prefixes in one call, ordered by topic key; no-topic rows never match.
	if got, want := listKeys(t, s, "p", "procedural/", "other/"), []string{"other/f", "procedural/e"}; !reflect.DeepEqual(got, want) {
		t.Errorf("two prefixes = %v, want %v", got, want)
	}
	// Overlapping prefixes list a memory once.
	if got, want := listKeys(t, s, "p", "proc_x/", "proc_x/a"), []string{"proc_x/a", "proc_x/b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("overlapping prefixes = %v, want %v", got, want)
	}
}

func TestListReturnsFullFieldsOfOneProjectOnly(t *testing.T) {
	s := newSvc(t)
	id := save(t, s, SaveInput{Project: "p", Title: "Cand", Body: "full body", TopicKey: "procedural/candidates/x"}).ID
	save(t, s, SaveInput{Project: "q", Title: "Other", Body: "elsewhere", TopicKey: "procedural/candidates/y"})

	got, err := s.List(context.Background(), ListInput{Project: "p", TopicPrefixes: []string{"procedural/"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []Listed{{ID: id, TopicKey: "procedural/candidates/x", Title: "Cand", Body: "full body"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestListExcludesSupersededAndArchived(t *testing.T) {
	s := newSvc(t)
	archived := save(t, s, SaveInput{Project: "p", Title: "A", Body: "a", TopicKey: "k/archived"}).ID
	old := save(t, s, SaveInput{Project: "p", Title: "Old", Body: "old", TopicKey: "k/old"}).ID
	save(t, s, SaveInput{Project: "p", Title: "New", Body: "new", TopicKey: "k/new", Supersedes: old})
	save(t, s, SaveInput{Project: "p", Title: "Live", Body: "live", TopicKey: "k/live"})
	if _, err := s.Update(context.Background(), archived, UpdateInput{Archive: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if got, want := listKeys(t, s, "p", "k/"), []string{"k/live", "k/new"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestListWritesNothing(t *testing.T) {
	s := newSvc(t)
	id := save(t, s, SaveInput{Project: "p", Title: "T", Body: "b", TopicKey: "k/x"}).ID
	var before [3]string
	read := func() [3]string {
		var uc, lu, up string
		if err := s.db.SQL().QueryRow(
			`select use_count, coalesce(last_used_at, 'null'), updated_at from memories where id = ?`, id).
			Scan(&uc, &lu, &up); err != nil {
			t.Fatal(err)
		}
		return [3]string{uc, lu, up}
	}
	before = read()
	var changes int
	if err := s.db.SQL().QueryRow(`select total_changes()`).Scan(&changes); err != nil {
		t.Fatal(err)
	}
	if got := listKeys(t, s, "p", "k/"); len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	if after := read(); after != before {
		t.Errorf("row changed by list: %v -> %v", before, after)
	}
	var changesAfter int
	if err := s.db.SQL().QueryRow(`select total_changes()`).Scan(&changesAfter); err != nil {
		t.Fatal(err)
	}
	if changesAfter != changes {
		t.Errorf("total_changes %d -> %d, want no write", changes, changesAfter)
	}
}

func TestListShowsSplitMemoryAsItsStoredParts(t *testing.T) {
	s := newSvc(t)
	save(t, s, SaveInput{Project: "p", Title: "Big", Body: paragraphs(20, "big"), TopicKey: "doc/big"})
	got, err := s.List(context.Background(), ListInput{Project: "p", TopicPrefixes: []string{"doc/"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].TopicKey != "doc/big" || got[1].TopicKey != "doc/big/part-2" ||
		got[0].Title != "Big (part 1/2)" || strings.Contains(got[0].Body, "paragraph 019") {
		t.Errorf("got %d rows %+v, want the two stored parts as they are", len(got), got)
	}
}

func TestListOrdersTenOrMorePartsNumerically(t *testing.T) {
	s := newSvc(t)
	r := save(t, s, SaveInput{Project: "p", Title: "Huge", Body: paragraphs(130, "huge"), TopicKey: "doc/huge"})
	if len(r.Parts) < 11 {
		t.Fatalf("fixture made %d parts, want 11 or more", len(r.Parts))
	}
	got := listKeys(t, s, "p", "doc/")
	want := []string{"doc/huge"}
	for i := 2; i <= len(r.Parts); i++ {
		want = append(want, partKey("doc/huge", i))
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want parts in part order %v", got, want)
	}
}

func TestListRequiresProjectAndPrefix(t *testing.T) {
	s := newSvc(t)
	for _, in := range []ListInput{
		{Project: "p"},
		{Project: "p", TopicPrefixes: []string{"a/", ""}},
		{TopicPrefixes: []string{"a/"}},
	} {
		if _, err := s.List(context.Background(), in); !errors.Is(err, ErrListInput) {
			t.Errorf("List(%+v) err = %v, want ErrListInput", in, err)
		}
	}
}
