package memory

import (
	"context"
	"errors"
	"strings"
)

// ErrListInput reports a list request with no project or with a missing or
// empty topic prefix.
var ErrListInput = errors.New("list needs a project and at least one non-empty topic prefix")

// ListInput selects the memories to list: those of Project whose topic key
// starts with any of TopicPrefixes.
type ListInput struct {
	Project       string
	TopicPrefixes []string
}

// Listed is one stored row as List returns it: a memory saved in parts is
// listed as its parts, not joined.
type Listed struct {
	ID       int64
	TopicKey string
	Title    string
	Body     string
}

// List returns the active memories of a project whose topic key starts with
// any given prefix, ordered by topic key then id. Prefixes match literally
// (substr, never LIKE, so "_" and "%" in a key are ordinary characters) and
// memories with no topic key never match. It reads only: unlike Get it does
// not touch use_count or last_used_at.
func (s *Service) List(ctx context.Context, in ListInput) ([]Listed, error) {
	if in.Project == "" || len(in.TopicPrefixes) == 0 {
		return nil, ErrListInput
	}
	args := []any{in.Project}
	conds := make([]string, len(in.TopicPrefixes))
	for i, p := range in.TopicPrefixes {
		if p == "" {
			return nil, ErrListInput
		}
		conds[i] = `substr(topic_key, 1, length(?)) = ?`
		args = append(args, p, p)
	}
	rows, err := s.db.SQL().QueryContext(ctx,
		`select id, topic_key, title, body from memories
		 where project = ? and status = 'active' and topic_key is not null and (`+strings.Join(conds, ` or `)+`)
		 order by topic_key, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Listed{}
	for rows.Next() {
		var m Listed
		if err := rows.Scan(&m.ID, &m.TopicKey, &m.Title, &m.Body); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
