package memory

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"

	"github.com/labdrian-ai/memorycard/internal/embed"
)

const (
	// EmbedBodyRunes is how much of the body is embedded along with the title.
	EmbedBodyRunes = 1500
	// DuplicateHintCosine is the similarity at which Save hints at a duplicate.
	// The starting value is calibrated later on held-out data.
	DuplicateHintCosine = 0.97
)

// Hint points at an existing memory that looks like the one just saved.
type Hint struct {
	ID    int64
	Title string
	Score float64
}

// SetEmbedder enables vector indexing. Without one, Save stores text only.
func (s *Service) SetEmbedder(e embed.Embedder) { s.embedder = e }

// SetHybrid makes Search fuse the dense ranking with the lexical one, so words
// beyond the embedded part of a memory still count. It needs an embedder;
// without one, search stays lexical.
func (s *Service) SetHybrid(on bool) { s.hybrid = on }

func embedText(title, body string) string {
	r := []rune(body)
	if len(r) > EmbedBodyRunes {
		r = r[:EmbedBodyRunes]
	}
	return title + "\n" + string(r)
}

// index embeds one memory and replaces its stored vector.
func (s *Service) index(ctx context.Context, id int64, title, body string) ([]float32, error) {
	vecs, err := s.embedder.Embed(ctx, []string{embedText(title, body)}, embed.KindDocument)
	if err != nil {
		return nil, err
	}
	v := unit(vecs[0])
	if err := s.putVector(ctx, id, v); err != nil {
		return nil, err
	}
	return v, nil
}

func (s *Service) putVector(ctx context.Context, id int64, v []float32) error {
	_, err := s.db.SQL().ExecContext(ctx,
		`insert or replace into vectors(memory_id, model, dim, vec) values (?,?,?,?)`,
		id, s.embedder.Model(), len(v), encodeVec(v))
	return err
}

// EmbedPending embeds memories that have no vector for the current model,
// up to limit per call, and returns how many it embedded.
func (s *Service) EmbedPending(ctx context.Context, limit int) (int, error) {
	if s.embedder == nil {
		return 0, nil
	}
	rows, err := s.db.SQL().QueryContext(ctx,
		`select m.id, m.title, m.body from memories m
		 left join vectors v on v.memory_id = m.id
		 where v.memory_id is null or v.model != ?
		 order by m.id limit ?`, s.embedder.Model(), limit)
	if err != nil {
		return 0, err
	}
	type item struct {
		id          int64
		title, body string
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.title, &it.body); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(items) == 0 {
		return 0, err
	}
	texts := make([]string, len(items))
	for i, it := range items {
		texts[i] = embedText(it.title, it.body)
	}
	vecs, err := s.embedder.Embed(ctx, texts, embed.KindDocument)
	if err != nil {
		return 0, err
	}
	for i, it := range items {
		if err := s.putVector(ctx, it.id, unit(vecs[i])); err != nil {
			return i, err
		}
	}
	return len(items), nil
}

// nearest returns the most similar active memory of the same project, other
// than id, whose cosine to v is at least min.
func (s *Service) nearest(ctx context.Context, id int64, project string, v []float32, min float64) (*Hint, error) {
	rows, err := s.db.SQL().QueryContext(ctx,
		`select m.id, m.title, v.vec from vectors v join memories m on m.id = v.memory_id
		 where m.project = ? and m.status = 'active' and v.model = ? and m.id != ?`,
		project, s.embedder.Model(), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var best *Hint
	for rows.Next() {
		var h Hint
		var blob []byte
		if err := rows.Scan(&h.ID, &h.Title, &blob); err != nil {
			return nil, err
		}
		other := decodeVec(blob)
		if len(other) != len(v) {
			continue
		}
		h.Score = dot(v, other)
		if h.Score >= min && (best == nil || h.Score > best.Score) {
			c := h
			best = &c
		}
	}
	return best, rows.Err()
}

func (s *Service) afterWrite(ctx context.Context, id int64, project, title, body, action string, res *SaveResult) {
	if s.embedder == nil || (action != ActionCreated && action != ActionUpdated) {
		return
	}
	v, err := s.index(ctx, id, title, body)
	if err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("embedding pending: %v", err))
		return
	}
	if action == ActionCreated {
		if h, err := s.nearest(ctx, id, project, v, DuplicateHintCosine); err == nil {
			res.Hint = h
		}
	}
}

func unit(v []float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	n = math.Sqrt(n)
	out := make([]float32, len(v))
	if n == 0 {
		return out
	}
	for i, x := range v {
		out[i] = float32(float64(x) / n)
	}
	return out
}

func dot(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

func encodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}
