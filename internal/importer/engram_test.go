package importer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/labdrian-ai/memorycard/internal/memory"
	"github.com/labdrian-ai/memorycard/internal/store"
)

// fakeEngram builds a database with the columns the importer reads.
func fakeEngram(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "engram.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := []string{
		`create table observations (
			id integer primary key autoincrement, sync_id text, session_id text not null default 's',
			type text not null, title text not null, content text not null, project text,
			scope text not null default 'project', topic_key text, created_at text not null,
			updated_at text not null, deleted_at text, pinned boolean not null default 0)`,
		`create table memory_relations (
			id integer primary key autoincrement, sync_id text not null unique, source_id text, target_id text,
			relation text not null default 'pending', judgment_status text not null default 'pending')`,
		// 1 old decision, replaced by 2; 3 deleted; 4 no project and a "T" timestamp; 5 pinned.
		`insert into observations(id,sync_id,type,title,content,project,topic_key,created_at,updated_at,deleted_at,pinned) values
			(1,'obs-1','decision','Old auth','session cookies','proj','arch/auth','2025-01-01 10:00:00','2025-01-01 10:00:00',null,0),
			(2,'obs-2','decision','New auth','signed tokens','proj','arch/auth-v2','2025-03-01 10:00:00','2025-03-02 11:00:00',null,0),
			(3,'obs-3','bugfix','Deleted one','gone','proj',null,'2025-01-01 10:00:00','2025-01-01 10:00:00','2025-02-01 00:00:00',0),
			(4,'obs-4','discovery','Loose','no project here',null,null,'2025-04-01T10:00:00Z','2025-04-01T10:00:00Z',null,0),
			(5,'obs-5','manual','Rule','always pin me','proj',null,'2025-05-01 10:00:00','2025-05-01 10:00:00',null,1)`,
		`insert into memory_relations(sync_id,source_id,target_id,relation,judgment_status) values
			('r1','obs-2','obs-1','supersedes','judged'),
			('r2','obs-2','obs-3','supersedes','judged'),
			('r3','obs-4','obs-5','pending','pending'),
			('r4','obs-4','obs-5','related','judged'),
			('r5','obs-ghost','obs-1','supersedes','judged')`, // source row does not exist at all
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	return path
}

func newService(t *testing.T) (*memory.Service, *store.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return memory.New(db), db
}

func TestFromEngramMapsLiveRowsAndSkipsTheRest(t *testing.T) {
	svc, db := newService(t)
	res, err := FromEngram(context.Background(), svc, fakeEngram(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 4 || res.SkippedDeleted != 1 || res.Linked != 1 || res.LinksSkipped != 2 || res.UnusedRelations != 2 {
		t.Fatalf("result = %+v", res)
	}

	var n int
	db.SQL().QueryRow(`select count(*) from memories`).Scan(&n)
	if n != 4 {
		t.Errorf("memories = %d, want 4 (the deleted row is not imported)", n)
	}
	var status string
	var by int64
	db.SQL().QueryRow(`select status, superseded_by from memories where title='Old auth'`).Scan(&status, &by)
	if status != "superseded" || by == 0 {
		t.Errorf("old auth = %s superseded_by %d", status, by)
	}
	var project, created string
	db.SQL().QueryRow(`select project, created_at from memories where title='Loose'`).Scan(&project, &created)
	if project != "unassigned" || created != "2025-04-01T10:00:00Z" {
		t.Errorf("loose = %q created %q", project, created)
	}
	db.SQL().QueryRow(`select created_at from memories where title='Old auth'`).Scan(&created)
	if created != "2025-01-01T10:00:00Z" {
		t.Errorf("engram timestamp not converted to RFC3339: %q", created)
	}
	var pinned int
	db.SQL().QueryRow(`select pinned from memories where title='Rule'`).Scan(&pinned)
	if pinned != 1 {
		t.Error("pinned flag lost")
	}
}

func TestFromEngramIsResumableAndLeavesSourceUntouched(t *testing.T) {
	src := fakeEngram(t)
	before := sum(t, src)
	svc, db := newService(t)
	if _, err := FromEngram(context.Background(), svc, src, nil); err != nil {
		t.Fatal(err)
	}
	again, err := FromEngram(context.Background(), svc, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.Imported != 0 || again.AlreadyImported != 4 || again.Linked != 0 {
		t.Errorf("second run = %+v, want everything already imported", again)
	}
	var n int
	db.SQL().QueryRow(`select count(*) from memories`).Scan(&n)
	if n != 4 {
		t.Errorf("memories = %d after two runs, want 4", n)
	}
	if sum(t, src) != before {
		t.Error("the Engram database was modified")
	}
}

func TestFromEngramReportsProgressInBatches(t *testing.T) {
	svc, _ := newService(t)
	var last, total int
	calls := 0
	if _, err := FromEngram(context.Background(), svc, fakeEngram(t), func(done, tot int) {
		calls++
		last, total = done, tot
	}); err != nil {
		t.Fatal(err)
	}
	if calls == 0 || last != 4 || total != 4 {
		t.Errorf("progress calls=%d last=%d total=%d, want a final 4/4", calls, last, total)
	}
}

func TestFromEngramMissingFileFailsWithoutCreatingIt(t *testing.T) {
	svc, _ := newService(t)
	path := filepath.Join(t.TempDir(), "nope.db")
	_, err := FromEngram(context.Background(), svc, path, nil)
	if err == nil || !strings.Contains(err.Error(), "nope.db") {
		t.Errorf("err = %v, want it to name the missing file", err)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("the importer created the source file")
	}
}

// A WAL database in a read-only directory cannot create its -shm file; the
// error must say what to do instead of only quoting SQLite.
func TestFromEngramReadOnlyDirectorySuggestsCopying(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	path := fakeEngram(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`pragma journal_mode = wal`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	svc, _ := newService(t)
	_, err = FromEngram(context.Background(), svc, path, nil)
	if err == nil || !strings.Contains(err.Error(), "copy") {
		t.Errorf("err = %v, want advice to import a copy of the database", err)
	}
}

func sum(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}
