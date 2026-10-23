package cli

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func fakeEngramDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "engram.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`create table observations (id integer primary key, sync_id text, type text, title text, content text,
			project text, scope text default 'project', topic_key text, created_at text, updated_at text,
			deleted_at text, pinned boolean default 0)`,
		`create table memory_relations (id integer primary key, sync_id text, source_id text, target_id text,
			relation text, judgment_status text)`,
		`insert into observations(id,sync_id,type,title,content,project,created_at,updated_at) values
			(1,'a','decision','First','alpha text','proj','2025-01-01 00:00:00','2025-01-01 00:00:00'),
			(2,'b','decision','Second','beta text','proj','2025-01-02 00:00:00','2025-01-02 00:00:00')`,
		`insert into memory_relations(sync_id,source_id,target_id,relation,judgment_status) values ('r','b','a','supersedes','judged')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestImportEngramCommand(t *testing.T) {
	db, src := newDB(t), fakeEngramDB(t)
	code, out, errOut := run(t, db, "", "import", "engram", src)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	for _, want := range []string{"imported: 2", "superseded links: 1", "without embedding: 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	_, out, _ = run(t, db, "", "import", "engram", src)
	if !strings.Contains(out, "already imported: 2") || !strings.Contains(out, "imported: 0") {
		t.Errorf("rerun output = %q", out)
	}
	if _, out, _ := run(t, db, "", "search", "--project", "proj", "--all", "alpha"); !strings.Contains(out, "First") {
		t.Errorf("imported memory not searchable with --all: %q", out)
	}
}

func TestImportEngramFailuresAndUsage(t *testing.T) {
	db := newDB(t)
	if code, _, errOut := run(t, db, "", "import", "engram", filepath.Join(t.TempDir(), "missing.db")); code != 1 || !strings.Contains(errOut, "missing.db") {
		t.Errorf("missing source: code=%d err=%q", code, errOut)
	}
	for _, args := range [][]string{{"import"}, {"import", "engram"}, {"import", "notion", "x"}} {
		if code, _, errOut := run(t, db, "", args...); code != 2 || !strings.Contains(errOut, "usage: memorycard import engram") {
			t.Errorf("args %v: code=%d err=%q", args, code, errOut)
		}
	}
}

func TestEmbedLoopsUntilNothingIsPending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []string }
		json.NewDecoder(r.Body).Decode(&req)
		vecs := make([][]float32, len(req.Input))
		for i := range vecs {
			vecs[i] = []float32{1, float32(i + 1), 0}
		}
		json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
	}))
	defer srv.Close()

	db := newDB(t)
	for _, title := range []string{"one", "two", "three"} {
		run(t, db, "", "save", "--title", title, "--body", "body "+title)
	}
	on := map[string]string{"MEMORYCARD_EMBED": "on", "MEMORYCARD_OLLAMA_URL": srv.URL}
	code, out, errOut := runEnv(t, db, on, "", "embed", "--limit", "2")
	if code != 0 || !strings.Contains(out, "embedded 3") {
		t.Fatalf("embed: code=%d out=%q err=%q", code, out, errOut)
	}
	if _, out, _ := run(t, db, "", "stats"); !strings.Contains(out, "without embedding: 0") {
		t.Errorf("stats after embed = %q", out)
	}
}
