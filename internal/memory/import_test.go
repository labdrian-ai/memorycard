package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func imp(t *testing.T, s *Service, recs []ImportRecord, links []ImportLink) ImportReport {
	t.Helper()
	r, err := s.Import(context.Background(), recs, links)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	return r
}

func rec(key, title, body string) ImportRecord {
	at := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	return ImportRecord{Source: "engram", Key: key, Project: "p", Type: "decision", Title: title, Body: body, CreatedAt: at, UpdatedAt: at}
}

func TestImportPreservesFieldsAndTimestamps(t *testing.T) {
	s := newSvc(t)
	r := rec("7", "Auth model", "signed tokens")
	r.TopicKey, r.Pinned, r.Scope = "Architecture/Auth", true, "personal"
	r.UpdatedAt = time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC)
	rep := imp(t, s, []ImportRecord{r}, nil)
	if rep.Imported != 1 {
		t.Fatalf("report = %+v", rep)
	}
	m, err := s.Get(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Pinned || m.TopicKey != "architecture/auth" || m.Scope != "personal" || m.Type != "decision" {
		t.Errorf("fields lost: %+v", m)
	}
	if m.CreatedAt != "2025-01-02T03:04:05Z" || m.UpdatedAt != "2025-02-03T04:05:06Z" {
		t.Errorf("timestamps = %q / %q, want the source's", m.CreatedAt, m.UpdatedAt)
	}
	if hits := search(t, s, SearchInput{Query: "tokens", Project: "p"}).Hits; len(hits) != 1 || !hits[0].Stale {
		t.Errorf("old imported decision should be searchable and flagged stale: %+v", hits)
	}
}

func TestImportIsResumable(t *testing.T) {
	s := newSvc(t)
	recs := []ImportRecord{rec("1", "One", "first"), rec("2", "Two", "second")}
	imp(t, s, recs[:1], nil)
	rep := imp(t, s, recs, nil)
	if rep.Imported != 1 || rep.AlreadyImported != 1 {
		t.Errorf("resume report = %+v, want 1 new and 1 already imported", rep)
	}
	rep = imp(t, s, recs, nil)
	if rep.Imported != 0 || rep.AlreadyImported != 2 || count(t, s, `select count(*) from memories`) != 2 {
		t.Errorf("rerun report = %+v", rep)
	}
}

func TestImportMergesExactDuplicatesAndLinksStillResolve(t *testing.T) {
	s := newSvc(t)
	recs := []ImportRecord{rec("1", "Same", "same body"), rec("2", "Same", "same body"), rec("3", "New", "replacement")}
	rep := imp(t, s, recs, []ImportLink{{Source: "engram", Replacement: "3", Replaced: "2"}})
	if rep.Imported != 2 || rep.Duplicates != 1 || rep.Linked != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if n := count(t, s, `select count(*) from memories where status='superseded'`); n != 1 {
		t.Errorf("superseded rows = %d, want 1 (the duplicate key resolved to the merged row)", n)
	}
}

