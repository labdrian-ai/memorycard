package memory

import (
	"fmt"
	"strings"
	"testing"
)

// paragraphs returns n paragraphs of 1000 bytes joined by blank lines; twelve
// of them fit one part, so 20 make two parts and 30 make three.
func paragraphs(n int, tag string) string {
	ps := make([]string, n)
	for i := range ps {
		head := fmt.Sprintf("%s paragraph %03d ", tag, i)
		ps[i] = head + strings.Repeat("x", 1000-len(head))
	}
	return strings.Join(ps, "\n\n")
}

type part struct {
	id                 int64
	title, body, topic string
	status             string
}

func parts(t *testing.T, s *Service, where string, args ...any) []part {
	t.Helper()
	rows, err := s.db.SQL().Query(
		`select id, title, body, coalesce(topic_key, ''), status from memories where `+where+` order by id`, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []part
	for rows.Next() {
		var p part
		if err := rows.Scan(&p.id, &p.title, &p.body, &p.topic, &p.status); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func TestSaveSplitsABodyOverTheHardCapIntoParts(t *testing.T) {
	s := newSvc(t)
	body := paragraphs(30, "a")
	r := save(t, s, SaveInput{Project: "p", Title: "Design doc", Body: body, TopicKey: "doc"})

	got := parts(t, s, `project = 'p'`)
	if len(got) != 3 {
		t.Fatalf("stored %d rows, want 3 parts", len(got))
	}
	if r.Action != ActionCreated || r.ID != got[0].id {
		t.Errorf("result %+v, want created with the id of part 1 (#%d)", r, got[0].id)
	}
	if len(r.Parts) != 3 || r.Parts[0] != got[0].id || r.Parts[2] != got[2].id {
		t.Errorf("result parts %v, want the three part ids", r.Parts)
	}
	wantTopics := []string{"doc", "doc/part-2", "doc/part-3"}
	var bodies []string
	for i, p := range got {
		if want := fmt.Sprintf("Design doc (part %d/3)", i+1); p.title != want {
			t.Errorf("part %d title %q, want %q", i+1, p.title, want)
		}
		if p.topic != wantTopics[i] || p.status != "active" {
			t.Errorf("part %d topic %q status %q, want %q active", i+1, p.topic, p.status, wantTopics[i])
		}
		if len(p.body) > HardCapBytes {
			t.Errorf("part %d is %d bytes, over the hard cap", i+1, len(p.body))
		}
		bodies = append(bodies, p.body)
	}
	if strings.Join(bodies, "\n\n") != body {
		t.Error("the parts do not add up to the original body")
	}
	if !strings.Contains(strings.Join(r.Warnings, "\n"), "3 parts") {
		t.Errorf("warnings %v must say the body was saved as 3 parts", r.Warnings)
	}
}

func TestSaveSplitTopicKeyUpdatesPartsInPlaceAndArchivesLeftovers(t *testing.T) {
	s := newSvc(t)
	first := save(t, s, SaveInput{Project: "p", Title: "Doc", Body: paragraphs(30, "v1"), TopicKey: "doc"})

	again := save(t, s, SaveInput{Project: "p", Title: "Doc", Body: paragraphs(30, "v1"), TopicKey: "doc"})
	if again.Action != ActionUnchanged || again.ID != first.ID {
		t.Errorf("same body again: %+v, want unchanged #%d", again, first.ID)
	}

	shrunk := save(t, s, SaveInput{Project: "p", Title: "Doc", Body: paragraphs(20, "v2"), TopicKey: "doc"})
	if shrunk.Action != ActionUpdated || shrunk.ID != first.ID {
		t.Errorf("shrunk: %+v, want updated #%d", shrunk, first.ID)
	}
	got := parts(t, s, `project = 'p'`)
	if len(got) != 3 {
		t.Fatalf("shrinking created rows: %d, want the same 3", len(got))
	}
	if got[0].title != "Doc (part 1/2)" || got[1].title != "Doc (part 2/2)" || got[2].status != "archived" {
		t.Errorf("after shrink: %q %q, part 3 %q; want parts 1/2, 2/2 and part 3 archived", got[0].title, got[1].title, got[2].status)
	}
	if !strings.HasPrefix(got[0].body, "v2 paragraph 000") {
		t.Error("part 1 was not updated in place")
	}

	single := save(t, s, SaveInput{Project: "p", Title: "Doc", Body: "Now it fits in one memory.", TopicKey: "doc"})
	got = parts(t, s, `project = 'p'`)
	if single.ID != first.ID || got[0].title != "Doc" || got[1].status != "archived" || len(single.Parts) != 0 {
		t.Errorf("back to one body: %+v, titles %q, part 2 %q; want #%d titled Doc and part 2 archived", single, got[0].title, got[1].status, first.ID)
	}

	save(t, s, SaveInput{Project: "p", Title: "Doc", Body: paragraphs(30, "v3"), TopicKey: "doc"})
	got = parts(t, s, `project = 'p'`)
	if len(got) != 3 || got[1].status != "active" || got[2].status != "active" || !strings.HasPrefix(got[2].body, "v3") {
		t.Errorf("growing again must reuse and restore the archived parts: %+v", got)
	}
	if n := count(t, s, `select count(*) from memory_versions where memory_id = ?`, first.ID); n == 0 {
		t.Error("updating part 1 kept no version history")
	}
}

func TestSaveSplitArchivesLeftoversOfNonASCIIKeys(t *testing.T) {
	s := newSvc(t)
	save(t, s, SaveInput{Project: "p", Title: "Diseño", Body: paragraphs(30, "d"), TopicKey: "diseño"})
	save(t, s, SaveInput{Project: "p", Title: "Diseño", Body: "Ahora entra en una sola memoria.", TopicKey: "diseño"})
	got := parts(t, s, `project = 'p'`)
	if len(got) != 3 || got[0].status != "active" || got[1].status != "archived" || got[2].status != "archived" {
		t.Errorf("after shrinking diseño to one memory: %+v, want parts 2 and 3 archived", got)
	}
}

func TestSaveLeavesKeysThatOnlyLookLikePartsAlone(t *testing.T) {
	s := newSvc(t)
	manual := save(t, s, SaveInput{Project: "p", Title: "Release part two", Body: "notes for part two", TopicKey: "release/part-2"})
	save(t, s, SaveInput{Project: "p", Title: "Release", Body: "release plan", TopicKey: "release"})

	save(t, s, SaveInput{Project: "p", Title: "Doc", Body: paragraphs(30, "v1"), TopicKey: "doc"})
	padded := save(t, s, SaveInput{Project: "p", Title: "Doc appendix", Body: "appendix", TopicKey: "doc/part-04"})
	save(t, s, SaveInput{Project: "p", Title: "Doc", Body: paragraphs(20, "v2"), TopicKey: "doc"})

	for _, id := range []int64{manual.ID, padded.ID} {
		if n := count(t, s, `select count(*) from memories where id = ? and status = 'active'`, id); n != 1 {
			t.Errorf("memory #%d was never a part but is no longer active", id)
		}
	}
}

func TestSaveSplitNeverRevivesAnotherKeysArchivedPart(t *testing.T) {
	s := newSvc(t)
	body := paragraphs(20, "c")
	save(t, s, SaveInput{Project: "p", Title: "Doc", Body: body, TopicKey: "doc"})
	save(t, s, SaveInput{Project: "p", Title: "Doc", Body: "short now", TopicKey: "doc"})
	old := parts(t, s, `topic_key = 'doc/part-2'`)

	save(t, s, SaveInput{Project: "p", Title: "Doc", Body: body, TopicKey: "copy", Scope: "personal"})
	if got := parts(t, s, `id = ?`, old[0].id); got[0].status != "archived" {
		t.Errorf("saving under key copy revived doc's archived part #%d", old[0].id)
	}
}

func TestSaveSplitWithoutTopicKeyStoresPartsOnceAndSupersedesWithPartOne(t *testing.T) {
	s := newSvc(t)
	old := save(t, s, SaveInput{Project: "p", Title: "Old notes", Body: "superseded soon"})
	body := paragraphs(20, "n")
	r := save(t, s, SaveInput{Project: "p", Title: "Notes", Body: body, Supersedes: old.ID})
	if len(r.Parts) != 2 {
		t.Fatalf("parts %v, want 2", r.Parts)
	}
	got := parts(t, s, `title like 'Notes (part %'`)
	if len(got) != 2 || got[0].topic != "" || got[1].topic != "" {
		t.Errorf("untagged split: %+v, want 2 parts without topic keys", got)
	}
	var status string
	var by int64
	if err := s.db.SQL().QueryRow(`select status, superseded_by from memories where id = ?`, old.ID).Scan(&status, &by); err != nil {
		t.Fatal(err)
	}
	if status != "superseded" || by != r.ID {
		t.Errorf("old memory %s by #%d, want superseded by part 1 #%d", status, by, r.ID)
	}

	dup := save(t, s, SaveInput{Project: "p", Title: "Notes", Body: body})
	if dup.Action != ActionDuplicate || dup.ID != r.ID {
		t.Errorf("same split body again: %+v, want duplicate of #%d", dup, r.ID)
	}
	if n := count(t, s, `select count(*) from memories where title like 'Notes (part %'`); n != 2 {
		t.Errorf("saving the same body again stored %d parts, want 2", n)
	}
}
