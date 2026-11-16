package memory

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func resolve(t *testing.T, s *Service, source string, keys ...string) []Resolved {
	t.Helper()
	got, err := s.Resolve(context.Background(), ResolveInput{Source: source, Keys: keys})
	if err != nil {
		t.Fatalf("Resolve(%q, %v): %v", source, keys, err)
	}
	return got
}

func TestResolveMapsAKeyToItsMemoryAndReportsAnUnknownOne(t *testing.T) {
	s := newSvc(t)
	imp(t, s, []ImportRecord{rec("7", "Auth model", "signed tokens")}, nil)
	id := int64(count(t, s, `select memory_id from import_map where source = 'engram' and source_key = '7'`))

	got := resolve(t, s, "engram", "7", "99")
	want := []Resolved{{Key: "7", MemoryID: id, Found: true}, {Key: "99"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestResolveKeepsInputOrderAndDuplicates(t *testing.T) {
	s := newSvc(t)
	imp(t, s, []ImportRecord{rec("1", "One", "first"), rec("2", "Two", "second")}, nil)
	var keys []string
	for _, r := range resolve(t, s, "engram", "2", "ghost", "1", "2") {
		keys = append(keys, r.Key)
	}
	if want := []string{"2", "ghost", "1", "2"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
}

func TestResolveAKeySavedInPartsGivesItsPartOneMemory(t *testing.T) {
	s := newSvc(t)
	body := strings.Repeat("A", chunkTargetBytes-1) + "\n\n" + strings.Repeat("B", 13000*sizeScale)
	imp(t, s, []ImportRecord{rec("5", "Long note", body)}, nil)
	if n := count(t, s, `select count(*) from import_map where source_key = '5'`); n < 2 {
		t.Fatalf("fixture has %d import_map rows, want at least 2 parts", n)
	}
	part1 := int64(count(t, s, `select memory_id from import_map where source_key = '5' and part = 1`))
	part2 := int64(count(t, s, `select memory_id from import_map where source_key = '5' and part = 2`))
	if part1 == part2 {
		t.Fatalf("fixture: parts 1 and 2 share memory %d", part1)
	}
	got := resolve(t, s, "engram", "5")
	if len(got) != 1 || got[0].MemoryID != part1 || !got[0].Found {
		t.Errorf("got %+v, want memory %d (part 1)", got, part1)
	}
}

func TestResolveTwoKeysMergedIntoOneMemoryGiveTheSameID(t *testing.T) {
	s := newSvc(t)
	imp(t, s, []ImportRecord{rec("1", "Same", "same body"), rec("2", "Same", "same body")}, nil)
	got := resolve(t, s, "engram", "1", "2")
	if len(got) != 2 || !got[0].Found || got[0].MemoryID != got[1].MemoryID {
		t.Errorf("got %+v, want one memory id twice", got)
	}
}

func TestResolveDoesNotMatchAnotherSourcesKey(t *testing.T) {
	s := newSvc(t)
	other := rec("9", "Elsewhere", "from notion")
	other.Source = "notion"
	imp(t, s, []ImportRecord{other}, nil)
	if got := resolve(t, s, "engram", "9"); got[0].Found {
		t.Errorf("engram:9 resolved to %+v, but only notion:9 was imported", got[0])
	}
	if got := resolve(t, s, "notion", "9"); !got[0].Found {
		t.Errorf("notion:9 = %+v, want found", got[0])
	}
}

func TestResolveWritesNothing(t *testing.T) {
	s := newSvc(t)
	imp(t, s, []ImportRecord{rec("1", "One", "first")}, nil)
	snapshot := func() []int {
		return []int{
			count(t, s, `select count(*) from memories`),
			count(t, s, `select count(*) from import_map`),
			count(t, s, `select count(*) from memory_versions`),
			count(t, s, `select coalesce(sum(use_count), 0) from memories`),
			count(t, s, `select count(*) from memories where last_used_at is not null`),
		}
	}
	before := snapshot()
	resolve(t, s, "engram", "1", "404")
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Errorf("tables changed: before %v, after %v", before, after)
	}
}

func TestResolveRefusesEmptyInput(t *testing.T) {
	s := newSvc(t)
	cases := map[string]ResolveInput{
		"no source":    {Keys: []string{"1"}},
		"no key":       {Source: "engram"},
		"empty key":    {Source: "engram", Keys: []string{"1", ""}},
		"empty source": {Source: "", Keys: nil},
	}
	for name, in := range cases {
		if _, err := s.Resolve(context.Background(), in); !errors.Is(err, ErrResolveInput) {
			t.Errorf("%s: err = %v, want ErrResolveInput", name, err)
		}
	}
}
