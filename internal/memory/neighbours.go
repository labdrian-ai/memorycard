package memory

import (
	"context"
	"sort"
	"strings"

	"github.com/labdrian-ai/memorycard/internal/embed"
)

const (
	// NeighbourCosine is the similarity from which Save names an active memory
	// of the same project and type, so the agent can supersede it when the new
	// one makes it outdated. On the 2026-11-04 snapshot about half of all
	// saves name at least one at this value.
	NeighbourCosine = 0.75
	// MaxNeighbours caps how many similar memories one save names.
	MaxNeighbours = 3
)

// similar returns up to MaxNeighbours active memories of project and typ whose
// stored vector is at least NeighbourCosine to memory id's, closest first.
// Ids in skip are left out. Without a vector for id, it returns nothing.
func (s *Service) similar(ctx context.Context, id int64, project, typ string, skip map[int64]bool) ([]Hint, error) {
	var blob []byte
	err := s.db.SQL().QueryRowContext(ctx,
		`select vec from vectors where memory_id = ? and model = ?`, id, s.embedder.Model()).Scan(&blob)
	if err != nil {
		return nil, err
	}
	v := decodeVec(blob)
	rows, err := s.db.SQL().QueryContext(ctx,
		`select m.id, m.title, v.vec from vectors v join memories m on m.id = v.memory_id
		 where m.project = ? and m.type = ? and m.status = 'active' and v.model = ? and m.id != ?`,
		project, typ, s.embedder.Model(), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hint
	for rows.Next() {
		var h Hint
		if err := rows.Scan(&h.ID, &h.Title, &blob); err != nil {
			return nil, err
		}
		other := decodeVec(blob)
		if skip[h.ID] || len(other) != len(v) {
			continue
		}
		if h.Score = dot(v, other); h.Score >= NeighbourCosine {
			out = append(out, h)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > MaxNeighbours {
		out = out[:MaxNeighbours]
	}
	return out, nil
}

// neighbours fills res.Neighbours after a save that wrote text. Session
// summaries are bookkeeping, not claims that can go out of date, so they get
// none. The save's own parts and the duplicate hint are not repeated.
func (s *Service) neighbours(ctx context.Context, in SaveInput, ids []int64, res *SaveResult) {
	if s.embedder == nil || in.Type == "session_summary" ||
		(res.Action != ActionCreated && res.Action != ActionUpdated) {
		return
	}
	skip := map[int64]bool{}
	for _, id := range ids {
		skip[id] = true
	}
	if res.Hint != nil {
		skip[res.Hint.ID] = true
	}
	// A failed lookup only loses the hint; the save itself succeeded.
	if hs, err := s.similar(ctx, ids[0], in.Project, in.Type, skip); err == nil && len(hs) > 0 {
		res.Neighbours = hs
	}
}

// EmbedMemories embeds the memories in ids that have no vector for the current
// model, in one embedder call, and returns how many it embedded. Saves made
// with DeferEmbed use it to share one batch. On error nothing is stored and
// the memories stay pending for EmbedPending.
func (s *Service) EmbedMemories(ctx context.Context, ids []int64) (int, error) {
	if s.embedder == nil || len(ids) == 0 {
		return 0, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := []any{s.embedder.Model()}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.SQL().QueryContext(ctx,
		`select m.id, m.title, m.body from memories m
		 left join vectors v on v.memory_id = m.id and v.model = ?
		 where v.memory_id is null and m.id in (`+marks+`)
		 order by m.id`, args...)
	if err != nil {
		return 0, err
	}
	var pending []int64
	var texts []string
	for rows.Next() {
		var id int64
		var title, body string
		if err := rows.Scan(&id, &title, &body); err != nil {
			rows.Close()
			return 0, err
		}
		pending = append(pending, id)
		texts = append(texts, embedText(title, body))
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(pending) == 0 {
		return 0, err
	}
	vecs, err := s.embedder.Embed(ctx, texts, embed.KindDocument)
	if err != nil {
		return 0, err
	}
	for i, id := range pending {
		if err := s.putVector(ctx, id, unit(vecs[i])); err != nil {
			return i, err
		}
	}
	return len(pending), nil
}
