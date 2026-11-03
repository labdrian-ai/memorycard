package memory

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// DeletedRecord is a record its source deleted after memorycard may have
// copied it, by an import or by an agent's dual write.
type DeletedRecord struct {
	ImportRecord
	// Before is when the deletion happened, rounded up to the source's
	// precision: only memories last written before it are archived.
	Before time.Time
}

// ArchiveDeleted archives the active memories that still hold a deleted
// source record: the memories an import mapped to it or, for a record never
// imported, the memories with its topic key or its exact content. live lists
// the source's live records. A memory is kept when it was written after the
// deletion (the save is the newer word), or when a live record still holds
// its topic key, its content or its import mapping. It returns how many
// memories it archived.
func (s *Service) ArchiveDeleted(ctx context.Context, deleted []DeletedRecord, live []ImportRecord) (int, error) {
	liveKeys, liveHashes, liveSource := map[[3]string]bool{}, map[[2]string]bool{}, map[[2]string]bool{}
	for _, r := range live {
		liveSource[[2]string{r.Source, r.Key}] = true
		ins, err := recordParts(r)
		if err != nil {
			continue // the import itself reports a record it cannot store
		}
		for _, in := range ins {
			if in.TopicKey != "" {
				liveKeys[[3]string{in.Project, in.Scope, in.TopicKey}] = true
			}
			liveHashes[[2]string{in.Project, contentHash(in.Title, in.Body)}] = true
		}
	}

	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stamp := s.now().UTC().Format(time.RFC3339Nano)
	archived := 0
	for _, d := range deleted {
		ids, err := deletedHolders(ctx, tx, d, liveKeys, liveHashes)
		if err != nil {
			return 0, err
		}
		for _, id := range ids {
			var project, scope, topic, hash, updated string
			if err := tx.QueryRowContext(ctx,
				`select project, scope, coalesce(topic_key, ''), content_hash, updated_at
				 from memories where id = ? and status = 'active'`, id).
				Scan(&project, &scope, &topic, &hash, &updated); errors.Is(err, sql.ErrNoRows) {
				continue
			} else if err != nil {
				return 0, err
			}
			at, err := time.Parse(time.RFC3339Nano, updated)
			if err != nil || !at.Before(d.Before) {
				continue // written after the deletion, or of unknown age: keep it
			}
			if (topic != "" && liveKeys[[3]string{project, scope, topic}]) || liveHashes[[2]string{project, hash}] {
				continue
			}
			held, err := mappedFromLive(ctx, tx, d.Source, id, liveSource)
			if err != nil {
				return 0, err
			}
			if held {
				continue
			}
			r, err := tx.ExecContext(ctx,
				`update memories set status = 'archived', updated_at = ? where id = ? and status = 'active'`, stamp, id)
			if err != nil {
				return 0, err
			}
			n, err := r.RowsAffected()
			if err != nil {
				return 0, err
			}
			archived += int(n)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return archived, nil
}

// deletedHolders returns the memories that may hold deleted record d: the
// ones an import mapped to it, else the active ones with its topic key or its
// content that no live record holds.
func deletedHolders(ctx context.Context, tx *sql.Tx, d DeletedRecord, liveKeys map[[3]string]bool, liveHashes map[[2]string]bool) ([]int64, error) {
	ids, err := queryIDs(ctx, tx,
		`select memory_id from import_map where source = ? and source_key = ? order by part`, d.Source, d.Key)
	if err != nil || len(ids) > 0 {
		return ids, err
	}
	ins, err := recordParts(d.ImportRecord)
	if err != nil {
		return nil, nil // a record the import could not store has no copy to find
	}
	for _, in := range ins {
		if in.TopicKey != "" && !liveKeys[[3]string{in.Project, in.Scope, in.TopicKey}] {
			byKey, err := queryIDs(ctx, tx,
				`select id from memories where project = ? and scope = ? and topic_key = ? and status = 'active'`,
				in.Project, in.Scope, in.TopicKey)
			if err != nil {
				return nil, err
			}
			ids = append(ids, byKey...)
		}
		if hash := contentHash(in.Title, in.Body); !liveHashes[[2]string{in.Project, hash}] {
			byContent, err := queryIDs(ctx, tx,
				`select id from memories where project = ? and scope = ? and content_hash = ? and status = 'active'`,
				in.Project, in.Scope, hash)
			if err != nil {
				return nil, err
			}
			ids = append(ids, byContent...)
		}
	}
	return ids, nil
}

// mappedFromLive reports whether an import mapped a live source record to
// memory id, as it does when it merges identical records.
func mappedFromLive(ctx context.Context, tx *sql.Tx, source string, id int64, live map[[2]string]bool) (bool, error) {
	keys, err := tx.QueryContext(ctx, `select source_key from import_map where source = ? and memory_id = ?`, source, id)
	if err != nil {
		return false, err
	}
	defer keys.Close()
	for keys.Next() {
		var k string
		if err := keys.Scan(&k); err != nil {
			return false, err
		}
		if live[[2]string{source, k}] {
			return true, nil
		}
	}
	return false, keys.Err()
}

func queryIDs(ctx context.Context, tx *sql.Tx, q string, args ...any) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
