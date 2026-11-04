package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labdrian-ai/memorycard/internal/memory"
	"github.com/labdrian-ai/memorycard/internal/store"
)

func env(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

func exec(t *testing.T, getenv func(string) string, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut, getenv)
	return out.String(), errOut.String(), code
}

// digestDB holds a project "d" whose digest has a session summary, two parts
// of one document and a cut title among six lines.
func digestDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "m.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rec := func(key, typ, title string) memory.ImportRecord {
		return memory.ImportRecord{Source: "engram", Key: key, Project: "d", Type: typ, Title: title, Body: "body of " + title, CreatedAt: at, UpdatedAt: at}
	}
	if _, err := memory.New(db).Import(context.Background(), []memory.ImportRecord{
		rec("1", "session_summary", "Session summary: d"),
		rec("2", "architecture", "Guide (part 1/2)"),
		rec("3", "architecture", "Guide (part 2/2)"),
		rec("4", "discovery", "A title that was cut..."),
		rec("5", "discovery", "Plain note"),
		rec("6", "discovery", "Another note"),
	}, nil); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUsageWithoutDBOrWorkExitsTwo(t *testing.T) {
	for _, args := range [][]string{nil, {"-db", "x.db"}, {"-gold", "g"}} {
		_, errOut, code := exec(t, env(), args...)
		if code != 2 || !strings.Contains(errOut, "usage: mc-accept") {
			t.Errorf("args %v: exit %d stderr %q, want 2 and the usage", args, code, errOut)
		}
	}
}

func TestGoldNeedsTheEmbedder(t *testing.T) {
	_, errOut, code := exec(t, env("MEMORYCARD_EMBED", "off"), "-db", digestDB(t), "-gold", t.TempDir())
	if code != 1 || !strings.Contains(errOut, "acceptance needs the embedder") {
		t.Errorf("exit %d stderr %q, want 1 and the embedder error", code, errOut)
	}
}

func TestDigestCheckRunsWithoutGoldOrEmbedder(t *testing.T) {
	out, errOut, code := exec(t, env("MEMORYCARD_EMBED", "off"), "-db", digestDB(t), "-digest", "d,empty")
	if code != 0 {
		t.Fatalf("exit %d stderr %q out %q, want 0", code, errOut, out)
	}
	for _, want := range []string{
		"== digest noise (session summaries, further parts, cut titles)",
		"  d                      lines   6  summaries 1  parts 1  cut 1  noisy 3 (50%)",
		"  empty                  lines   0  summaries 0  parts 0  cut 0  noisy 0 (0%)",
		"  total                  lines   6  summaries 1  parts 1  cut 1  noisy 3 (50%)",
		"RESULT: PASS",
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestProjectFlagRejectsAnEmptyListEntry(t *testing.T) {
	_, errOut, code := exec(t, env("MEMORYCARD_EMBED", "off"), "-db", digestDB(t), "-digest", "d,,x")
	if code != 2 || !strings.Contains(errOut, "-digest: empty project name") {
		t.Errorf("exit %d stderr %q, want 2 and the empty-name error", code, errOut)
	}
}
