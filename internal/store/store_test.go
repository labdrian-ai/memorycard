package store

import (
	"fmt"
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenCreatesSchema(t *testing.T) {
	db := openTemp(t)
	for _, name := range []string{"memories", "memory_versions", "vectors", "sessions", "fts"} {
		var n int
		err := db.SQL().QueryRow(`select count(*) from sqlite_master where name = ?`, name).Scan(&n)
		if err != nil || n != 1 {
			t.Errorf("table %q: count=%d err=%v", name, n, err)
		}
	}
	var v int
	if err := db.SQL().QueryRow(`pragma user_version`).Scan(&v); err != nil || v != SchemaVersion {
		t.Errorf("user_version=%d err=%v, want %d", v, err, SchemaVersion)
	}
}

func TestOpenAppliesPragmas(t *testing.T) {
	db := openTemp(t)
	var mode string
	if err := db.SQL().QueryRow(`pragma journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode=%q err=%v, want wal", mode, err)
	}
	var fk int
	if err := db.SQL().QueryRow(`pragma foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Errorf("foreign_keys=%d err=%v, want 1", fk, err)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.SQL().Exec(
		`insert into memories(project,title,body,content_hash,created_at,updated_at) values('p','t','b','h','x','x')`); err != nil {
		t.Fatal(err)
	}
	first.Close()

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()
	var n int
	if err := second.SQL().QueryRow(`select count(*) from memories`).Scan(&n); err != nil || n != 1 {
		t.Errorf("rows after reopen=%d err=%v, want 1 (data must survive)", n, err)
	}
}

// FTS5 must be available in the pure-Go driver and fold accents, so a
// Spanish query without tildes still matches accented text.
func TestFTSFoldsDiacritics(t *testing.T) {
	db := openTemp(t)
	if _, err := db.SQL().Exec(
		`insert into memories(project,title,body,content_hash,created_at,updated_at)
		 values('p','Decisión de caché','usamos una política de expiración','h','x','x')`); err != nil {
		t.Fatal(err)
	}
	var id int
	err := db.SQL().QueryRow(`select rowid from fts where fts match '"decision" OR "expiracion"'`).Scan(&id)
	if err != nil || id != 1 {
		t.Errorf("fts match id=%d err=%v, want 1", id, err)
	}
}

func TestFTSFollowsUpdateAndDelete(t *testing.T) {
	db := openTemp(t)
	if _, err := db.SQL().Exec(
		`insert into memories(project,title,body,content_hash,created_at,updated_at) values('p','alpha','one','h','x','x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`update memories set title='bravo' where id=1`); err != nil {
		t.Fatal(err)
	}
	count := func(q string) int {
		var n int
		if err := db.SQL().QueryRow(`select count(*) from fts where fts match ?`, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(`"alpha"`) != 0 || count(`"bravo"`) != 1 {
		t.Errorf("after update: alpha=%d bravo=%d, want 0 and 1", count(`"alpha"`), count(`"bravo"`))
	}
	if _, err := db.SQL().Exec(`delete from memories where id=1`); err != nil {
		t.Fatal(err)
	}
	if count(`"bravo"`) != 0 {
		t.Errorf("after delete bravo still indexed")
	}
}

func TestUpgradeFromV1KeepsDataAndAddsImportMap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`insert into memories(project,title,body,content_hash,created_at,updated_at) values('p','kept','b','h','x','x')`,
		`drop table import_map`,
		`drop index idx_memories_topic_live`,
		`pragma user_version = 1`, // what a database created by the v1 build looks like
	} {
		if _, err := first.SQL().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	first.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer db.Close()
	var title string
	if err := db.SQL().QueryRow(`select title from memories`).Scan(&title); err != nil || title != "kept" {
		t.Errorf("row after upgrade: %q, %v", title, err)
	}
	if _, err := db.SQL().Exec(`insert into import_map(source, source_key, memory_id) values ('s','1',1)`); err != nil {
		t.Errorf("import_map missing after upgrade: %v", err)
	}
}

func TestUpgradeToV3RekeysLiveTopicKeyCollisionsAndEnforcesThem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ins := `insert into memories(project,title,body,topic_key,status,content_hash,created_at,updated_at) values('p',?,'b',?,?,?,'x','x')`
	for _, q := range []string{`drop index if exists idx_memories_topic_live`, `pragma user_version = 2`} {
		if _, err := first.SQL().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	rows := []struct{ title, key, status string }{
		{"genuine k", "k", "active"},           // 1: the source's own key
		{"generated part of k", "k", "active"}, // 2: an importer chunk part, newer
		{"old j", "j", "superseded"},           // 3: superseded rows never count
		{"j", "j", "active"},                   // 4
		{"x older", "x", "archived"},           // 5
		{"x newer", "x", "active"},             // 6
	}
	for i, r := range rows {
		if _, err := first.SQL().Exec(ins, r.title, r.key, r.status, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := first.SQL().Exec(`insert into import_map(source, source_key, part, memory_id) values ('engram','a',2,2)`); err != nil {
		t.Fatal(err)
	}
	first.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer db.Close()
	want := map[int]string{1: "k", 2: "k~2", 3: "j", 4: "j", 5: "x~5", 6: "x"}
	for id, key := range want {
		var got string
		if err := db.SQL().QueryRow(`select topic_key from memories where id = ?`, id).Scan(&got); err != nil || got != key {
			t.Errorf("memory %d topic_key = %q (%v), want %q", id, got, err, key)
		}
	}
	if _, err := db.SQL().Exec(ins, "another k", "k", "active", "h7"); err == nil {
		t.Error("a second live memory with topic_key k was accepted")
	}
	if _, err := db.SQL().Exec(ins, "superseded k", "k", "superseded", "h8"); err != nil {
		t.Errorf("a superseded memory with a taken topic_key was rejected: %v", err)
	}
	if _, err := db.SQL().Exec(ins, "untagged", nil, "active", "h9"); err != nil {
		t.Errorf("a memory without topic_key was rejected: %v", err)
	}
}
