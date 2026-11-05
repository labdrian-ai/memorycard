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
	// ChunkOverlapRunes is how much of the body two consecutive chunks of a
	// long memory share, so a sentence cut by one window is whole in the next.
	ChunkOverlapRunes = 200
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

// embedText is the text of chunk 0: the title and the first window of the body.
func embedText(title, body string) string {
	return embedChunks(title, body)[0]
}

// embedChunks splits a memory into the texts it is embedded as. A body that
// fits the window is one text; a longer one is cut into windows of
// EmbedBodyRunes that overlap by ChunkOverlapRunes, each prefixed with the
// title, until the end of the body is covered.
func embedChunks(title, body string) []string {
	r := []rune(body)
	if len(r) <= EmbedBodyRunes {
		return []string{title + "\n" + body}
	}
	var out []string
	for start := 0; ; start += EmbedBodyRunes - ChunkOverlapRunes {
		end := min(start+EmbedBodyRunes, len(r))
		out = append(out, title+"\n"+string(r[start:end]))
		if end == len(r) {
			return out
		}
	}
}

// index embeds every chunk of one memory, replaces all its stored vectors,
// and returns the vector of chunk 0.
func (s *Service) index(ctx context.Context, id int64, title, body string) ([]float32, error) {
	vecs, err := s.embedAll(ctx, embedChunks(title, body), 0)
	if err != nil {
		return nil, err
	}
	if err := s.putVectors(ctx, id, vecs[0], vecs[1:]); err != nil {
		return nil, err
	}
	return vecs[0], nil
}

// embedAll embeds texts as documents in requests of at most batch texts and
// returns them as unit vectors.
func (s *Service) embedAll(ctx context.Context, texts []string, batch int) ([][]float32, error) {
	if batch <= 0 {
		batch = len(texts)
	}
	out := make([][]float32, 0, len(texts))
	for i := 0; i < len(texts); i += batch {
		part := texts[i:min(i+batch, len(texts))]
		vecs, err := s.embedder.Embed(ctx, part, embed.KindDocument)
		if err != nil {
			return nil, err
		}
		if len(vecs) != len(part) {
			return nil, fmt.Errorf("embedder returned %d vectors for %d texts", len(vecs), len(part))
		}
		for _, v := range vecs {
			out = append(out, unit(v))
		}
	}
	return out, nil
}

// putVectors replaces the vectors of memory id in one transaction: base, when
// not nil, becomes its chunk-0 row, and extra its chunks 1..n. All earlier
// chunks are dropped, so a memory never mixes vectors of two texts.
func (s *Service) putVectors(ctx context.Context, id int64, base []float32, extra [][]float32) error {
	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if base != nil {
		if _, err := tx.ExecContext(ctx,
			`insert or replace into vectors(memory_id, model, dim, vec) values (?,?,?,?)`,
			id, s.embedder.Model(), len(base), encodeVec(base)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `delete from vector_chunks where memory_id=?`, id); err != nil {
		return err
	}
	for i, v := range extra {
		if _, err := tx.ExecContext(ctx,
			`insert into vector_chunks(memory_id, chunk, vec) values (?,?,?)`, id, i+1, encodeVec(v)); err != nil {
			return err
		}
	}
	if base == nil && len(extra) == 0 {
		// Picked for chunking by SQL's character count, yet one window in Go's
		// rune count (invalid UTF-8). Mark it done with a copy of chunk 0 so
		// EmbedPending does not pick it again.
		if _, err := tx.ExecContext(ctx,
			`insert into vector_chunks(memory_id, chunk, vec) select memory_id, 0, vec from vectors where memory_id=?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// EmbedPending embeds memories that have no vector for the current model, and
// the missing chunks of long memories that only have their chunk-0 vector (as
// every memory embedded before chunking has), up to limit memories per call.
// Embedder requests carry at most limit texts. It returns how many memories
// it embedded.
func (s *Service) EmbedPending(ctx context.Context, limit int) (int, error) {
	if s.embedder == nil {
		return 0, nil
	}
	rows, err := s.db.SQL().QueryContext(ctx,
		`select m.id, m.title, m.body, (v.memory_id is null or v.model != ?) from memories m
		 left join vectors v on v.memory_id = m.id
		 where v.memory_id is null or v.model != ?
		    or (length(m.body) > ? and not exists (select 1 from vector_chunks c where c.memory_id = m.id))
		 order by m.id limit ?`, s.embedder.Model(), s.embedder.Model(), EmbedBodyRunes, limit)
	if err != nil {
		return 0, err
	}
	type item struct {
		id          int64
		title, body string
		whole       bool // no usable chunk-0 vector: embed every chunk
		texts       []string
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.title, &it.body, &it.whole); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(items) == 0 {
		return 0, err
	}
	var texts []string
	for i := range items {
		it := &items[i]
		it.texts = embedChunks(it.title, it.body)
		if !it.whole {
			it.texts = it.texts[1:]
		}
		texts = append(texts, it.texts...)
	}
	vecs, err := s.embedAll(ctx, texts, limit)
	if err != nil {
		return 0, err
	}
	for i, it := range items {
		mine := vecs[:len(it.texts)]
		vecs = vecs[len(it.texts):]
		var base []float32
		if it.whole {
			base, mine = mine[0], mine[1:]
		}
		if err := s.putVectors(ctx, it.id, base, mine); err != nil {
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
