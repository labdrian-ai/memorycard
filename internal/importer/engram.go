// Package importer reads other memory stores into memorycard. It only reads:
// every source is opened read-only.
package importer

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/labdrian-ai/memorycard/internal/memory"
)

const (
	sourceName = "engram"
	batchSize  = 200
)

// Result extends the memory report with what the Engram source contained.
type Result struct {
	memory.ImportReport
	SkippedDeleted  int // soft-deleted observations, not imported
	UnusedRelations int // relations that are not judged supersedes (pending, related, ...)
	BadTimestamps   int // timestamps that could not be parsed and fell back to now
}

// FromEngram imports the live observations of an Engram database and maps
// its judged "supersedes" relations to superseded_by. It is resumable: rerun
// it and only what is missing is imported. progress, if set, is called after
// each batch with the number of source rows handled and the total.
func FromEngram(ctx context.Context, dst *memory.Service, path string, progress func(done, total int)) (Result, error) {
	var res Result
	abs, err := filepath.Abs(path)
	if err != nil {
		return res, err
	}
	if _, err := os.Stat(abs); err != nil {
		return res, fmt.Errorf("engram database %s: %w", path, err)
	}
	q := url.Values{"mode": {"ro"}}
	q.Add("_pragma", "query_only(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	src, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}).String())
	if err != nil {
		return res, err
	}
	defer src.Close()

	var total int
	if err := src.QueryRowContext(ctx,
		`select count(*), coalesce(sum(deleted_at is not null), 0) from observations`).Scan(&total, &res.SkippedDeleted); err != nil {
		return res, fmt.Errorf("engram database %s: %w", path, err)
	}
	total -= res.SkippedDeleted

	rows, err := src.QueryContext(ctx,
		`select id, type, title, content, project, scope, topic_key, pinned, created_at, updated_at
		 from observations where deleted_at is null order by id`)
	if err != nil {
		return res, err
	}
	defer rows.Close()

	var batch []memory.ImportRecord
	done := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		rep, err := dst.Import(ctx, batch, nil)
		if err != nil {
			return err
		}
		res.ImportReport.Add(rep)
		done += len(batch)
		batch = batch[:0]
		if progress != nil {
			progress(done, total)
		}
		return nil
	}
	for rows.Next() {
		var id int64
		var r memory.ImportRecord
		var project, topic sql.NullString
		var pinned int
		var created, updated string
		if err := rows.Scan(&id, &r.Type, &r.Title, &r.Body, &project, &r.Scope, &topic, &pinned, &created, &updated); err != nil {
			return res, err
		}
		r.Source, r.Key = sourceName, fmt.Sprint(id)
		r.Project, r.TopicKey, r.Pinned = project.String, topic.String, pinned == 1
		var bad int
		r.CreatedAt, bad = parseTime(created, bad)
		r.UpdatedAt, bad = parseTime(updated, bad)
		res.BadTimestamps += bad
		batch = append(batch, r)
		if len(batch) == batchSize {
			if err := flush(); err != nil {
				return res, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return res, err
	}
	if err := flush(); err != nil {
		return res, err
	}

	links, unused, err := supersessions(ctx, src)
	if err != nil {
		return res, err
	}
	res.UnusedRelations = unused
	rep, err := dst.Import(ctx, nil, links)
	if err != nil {
		return res, err
	}
	res.ImportReport.Add(rep)
	return res, nil
}

// supersessions returns the judged supersedes relations and how many other
// relations were left out. A relation's source replaced its target.
func supersessions(ctx context.Context, src *sql.DB) ([]memory.ImportLink, int, error) {
	rows, err := src.QueryContext(ctx,
		`select s.id, t.id from memory_relations r
		 join observations s on s.sync_id = r.source_id
		 join observations t on t.sync_id = r.target_id
		 where r.relation = 'supersedes' and r.judgment_status = 'judged'`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var links []memory.ImportLink
	for rows.Next() {
		var newer, older int64
		if err := rows.Scan(&newer, &older); err != nil {
			return nil, 0, err
		}
		links = append(links, memory.ImportLink{Source: sourceName, Replacement: fmt.Sprint(newer), Replaced: fmt.Sprint(older)})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var unused int
	err = src.QueryRowContext(ctx,
		`select count(*) from memory_relations where not (relation = 'supersedes' and judgment_status = 'judged')`).Scan(&unused)
	return links, unused, err
}

// parseTime reads Engram's "2006-01-02 15:04:05" (UTC) or RFC 3339. On
// failure it returns the zero time, which the importer replaces with now,
// and counts one bad timestamp.
func parseTime(s string, bad int) (time.Time, int) {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339Nano, time.RFC3339} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, bad
		}
	}
	return time.Time{}, bad + 1
}
