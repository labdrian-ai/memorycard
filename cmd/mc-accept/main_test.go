package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labdrian-ai/memorycard/internal/embed"
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

// fakeOllama embeds every text to the same vector.
func fakeOllama(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []string }
		json.NewDecoder(r.Body).Decode(&req)
		vecs := make([][]float32, len(req.Input))
		for i := range vecs {
			vecs[i] = []float32{1, 0, 0}
		}
		json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// goldDB is digestDB with every memory embedded and "5" superseded by "6".
func goldDB(t *testing.T, url string) string {
	t.Helper()
	path := digestDB(t)
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := memory.New(db)
	if _, err := svc.Import(context.Background(), nil, []memory.ImportLink{{Source: "engram", Replacement: "6", Replaced: "5"}}); err != nil {
		t.Fatal(err)
	}
	svc.SetEmbedder(embed.NewOllama(url, "fake"))
	if _, err := svc.EmbedPending(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGoldRunKeepsTheDefaultViewAndAddsTheUserView(t *testing.T) {
	url := fakeOllama(t)
	db := goldDB(t, url)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "queries.tsv"), []byte("id\tquery\tlang\n5\tplain note\ten\n"), 0o644)
	getenv := env("MEMORYCARD_OLLAMA_URL", url)

	// Default: the lab-index view, header and lines as before.
	out, errOut, code := exec(t, getenv, "-db", db, "-gold", dir)
	if code != 1 { // the 1-question set misses the acceptance thresholds
		t.Fatalf("default: exit %d stderr %q", code, errOut)
	}
	if !strings.Contains(out, "== "+dir+": 1 questions, include-old=true similarity=0\n") {
		t.Errorf("default header changed:\n%s", out)
	}
	for _, unwanted := range []string{"project=", "answered only by", "outdated"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("default output mentions %q:\n%s", unwanted, out)
		}
	}

	// User view: active only, project of the answer, outdated answers ranked.
	os.WriteFile(filepath.Join(dir, "outdated.tsv"), []byte("5\t4\n"), 0o644)
	out, _, _ = exec(t, getenv, "-db", db, "-gold", dir, "-include-old=false", "-project", "@target")
	for _, want := range []string{
		"== " + dir + ": 1 questions, include-old=false project=@target similarity=0\n",
		"1 questions answered only by superseded or archived memories\n",
		"(1 questions)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("user view lacks %q:\n%s", want, out)
		}
	}
}

func TestProjectFlagRejectsAnEmptyListEntry(t *testing.T) {
	_, errOut, code := exec(t, env("MEMORYCARD_EMBED", "off"), "-db", digestDB(t), "-digest", "d,,x")
	if code != 2 || !strings.Contains(errOut, "-digest: empty project name") {
		t.Errorf("exit %d stderr %q, want 2 and the empty-name error", code, errOut)
	}
}