func TestImportChunksLargeBodiesWithoutLoss(t *testing.T) {
	s := newSvc(t)
	var paras []string
	for i := 0; i < 40; i++ {
		paras = append(paras, fmt.Sprintf("paragraph %02d %s", i, strings.TrimSpace(strings.Repeat("word ", 200))))
	}
	body := strings.Join(paras, "\n\n")
	if len(body) < 2*HardCapBytes {
		t.Fatalf("setup: body is only %d bytes", len(body))
	}
	r := rec("9", "Big note", body)
	r.TopicKey = "big/note"
	rep := imp(t, s, []ImportRecord{r}, nil)
	if rep.Chunked != 1 || rep.Imported != 1 || rep.Parts < 3 {
		t.Fatalf("report = %+v, want 1 chunked record in >= 3 parts", rep)
	}

	rows, err := s.db.SQL().Query(`select title, body, coalesce(topic_key,'') from memories order by id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var bodies, titles, topics []string
	for rows.Next() {
		var ti, b, tk string
		rows.Scan(&ti, &b, &tk)
		if len(b) > HardCapBytes {
			t.Errorf("part has %d bytes, over the cap", len(b))
		}
		titles, bodies, topics = append(titles, ti), append(bodies, b), append(topics, tk)
	}
	if got := strings.Join(bodies, "\n\n"); got != body {
		t.Errorf("chunks do not reassemble the original (%d vs %d bytes)", len(got), len(body))
	}
	n := len(bodies)
	if titles[0] != fmt.Sprintf("Big note (part 1/%d)", n) || titles[n-1] != fmt.Sprintf("Big note (part %d/%d)", n, n) {
		t.Errorf("titles = %q", titles)
	}
	if topics[0] != "big/note" || topics[1] != "big/note/part-2" {
		t.Errorf("topic keys = %q", topics)
	}
}

func TestImportSplitsAHugeSingleParagraphOnRuneBoundaries(t *testing.T) {
	s := newSvc(t)
	body := strings.Repeat("é", 30000) // 60,000 bytes, no paragraph break
	imp(t, s, []ImportRecord{rec("1", "Wall of text", body)}, nil)
	rows, _ := s.db.SQL().Query(`select body from memories order by id`)
	defer rows.Close()
	var joined strings.Builder
	for rows.Next() {
		var b string
		rows.Scan(&b)
		if !utf8.ValidString(b) || len(b) > HardCapBytes {
			t.Errorf("bad part: valid=%v bytes=%d", utf8.ValidString(b), len(b))
		}
		joined.WriteString(b)
	}
	if joined.String() != body {
		t.Errorf("content lost while splitting: %d vs %d bytes", joined.Len(), len(body))
	}
}

func TestImportLinksSupersessionIdempotently(t *testing.T) {
	s := newSvc(t)
	recs := []ImportRecord{rec("1", "Old", "old plan"), rec("2", "New", "new plan")}
	links := []ImportLink{
		{Source: "engram", Replacement: "2", Replaced: "1"},
		{Source: "engram", Replacement: "2", Replaced: "999"}, // unknown endpoint
		{Source: "engram", Replacement: "2", Replaced: "2"},   // self link
	}
	rep := imp(t, s, recs, links)
	if rep.Linked != 1 || rep.LinksSkipped != 2 {
		t.Fatalf("report = %+v, want 1 linked and 2 skipped", rep)
	}
	var status string
	var by int64
	s.db.SQL().QueryRow(`select status, superseded_by from memories where title='Old'`).Scan(&status, &by)
	if status != "superseded" || by != 2 {
		t.Errorf("old memory = %s superseded_by %d", status, by)
	}
	if hits := search(t, s, SearchInput{Query: "plan", Project: "p"}).Hits; len(hits) != 1 || hits[0].Title != "New" {
		t.Errorf("search should show only the replacement: %+v", hits)
	}
	if again := imp(t, s, recs, links); again.Linked != 0 {
		t.Errorf("rerun linked %d, want 0", again.Linked)
	}
}

func TestImportRedactsSkipsEmptyAndDefaultsProject(t *testing.T) {
	s := newSvc(t)
	secret := rec("1", "Keys", "token <private>abc123</private> rotates")
	noProject := rec("2", "Loose note", "belongs nowhere")
	noProject.Project = ""
	blank := rec("3", "Blank", "   ")
	rep := imp(t, s, []ImportRecord{secret, noProject, blank}, nil)
	if rep.Imported != 2 || rep.Empty != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if n := count(t, s, `select count(*) from memories where body like '%abc123%'`); n != 0 {
		t.Error("private text was imported")
	}
	if n := count(t, s, `select count(*) from memories where project='unassigned'`); n != 1 {
		t.Errorf("rows in unassigned = %d, want 1", n)
	}
}
