package memory

import (
	"context"
	"database/sql"
	"errors"
)

// ErrResolveInput reports a resolve request with an empty source or with no
// key, or with an empty key.
var ErrResolveInput = errors.New("resolve needs a source and at least one non-empty key")

// ResolveInput names the keys of one import source to look up.
type ResolveInput struct {
	Source string
	Keys   []string
}

// Resolved is the answer for one key: the memory its import created, or
// Found false when the import never mapped it.
type Resolved struct {
	Key      string
	MemoryID int64
	Found    bool
}

// Resolve maps each key of an import source (for example an Engram
// observation id) to the memory it was imported as: the part-1 row of
// import_map, the same lookup the importer uses to resolve a key. It answers
// every key in the order given, duplicates included; a key the import never
// mapped is reported with Found false, not as an error. It only reads: no row
// of any table is written. import_map is keyed by source and source key, so
// it takes no project.
func (s *Service) Resolve(ctx context.Context, in ResolveInput) ([]Resolved, error) {
	if in.Source == "" || len(in.Keys) == 0 {
		return nil, ErrResolveInput
	}
	for _, k := range in.Keys {
		if k == "" {
			return nil, ErrResolveInput
		}
	}
	out := make([]Resolved, 0, len(in.Keys))
	for _, k := range in.Keys {
		var id int64
		err := s.db.SQL().QueryRowContext(ctx,
			`select memory_id from import_map where source = ? and source_key = ? and part = 1`,
			in.Source, k).Scan(&id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			out = append(out, Resolved{Key: k})
		case err != nil:
			return nil, err
		default:
			out = append(out, Resolved{Key: k, MemoryID: id, Found: true})
		}
	}
	return out, nil
}
