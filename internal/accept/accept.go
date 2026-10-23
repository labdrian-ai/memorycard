// Package accept measures retrieval quality against a gold set of questions
// whose answers are known by their id in the source store (Engram), and
// checks the result against the v0 acceptance thresholds.
package accept

import (
	"context"
	"encoding/binary"
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/labdrian-ai/memorycard/internal/memory"
	"github.com/labdrian-ai/memorycard/internal/render"
	"github.com/labdrian-ai/memorycard/internal/store"
)

// Acceptance thresholds from the v0 spec (S8).
const (
	MinHit5   = 0.85
	MinHit1   = 0.60
	MaxTokens = 350.0
	MaxP50    = 500 * time.Millisecond

	charsPerToken = 3.6
	sourceName    = "engram"
)

// Query is one gold question. ID is the source id of the memory that answers
// it (and the key into Gold.Relevant).
type Query struct {
	ID, Text, Lang, Split string
}

// Gold is a set of questions plus optional per-question lists of source ids
// that are all accepted answers (replacing the default of just the ID).
type Gold struct {
	Queries  []Query
	Relevant map[string][]string
}

// LoadGold reads queries.tsv (columns id, query, lang, optional split) and an
// optional relevant.tsv (id, comma-separated accepted ids) from dir.
func LoadGold(dir string) (Gold, error) {
	f, err := os.Open(filepath.Join(dir, "queries.tsv"))
	if err != nil {
		return Gold{}, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comma, r.LazyQuotes, r.FieldsPerRecord = '\t', true, -1
	rows, err := r.ReadAll()
	if err != nil {
		return Gold{}, err
	}
	if len(rows) == 0 {
		return Gold{}, errors.New("queries.tsv is empty")
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[strings.TrimSpace(h)] = i
	}
	at := func(row []string, name string) string {
		if i, ok := col[name]; ok && i < len(row) {
			return row[i]
		}
		return ""
	}
	g := Gold{Relevant: map[string][]string{}}
	for _, row := range rows[1:] {
		g.Queries = append(g.Queries, Query{ID: at(row, "id"), Text: at(row, "query"), Lang: at(row, "lang"), Split: at(row, "split")})
	}
	rel, err := os.ReadFile(filepath.Join(dir, "relevant.tsv"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Gold{}, err
	}
	for _, line := range strings.Split(string(rel), "\n") {
		if id, ids, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok {
			g.Relevant[id] = strings.Split(ids, ",")
		}
	}
	return g, nil
}

// Options tune a run.
type Options struct {
	K                int     // results examined per query (default 10)
	IncludeOld       bool    // let superseded and archived memories compete
	NoTitleAccept    bool    // do not accept memories that share the target's title
	SimilarityAccept float64 // also accept memories with cosine >= this to the target (0 = off)
}

// QueryResult is the outcome of one question.
type QueryResult struct {
	Query
	Rank    int // 1-based position of the first accepted hit within K; 0 = miss
	Hits    int
	Mode    string
	Top     float64 // score of the first hit (cosine in dense mode)
	Tokens  float64
	Latency time.Duration
}

// Run asks every gold question through the real search path and ranks it.
func Run(ctx context.Context, svc *memory.Service, db *store.DB, g Gold, o Options) ([]QueryResult, error) {
	if o.K <= 0 {
		o.K = 10
	}
	ix, err := loadIndex(ctx, db)
	if err != nil {
		return nil, err
	}
	out := make([]QueryResult, 0, len(g.Queries))
	for _, q := range g.Queries {
		keys := g.Relevant[q.ID]
		if len(keys) == 0 {
			keys = []string{q.ID}
		}
		accepted := ix.accepted(keys, o)

		start := time.Now()
		res, err := svc.Search(ctx, memory.SearchInput{Query: q.Text, K: o.K, IncludeOld: o.IncludeOld})
		lat := time.Since(start)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", q.ID, err)
		}
		qr := QueryResult{Query: q, Hits: len(res.Hits), Mode: res.Mode, Latency: lat,
			Tokens: float64(len(render.Search(res))) / charsPerToken}
		if len(res.Hits) > 0 {
			qr.Top = res.Hits[0].Score
		}
		for i, h := range res.Hits {
			if accepted[h.ID] {
				qr.Rank = i + 1
				break
			}
		}
		out = append(out, qr)
	}
	return out, nil
}

type index struct {
	byKey  map[string][]int64  // source key -> memory ids (all parts)
	byBase map[string][]int64  // title without a "(part n/m)" suffix -> memory ids
	base   map[int64]string    // memory id -> base title
	vecs   map[int64][]float32 // memory id -> unit vector
}

var partSuffix = regexp.MustCompile(` \(part \d+/\d+\)$`)

func loadIndex(ctx context.Context, db *store.DB) (*index, error) {
	ix := &index{byKey: map[string][]int64{}, byBase: map[string][]int64{}, base: map[int64]string{}, vecs: map[int64][]float32{}}
	rows, err := db.SQL().QueryContext(ctx, `select source_key, memory_id from import_map where source = ?`, sourceName)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var key string
		var id int64
		if err := rows.Scan(&key, &id); err != nil {
			rows.Close()
			return nil, err
		}
		ix.byKey[key] = append(ix.byKey[key], id)
	}
	rows.Close()

	rows, err = db.SQL().QueryContext(ctx, `select id, title from memories`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			rows.Close()
			return nil, err
		}
		b := strings.ToLower(partSuffix.ReplaceAllString(title, ""))
		ix.base[id] = b
		ix.byBase[b] = append(ix.byBase[b], id)
	}
	rows.Close()

	rows, err = db.SQL().QueryContext(ctx, `select memory_id, vec from vectors`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			return nil, err
		}
		ix.vecs[id] = decode(blob)
	}
	return ix, rows.Err()
}

