package store

import (
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
