package memory

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strconv"
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
// any given prefix, ordered by topic key (a document's parts in part order,
// 2 before 10) then id. Prefixes match literally
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Text order puts "<key>/part-10" before "<key>/part-2"; order parts by
	// number instead (ids are no guide: a re-save reuses the old part rows).
	sort.SliceStable(out, func(i, j int) bool {
		bi, ni := splitPartKey(out[i].TopicKey)
		bj, nj := splitPartKey(out[j].TopicKey)
		if bi != bj {
			return bi < bj
		}
		return ni < nj
	})
	return out, nil
}

var partSuffix = regexp.MustCompile(`^(.*)/part-(\d+)$`)

// splitPartKey returns the key a part belongs to and its part number; a key
// that is not a part is its own base, number 0.
func splitPartKey(key string) (string, int) {
	m := partSuffix.FindStringSubmatch(key)
	if m == nil {
		return key, 0
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return key, 0
	}
	return m[1], n
}
