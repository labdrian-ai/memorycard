package cli

import (
	"strings"
	"testing"
	"time"
)

func TestSearchSinceUntilFilterByDay(t *testing.T) {
	db := newDB(t)
	run(t, db, "", "save", "--title", "Rollout plan", "--body", "canary first")
	today := time.Now().UTC().Format("2006-01-02")
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	code, out, errOut := run(t, db, "", "search", "--since", today, "--until", today, "canary")
	if code != 0 || !strings.Contains(out, "#1 manual: Rollout plan") || errOut != "" {
		t.Errorf("today: code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = run(t, db, "", "search", "--since", tomorrow, "canary")
	if code != 0 || !strings.Contains(out, "no results") || errOut != "" {
		t.Errorf("since tomorrow: code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = run(t, db, "", "search", "--until", "2000-01-01", "canary")
	if code != 0 || !strings.Contains(out, "no results") || errOut != "" {
		t.Errorf("until 2000: code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestSearchInvalidDateExitsNonZero(t *testing.T) {
	db := newDB(t)
	run(t, db, "", "save", "--title", "Rollout plan", "--body", "canary first")
	for _, flag := range []string{"--since", "--until"} {
		code, out, errOut := run(t, db, "", "search", flag, "2026-13-40", "canary")
		if code == 0 || out != "" || !strings.Contains(errOut, "invalid "+flag+" date") {
			t.Errorf("%s: code=%d out=%q err=%q", flag, code, out, errOut)
		}
	}
}

func TestSearchWithoutDatesIsUnchanged(t *testing.T) {
	db := newDB(t)
	run(t, db, "", "save", "--title", "Rollout plan", "--body", "canary first")
	run(t, db, "", "save", "--title", "Recap", "--body", "canary recap", "--type", "session_summary")
	code, out, errOut := run(t, db, "", "search", "canary")
	if code != 0 || errOut != "" || !strings.Contains(out, "Rollout plan") || strings.Contains(out, "Recap") {
		t.Errorf("default search: code=%d out=%q err=%q (session summaries stay out)", code, out, errOut)
	}
}
