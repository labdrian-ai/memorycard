package memory

import (
	"context"
	"sort"
	"strings"
)

const (
	// NeighbourCosine is the similarity from which Save names an active memory
	// of the same project and type, so the agent can supersede it when the new
	// one makes it outdated. On the 2026-11-04 snapshot about half of all
	// saves name at least one at this value.
	NeighbourCosine = 0.75
	// MaxNeighbours caps how many similar memories one save names.
	MaxNeighbours = 3
	// CaptureEmbedBatch caps the texts of one embedder request when the
	// deferred saves of a capture block are embedded together.
	CaptureEmbedBatch = 16
)

// similar returns up to MaxNeighbours active memories of project and typ whose
// stored vector is at least NeighbourCosine to memory id's, closest first.
// Ids in skip are left out. Without a vector for id, it returns nothing.
func (s *Service) similar(ctx context.Context, id int64, project, typ string, skip map[int64]bool) ([]Hint, error) {
	return s.closest(ctx, id, project, typ, NeighbourCosine, MaxNeighbours, skip)
}

// closest returns up to limit active memories of project, and of typ unless
// typ is empty, whose stored vector is at least min to memory id's, closest
// first; equal scores keep id order. Ids in skip are left out. Without a
// vector for id, it returns an error.
func (s *Service) closest(ctx context.Context, id int64, project, typ string, min float64, limit int, skip map[int64]bool) ([]Hint, error) {
	var blob []byte
	err := s.db.SQL().QueryRowContext(ctx,
		`select vec from vectors where memory_id = ? and model = ?`, id, s.embedder.Model()).Scan(&blob)
	if err != nil {
		return nil, err
	}
	v := decodeVec(blob)
	rows, err := s.db.SQL().QueryContext(ctx,
		`select m.id, m.title, v.vec from vectors v join memories m on m.id = v.memory_id
		 where m.project = ? and (? = '' or m.type = ?) and m.status = 'active' and v.model = ? and m.id != ?
		 order by m.id`,
		project, typ, typ, s.embedder.Model(), id)
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
		if h.Score = dot(v, other); h.Score >= min {
			out = append(out, h)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
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
	var chunks []int // chunk count of each pending memory
	var texts []string
	for rows.Next() {
		var id int64
		var title, body string
		if err := rows.Scan(&id, &title, &body); err != nil {
			rows.Close()
			return 0, err
		}
		cs := embedChunks(title, body)
		pending = append(pending, id)
		chunks = append(chunks, len(cs))
		texts = append(texts, cs...)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(pending) == 0 {
		return 0, err
	}
	// Every chunk of every memory goes in requests of at most
	// CaptureEmbedBatch texts, so a block of long items stays well inside
	// the embedder's request timeout.
	vecs, err := s.embedAll(ctx, texts, CaptureEmbedBatch)
	if err != nil {
		return 0, err
	}
	for i, id := range pending {
		mine := vecs[:chunks[i]]
		vecs = vecs[chunks[i]:]
		if err := s.putVectors(ctx, id, mine[0], mine[1:]); err != nil {
			return i, err
		}
	}
	return len(pending), nil
}

// FinishDeferred completes saves made with DeferEmbed, in their order: it
// embeds every memory they wrote in one embedder call, then fills each
// result's duplicate hint and neighbours the way Save does. Each save sees the
// memories saved before it, never the later ones of the same batch, so the
// hints match what saving them one by one would have given. Duplicate and
// unchanged results get nothing. On an embedder error nothing is stored, no
// hint is set, and the memories stay pending for EmbedPending.
func (s *Service) FinishDeferred(ctx context.Context, res []*SaveResult) error {
	if s.embedder == nil {
		return nil
	}
	var ids []int64
	for _, r := range res {
		ids = append(ids, written(r)...)
	}
	if _, err := s.EmbedMemories(ctx, ids); err != nil {
		return err
	}
	// later holds the ids written by the current save and every save after it.
	later := map[int64]bool{}
	for _, id := range ids {
		later[id] = true
	}
	for _, r := range res {
		own := written(r)
		if len(own) > 0 {
			s.deferredHints(ctx, r, later)
		}
		for _, id := range own {
			delete(later, id)
		}
	}
	return nil
}

// written returns the ids a save wrote text to: all its parts, or its id.
func written(r *SaveResult) []int64 {
	if r.Action != ActionCreated && r.Action != ActionUpdated {
		return nil
	}
	if len(r.Parts) > 0 {
		return r.Parts
	}
	return []int64{r.ID}
}

// deferredHints sets r's duplicate hint (on create only, as afterWrite does)
// and its neighbours, leaving out the ids in later. A failed lookup only
// loses the hint; the save itself succeeded.
func (s *Service) deferredHints(ctx context.Context, r *SaveResult, later map[int64]bool) {
	var project, typ string
	if err := s.db.SQL().QueryRowContext(ctx,
		`select project, type from memories where id = ?`, r.ID).Scan(&project, &typ); err != nil {
		return
	}
	if r.Action == ActionCreated {
		if hs, err := s.closest(ctx, r.ID, project, "", DuplicateHintCosine, 1, later); err == nil && len(hs) > 0 {
			r.Hint = &hs[0]
		}
	}
	if typ == "session_summary" {
		return
	}
	skip := map[int64]bool{}
	for id := range later {
		skip[id] = true
	}
	if r.Hint != nil {
		skip[r.Hint.ID] = true
	}
	if hs, err := s.similar(ctx, r.ID, project, typ, skip); err == nil && len(hs) > 0 {
		r.Neighbours = hs
	}
}
