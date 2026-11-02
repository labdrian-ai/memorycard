package memory

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"

	"github.com/labdrian-ai/memorycard/internal/embed"
)

const (
	// EmbedBodyRunes is how much of the body one vector embeds along with the
	// title. A longer body gets one vector per EmbedBodyRunes: chunk 0 in
	// vectors, the rest in chunk_vectors.
	EmbedBodyRunes = 1500
	// EmbedBatch is the most texts one embedder request carries, so a long
	// memory's chunks never make a request outlast the embedder's timeout.
	EmbedBatch = 8
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

// SetMaxChunks caps how many vectors one memory gets, chunk 0 included, so
// only the first n·EmbedBodyRunes runes of a body are embedded; 0 means no
// cap. It applies to memories embedded afterwards.
func (s *Service) SetMaxChunks(n int) { s.maxChunks = n }

func embedText(title, body string) string {
	r := []rune(body)
	if len(r) > EmbedBodyRunes {
		r = r[:EmbedBodyRunes]
	}
	return title + "\n" + string(r)
}

// chunkTexts returns what each vector of a memory embeds: chunk 0 is
// embedText, chunk i the title and the i-th EmbedBodyRunes of the body.
func (s *Service) chunkTexts(title, body string) []string {
	texts := []string{embedText(title, body)}
	r := []rune(body)
	for start := EmbedBodyRunes; start < len(r); start += EmbedBodyRunes {
		if s.maxChunks > 0 && len(texts) >= s.maxChunks {
			break
		}
		end := min(start+EmbedBodyRunes, len(r))
		texts = append(texts, title+"\n"+string(r[start:end]))
	}
	return texts
}

// missingChunks returns the SQL condition, over memories m, for a memory whose
// vectors for model (an empty string for any model) are incomplete, and its
// arguments: no chunk 0, or, unless the cap is one chunk, a body longer than
// one chunk and no other chunk. Runes counted by Go are never fewer than
// characters counted by SQLite, so a memory it selects always gets the chunk
// it lacked and stops matching.
func (s *Service) missingChunks(model string) (string, []any) {
	return fmt.Sprintf(`(not exists (select 1 from vectors v where v.memory_id = m.id and (? = '' or v.model = ?))
	or (? != 1 and length(m.body) > %d and not exists (select 1 from chunk_vectors c where c.memory_id = m.id and (? = '' or c.model = ?))))`,
		EmbedBodyRunes), []any{model, model, s.maxChunks, model, model}
}

// index embeds every chunk of one memory and replaces its stored vectors. It
// returns the vector of chunk 0.
func (s *Service) index(ctx context.Context, id int64, title, body string) ([]float32, error) {
	vecs, err := s.embedDocuments(ctx, s.chunkTexts(title, body))
	if err != nil {
		return nil, err
	}
	if err := s.putVectors(ctx, id, vecs); err != nil {
		return nil, err
	}
	return unit(vecs[0]), nil
}

// embedDocuments embeds texts in requests of at most EmbedBatch texts.
func (s *Service) embedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += EmbedBatch {
		vecs, err := s.embedder.Embed(ctx, texts[start:min(start+EmbedBatch, len(texts))], embed.KindDocument)
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

// putVectors stores the vectors of one memory, chunk 0 first, in one
// transaction, dropping chunks it had before.
func (s *Service) putVectors(ctx context.Context, id int64, vecs [][]float32) error {
	tx, err := s.db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	model := s.embedder.Model()
	v := unit(vecs[0])
	if _, err := tx.ExecContext(ctx,
		`insert or replace into vectors(memory_id, model, dim, vec) values (?,?,?,?)`,
		id, model, len(v), encodeVec(v)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from chunk_vectors where memory_id = ?`, id); err != nil {
		return err
	}
	for i, raw := range vecs[1:] {
		v := unit(raw)
		if _, err := tx.ExecContext(ctx,
			`insert into chunk_vectors(memory_id, chunk, model, dim, vec) values (?,?,?,?,?)`,
			id, i+1, model, len(v), encodeVec(v)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// EmbedPending embeds memories whose vectors for the current model are
// missing or incomplete, up to limit per call, and returns how many it
// embedded.
func (s *Service) EmbedPending(ctx context.Context, limit int) (int, error) {
	if s.embedder == nil {
		return 0, nil
	}
	missing, args := s.missingChunks(s.embedder.Model())
	rows, err := s.db.SQL().QueryContext(ctx,
		`select m.id, m.title, m.body from memories m
		 where `+missing+`
		 order by m.id limit ?`, append(args, limit)...)
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
	var texts []string
	first := make([]int, len(items)+1) // texts[first[i]:first[i+1]] belong to items[i]
	for i, it := range items {
		first[i] = len(texts)
		texts = append(texts, s.chunkTexts(it.title, it.body)...)
	}
	first[len(items)] = len(texts)
	vecs, err := s.embedDocuments(ctx, texts)
	if err != nil {
		return 0, err
	}
	for i, it := range items {
		if err := s.putVectors(ctx, it.id, vecs[first[i]:first[i+1]]); err != nil {
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