func decode(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

func dot(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// accepted returns the memory ids that count as a right answer for keys.
func (ix *index) accepted(keys []string, o Options) map[int64]bool {
	set := map[int64]bool{}
	for _, k := range keys {
		for _, id := range ix.byKey[k] {
			set[id] = true
		}
	}
	targets := make([]int64, 0, len(set))
	for id := range set {
		targets = append(targets, id)
	}
	for _, id := range targets {
		if !o.NoTitleAccept {
			for _, other := range ix.byBase[ix.base[id]] {
				set[other] = true
			}
		}
		if o.SimilarityAccept > 0 && ix.vecs[id] != nil {
			for other, v := range ix.vecs {
				if dot(ix.vecs[id], v) >= o.SimilarityAccept {
					set[other] = true
				}
			}
		}
	}
	return set
}

// Summary aggregates a run.
type Summary struct {
	N                       int
	Hit1, Hit3, Hit5, Hit10 float64
	MRR                     float64
	AvgTokens               float64
	P50                     time.Duration
	Hit5ByLang              map[string]float64
	Lexical                 int // answers that said they were lexical
	NonEmpty                int // answers with at least one hit
}

// Summarize computes the headline numbers.
func Summarize(rs []QueryResult) Summary {
	s := Summary{N: len(rs), Hit5ByLang: map[string]float64{}}
	if len(rs) == 0 {
		return s
	}
	n := float64(len(rs))
	langN, langHit := map[string]int{}, map[string]int{}
	var lat []time.Duration
	for _, r := range rs {
		for k, dst := range map[int]*float64{1: &s.Hit1, 3: &s.Hit3, 5: &s.Hit5, 10: &s.Hit10} {
			if r.Rank > 0 && r.Rank <= k {
				*dst += 1 / n
			}
		}
		if r.Rank > 0 {
			s.MRR += 1 / float64(r.Rank) / n
		}
		s.AvgTokens += r.Tokens / n
		lat = append(lat, r.Latency)
		langN[r.Lang]++
		if r.Rank > 0 && r.Rank <= 5 {
			langHit[r.Lang]++
		}
		if r.Mode == memory.ModeLexical {
			s.Lexical++
		}
		if r.Hits > 0 {
			s.NonEmpty++
		}
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	s.P50 = lat[len(lat)/2]
	for l, c := range langN {
		s.Hit5ByLang[l] = float64(langHit[l]) / float64(c)
	}
	return s
}

// Check lists every acceptance threshold the summary misses; empty means pass.
func (s Summary) Check() []string {
	var f []string
	if s.N == 0 {
		return []string{"no queries were run"}
	}
	if s.Hit5 < MinHit5 {
		f = append(f, fmt.Sprintf("hit@5 %.0f%% is below %.0f%%", s.Hit5*100, MinHit5*100))
	}
	if s.Hit1 < MinHit1 {
		f = append(f, fmt.Sprintf("hit@1 %.0f%% is below %.0f%%", s.Hit1*100, MinHit1*100))
	}
	if s.AvgTokens > MaxTokens {
		f = append(f, fmt.Sprintf("average tokens %.0f is above %.0f", s.AvgTokens, MaxTokens))
	}
	if s.P50 > MaxP50 {
		f = append(f, fmt.Sprintf("p50 latency %v is above %v", s.P50, MaxP50))
	}
	return f
}
