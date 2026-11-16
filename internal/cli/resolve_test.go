package cli

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/labdrian-ai/memorycard/internal/memory"
)

const resolveUsage = "usage: memorycard resolve --source S --key K [--key K2 ...] [--json]"

// engramFixture builds a tiny Engram database: 1 and 3 hold the same text
// (the import merges them), 2 is long enough to be stored in parts, 4 was
// deleted before the import.
func engramFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "engram.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	long := strings.Repeat("A", memory.HardCapBytes) + "\n\n" + strings.Repeat("B", memory.HardCapBytes)
	for _, q := range []string{
		`create table observations (
			id integer primary key autoincrement, sync_id text, session_id text not null default 's',
			type text not null, title text not null, content text not null, project text,
			scope text not null default 'project', topic_key text, created_at text not null,
			updated_at text not null, deleted_at text, pinned boolean not null default 0)`,
		`create table memory_relations (
			id integer primary key autoincrement, sync_id text not null unique, source_id text, target_id text,
			relation text not null default 'pending', judgment_status text not null default 'pending')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	ins := `insert into observations(id,sync_id,type,title,content,project,created_at,updated_at,deleted_at) values (?,?,?,?,?,?,?,?,?)`
	const at = "2025-01-01 10:00:00"
	for _, r := range []struct {
		id      int
		title   string
		content string
		deleted any
	}{{1, "Same", "same body", nil}, {2, "Long", long, nil}, {3, "Same", "same body", nil}, {4, "Gone", "gone", at}} {
		if _, err := db.Exec(ins, r.id, "obs-"+strconv.Itoa(r.id), "decision", r.title, r.content, "proj", at, at, r.deleted); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// resolveDB returns a memorycard database holding the fixture's import.
func resolveDB(t *testing.T) string {
	t.Helper()
	db := newDB(t)
	if code, out, errOut := run(t, db, "", "import", "engram", engramFixture(t)); code != 0 {
		t.Fatalf("import: code=%d out=%q err=%q", code, out, errOut)
	}
	return db
}

type resolveJSON struct {
	Source string `json:"source"`
	IDs    []struct {
		Key      string `json:"key"`
		MemoryID *int64 `json:"memory_id"`
	} `json:"ids"`
}

func TestResolveJSONFoundUnknownAndOrder(t *testing.T) {
	db := resolveDB(t)
	code, out, errOut := run(t, db, "", "resolve", "--json", "--source", "engram", "--key", "3", "--key", "404", "--key", "1", "--key", "3")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	var got resolveJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, out)
	}
	var keys []string
	for _, e := range got.IDs {
		keys = append(keys, e.Key)
	}
	if got.Source != "engram" || !reflect.DeepEqual(keys, []string{"3", "404", "1", "3"}) {
		t.Fatalf("got %+v, want source engram and keys 3 404 1 3 in input order", got)
	}
	if got.IDs[1].MemoryID != nil {
		t.Errorf("unknown key = %v, want null", *got.IDs[1].MemoryID)
	}
	if got.IDs[0].MemoryID == nil || got.IDs[2].MemoryID == nil || *got.IDs[0].MemoryID != *got.IDs[2].MemoryID || *got.IDs[3].MemoryID != *got.IDs[0].MemoryID {
		t.Errorf("keys 1 and 3 hold the same text and merge into one memory: %+v", got.IDs)
	}
}

func TestResolveJSONHasExactlyTheDocumentedKeys(t *testing.T) {
	db := resolveDB(t)
	_, out, _ := run(t, db, "", "resolve", "--json", "--source", "engram", "--key", "1", "--key", "404")
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 2 || raw["source"] == nil || raw["ids"] == nil {
		t.Errorf("top-level keys = %v, want exactly source and ids", raw)
	}
	var ids []map[string]json.RawMessage
	if err := json.Unmarshal(raw["ids"], &ids); err != nil {
		t.Fatal(err)
	}
	for _, e := range ids {
		if len(e) != 2 || e["key"] == nil || e["memory_id"] == nil {
			t.Errorf("entry keys = %v, want exactly key and memory_id", e)
		}
	}
	if string(ids[1]["memory_id"]) != "null" {
		t.Errorf("unknown memory_id = %s, want null", ids[1]["memory_id"])
	}
}

func TestResolveTextFormFoundAndUnknown(t *testing.T) {
	db := resolveDB(t)
	code, out, errOut := run(t, db, "", "resolve", "--source", "engram", "--key", "1", "--key", "404", "--key", "4")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	want := "engram:1 #1\nengram:404 unknown\nengram:4 unknown\n"
	if out != want {
		t.Errorf("out = %q, want %q", out, want)
	}
}

func TestResolveASplitRecordGivesItsPartOneMemory(t *testing.T) {
	db := resolveDB(t)
	_, out, _ := run(t, db, "", "resolve", "--source", "engram", "--key", "2")
	if !strings.HasPrefix(out, "engram:2 #") {
		t.Fatalf("out = %q", out)
	}
	_, got, _ := run(t, db, "", "get", strings.TrimSpace(strings.TrimPrefix(out, "engram:2 #")))
	if !strings.Contains(got, "(part 1/") {
		t.Errorf("memory %q is not part 1 of the long record:\n%.200s", out, got)
	}
}

func TestResolveWritesNothing(t *testing.T) {
	db := resolveDB(t)
	read := func() string {
		raw, err := sql.Open("sqlite", db)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		var m, im, v, u int
		if err := raw.QueryRow(`select (select count(*) from memories), (select count(*) from import_map),
			(select count(*) from memory_versions), (select coalesce(sum(use_count),0) from memories)`).Scan(&m, &im, &v, &u); err != nil {
			t.Fatal(err)
		}
		return strings.Join([]string{strconv.Itoa(m), strconv.Itoa(im), strconv.Itoa(v), strconv.Itoa(u)}, ",")
	}
	before := read()
	run(t, db, "", "resolve", "--source", "engram", "--key", "1", "--key", "404")
	run(t, db, "", "resolve", "--json", "--source", "engram", "--key", "2")
	if after := read(); after != before {
		t.Errorf("counts (memories,import_map,versions,use_count) = %s after resolve, %s before", after, before)
	}
}

func TestResolveUsageErrorsExitTwoWithTheUsageLine(t *testing.T) {
	db := newDB(t)
	cases := map[string][]string{
		"no key":       {"resolve", "--source", "engram"},
		"empty key":    {"resolve", "--source", "engram", "--key", "1", "--key", ""},
		"no source":    {"resolve", "--key", "1"},
		"empty source": {"resolve", "--source", "", "--key", "1"},
		"positional":   {"resolve", "--source", "engram", "--key", "1", "extra"},
		"nothing":      {"resolve"},
		"json, no key": {"resolve", "--json", "--source", "engram"},
	}
	for name, args := range cases {
		code, out, errOut := run(t, db, "", args...)
		if code != 2 || out != "" || !strings.Contains(errOut, resolveUsage) {
			t.Errorf("%s: code=%d out=%q err=%q, want exit 2 with %q", name, code, out, errOut, resolveUsage)
		}
	}
}

func TestResolveOnAStoreWithNoImportReportsEveryKeyUnknown(t *testing.T) {
	db := newDB(t)
	code, out, errOut := run(t, db, "", "resolve", "--source", "engram", "--key", "1")
	if code != 0 || out != "engram:1 unknown\n" || errOut != "" {
		t.Errorf("code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestHelpNamesResolve(t *testing.T) {
	_, out, _ := run(t, newDB(t), "", "help")
	if !strings.Contains(out, "resolve --source S --key K") {
		t.Errorf("help does not describe resolve:\n%s", out)
	}
}
