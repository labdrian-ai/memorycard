package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// The body cap is 64 KB and Save warns above 16 KB (owner decision,
// 2026-11-06): a feature document of a few tens of KB stays one memory.

func TestSaveKeepsA40KBBodyInOneMemory(t *testing.T) {
	s := newSvc(t)
	body := strings.Repeat("a", 40*1024)
	r := save(t, s, SaveInput{Project: "p", Title: "doc", Body: body, TopicKey: "odd/doc/tasks"})
	if len(r.Parts) > 1 {
		t.Fatalf("saved as %d parts, want one memory", len(r.Parts))
	}
	ps := parts(t, s, `project='p' and status='active'`)
	if len(ps) != 1 || ps[0].body != body || ps[0].title != "doc" {
		t.Fatalf("stored %d rows (title %q, %d bytes), want 1 row titled doc with the whole body", len(ps), firstTitle(ps), firstLen(ps))
	}
	want := "body is 40960 bytes; consider splitting it (target <= 16384)"
	if len(r.Warnings) != 1 || r.Warnings[0] != want {
		t.Errorf("warnings = %q, want [%q]", r.Warnings, want)
	}
}

func TestSaveDoesNotWarnUpTo16KB(t *testing.T) {
	s := newSvc(t)
	r := save(t, s, SaveInput{Project: "p", Title: "note", Body: strings.Repeat("b", 16*1024)})
	if len(r.Warnings) != 0 {
		t.Errorf("warnings = %q, want none for a 16 KB body", r.Warnings)
	}
}

func TestSaveSplitsOnlyAbove64KB(t *testing.T) {
	s := newSvc(t)
	r := save(t, s, SaveInput{Project: "p", Title: "at cap", Body: strings.Repeat("c", 64*1024)})
	if len(r.Parts) > 1 {
		t.Fatalf("a 64 KB body was split into %d parts", len(r.Parts))
	}
	r = save(t, s, SaveInput{Project: "p", Title: "big", Body: paragraphs(18, "big")})
	if len(r.Parts) != 2 {
		t.Fatalf("a 72 KB body made %d parts, want 2", len(r.Parts))
	}
	for _, p := range parts(t, s, `project='p' and title like 'big (part %'`) {
		if len(p.body) > 48*1024 {
			t.Errorf("part %q is %d bytes, want at most 48 KB", p.title, len(p.body))
		}
	}
}

func TestResavingASplitDocumentThatNowFitsJoinsItIntoOneMemory(t *testing.T) {
	s := newSvc(t)
	save(t, s, SaveInput{Project: "p", Title: "doc", Body: paragraphs(30, "v1"), TopicKey: "odd/doc/tasks"})
	if n := len(parts(t, s, `project='p' and status='active'`)); n < 2 {
		t.Fatalf("setup: %d active rows, want a split document", n)
	}
	body := paragraphs(10, "v2")
	r := save(t, s, SaveInput{Project: "p", Title: "doc", Body: body, TopicKey: "odd/doc/tasks"})
	active := parts(t, s, `project='p' and status='active'`)
	if len(active) != 1 || active[0].id != r.ID || active[0].title != "doc" || active[0].body != body || active[0].topic != "odd/doc/tasks" {
		var got []string
		for _, p := range active {
			got = append(got, fmt.Sprintf("#%d %q %d bytes", p.id, p.title, len(p.body)))
		}
		t.Fatalf("active rows = %v, want only #%d titled doc with the new body", got, r.ID)
	}
	if n := len(parts(t, s, `project='p' and status='archived' and topic_key like 'odd/doc/tasks/part-%'`)); n == 0 {
		t.Errorf("the old later parts were not archived")
	}
}

func TestUpdateAcceptsUpTo64KBAndRejectsMore(t *testing.T) {
	s := newSvc(t)
	r := save(t, s, SaveInput{Project: "p", Title: "t", Body: "short"})
	big := strings.Repeat("d", 64*1024)
	if _, err := s.Update(context.Background(), r.ID, UpdateInput{Body: &big}); err != nil {
		t.Fatalf("update with 64 KB: %v", err)
	}
	over := big + "e"
	_, err := s.Update(context.Background(), r.ID, UpdateInput{Body: &over})
	if err == nil || !strings.Contains(err.Error(), "cap is 65536") {
		t.Errorf("update with 64 KB + 1: err = %v, want the 65536 cap error", err)
	}
}

func firstTitle(ps []part) string {
	if len(ps) == 0 {
		return ""
	}
	return ps[0].title
}

func firstLen(ps []part) int {
	if len(ps) == 0 {
		return 0
	}
	return len(ps[0].body)
}
