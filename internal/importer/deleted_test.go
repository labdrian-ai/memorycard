package importer

import (
	"context"
	"database/sql"
	"testing"

	"github.com/labdrian-ai/memorycard/internal/memory"
	"github.com/labdrian-ai/memorycard/internal/store"
)

// exec runs statements against the fake Engram database at path.
func exec(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
}

func statusByTitle(t *testing.T, db *store.DB, title string) string {
	t.Helper()
	var s string
	if err := db.SQL().QueryRow(`select status from memories where title = ?`, title).Scan(&s); err != nil {
		t.Fatalf("%s: %v", title, err)
	}
	return s
}

func mirror(t *testing.T, svc *memory.Service, in memory.SaveInput) {
	t.Helper()
	if _, err := svc.Save(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}

func TestFromEngramArchivesImportedRowsDeletedAtTheSource(t *testing.T) {
	src := fakeEngram(t)
	svc, db := newService(t)
	if _, err := FromEngram(context.Background(), svc, src, nil); err != nil {
		t.Fatal(err)
	}
	// Deleted in Engram after the import, so after the memory was last written.
	exec(t, src, `update observations set deleted_at = '2099-01-01 00:00:00' where id = 5`)

	res, err := FromEngram(context.Background(), svc, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ArchivedDeleted != 1 {
		t.Errorf("archived deleted = %d, want 1 (result %+v)", res.ArchivedDeleted, res)
	}
	if s := statusByTitle(t, db, "Rule"); s != "archived" {
		t.Errorf("Rule status %q, want archived", s)
	}
	if s := statusByTitle(t, db, "New auth"); s != "active" {
		t.Errorf("New auth status %q, want active", s)
	}

	again, err := FromEngram(context.Background(), svc, src, nil)
	if err != nil || again.ArchivedDeleted != 0 {
		t.Errorf("second run archived %d, %v; want 0", again.ArchivedDeleted, err)
	}
}

func TestFromEngramArchivesMirrorsOfDeletedRows(t *testing.T) {
	src := fakeEngram(t)
	exec(t, src, `insert into observations(id,sync_id,type,title,content,project,topic_key,created_at,updated_at,deleted_at) values
		(10,'obs-10','decision','Keyed','by key','proj','mirror/keyed','2025-06-01 10:00:00','2025-06-01 10:00:00','2099-01-01 00:00:00'),
		(11,'obs-11','discovery','Plain','by content','proj',null,'2025-06-01 10:00:00','2025-06-01 10:00:00','2099-01-01 00:00:00')`)
	svc, db := newService(t)
	// What an agent's dual write left in memorycard, never imported.
	mirror(t, svc, memory.SaveInput{Project: "proj", Title: "Keyed", Body: "by key, mirrored", TopicKey: "mirror/keyed"})
	mirror(t, svc, memory.SaveInput{Project: "proj", Title: "Plain", Body: "by content"})

	res, err := FromEngram(context.Background(), svc, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ArchivedDeleted != 2 {
		t.Errorf("archived deleted = %d, want 2", res.ArchivedDeleted)
	}
	for _, title := range []string{"Keyed", "Plain"} {
		if s := statusByTitle(t, db, title); s != "archived" {
			t.Errorf("%s status %q, want archived", title, s)
		}
	}
}

func TestFromEngramKeepsMemoriesWrittenAfterTheDeletion(t *testing.T) {
	src := fakeEngram(t)
	exec(t, src, `insert into observations(id,sync_id,type,title,content,project,topic_key,created_at,updated_at,deleted_at) values
		(10,'obs-10','decision','Keyed','old text','proj','mirror/keyed','2025-06-01 10:00:00','2025-06-01 10:00:00','2025-06-02 10:00:00')`)
	svc, db := newService(t)
	// Saved now, long after the 2025 deletion: the newer word wins.
	mirror(t, svc, memory.SaveInput{Project: "proj", Title: "Keyed", Body: "new text", TopicKey: "mirror/keyed"})

	res, err := FromEngram(context.Background(), svc, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ArchivedDeleted != 0 || statusByTitle(t, db, "Keyed") != "active" {
		t.Errorf("archived deleted = %d, Keyed %q; want 0 and active", res.ArchivedDeleted, statusByTitle(t, db, "Keyed"))
	}
}

func TestFromEngramKeepsAKeyOrContentALiveRowStillHolds(t *testing.T) {
	src := fakeEngram(t)
	exec(t, src, `insert into observations(id,sync_id,type,title,content,project,topic_key,created_at,updated_at,deleted_at) values
		(10,'obs-10','decision','Keyed v1','old','proj','shared/key','2025-06-01 10:00:00','2025-06-01 10:00:00','2099-01-01 00:00:00'),
		(11,'obs-11','decision','Keyed v2','new','proj','shared/key','2025-06-03 10:00:00','2025-06-03 10:00:00',null),
		(12,'obs-12','discovery','Twin','same text','proj',null,'2025-06-01 10:00:00','2025-06-01 10:00:00','2099-01-01 00:00:00'),
		(13,'obs-13','discovery','Twin','same text','proj',null,'2025-06-01 10:00:00','2025-06-01 10:00:00',null)`)
	svc, db := newService(t)
	if _, err := FromEngram(context.Background(), svc, src, nil); err != nil {
		t.Fatal(err)
	}
	res, err := FromEngram(context.Background(), svc, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ArchivedDeleted != 0 {
		t.Errorf("archived deleted = %d, want 0", res.ArchivedDeleted)
	}
	for _, title := range []string{"Keyed v2", "Twin"} {
		if s := statusByTitle(t, db, title); s != "active" {
			t.Errorf("%s status %q, want active", title, s)
		}
	}
}
