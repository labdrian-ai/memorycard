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
	"strconv"
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

// Query is one gold question. ID is the key of the memory that answers it (and
// the key into Gold.Relevant and Gold.Outdated). A key is a source id in the
// import map (Engram), or "mc:<id>" for a memorycard memory id, which names
// memories saved after the import.
type Query struct {
	ID, Text, Lang, Split string
}

// Gold is a set of questions plus optional per-question lists of keys that
// are all accepted answers (replacing the default of just the ID), and of
// keys of outdated memories that contradict the answer: they are never
// accepted, and their rank is reported.
type Gold struct {
	Queries  []Query
	Relevant map[string][]string
	Outdated map[string][]string
}

// memoryKeyPrefix marks a gold key that is a memorycard memory id.
const memoryKeyPrefix = "mc:"

// LoadGold reads queries.tsv (columns id, query, lang, optional split), an
// optional relevant.tsv (id, comma-separated accepted keys) and an optional
// outdated.tsv (id, comma-separated outdated keys) from dir.
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
	g := Gold{}
	for _, row := range rows[1:] {
		g.Queries = append(g.Queries, Query{ID: at(row, "id"), Text: at(row, "query"), Lang: at(row, "lang"), Split: at(row, "split")})
	}
	if g.Relevant, err = loadLists(filepath.Join(dir, "relevant.tsv")); err != nil {
		return Gold{}, err
	}
	if g.Outdated, err = loadLists(filepath.Join(dir, "outdated.tsv")); err != nil {
		return Gold{}, err
	}
	return g, nil
}

// loadLists reads an optional file of "id<TAB>key,key" lines.
func loadLists(path string) (map[string][]string, error) {
	out := map[string][]string{}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if id, ids, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok {
			out[id] = strings.Split(ids, ",")
		}
	}
	return out, nil
}

// ProjectTarget as Options.Project searches each question inside the project
// of the memory that answers it, as an agent working in that project would.
const ProjectTarget = "@target"

// Options tune a run.
type Options struct {
	K                int     // results examined per query (default 10)
	IncludeOld       bool    // let superseded and archived memories compete
	NoTitleAccept    bool    // do not accept memories that share the target's title
	SimilarityAccept float64 // also accept memories with cosine >= this to the target (0 = off)
	// Project scopes every search: empty searches all projects, ProjectTarget
	// the project of each question's first answer, anything else that project.
	Project string
}

// QueryResult is the outcome of one question.
type QueryResult struct {
	Query
	Rank         int  // 1-based position of the first accepted hit within K; 0 = miss
	OutdatedRank int  // 1-based position of the first outdated hit within K; 0 = none
	HasOutdated  bool // the question lists outdated answers
	Inactive     bool // every answer is superseded or archived
	Hits         int
	Mode         string
	Top          float64 // score of the first hit (cosine in dense mode)
	Tokens       float64
	Latency      time.Duration
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
		outdated := ix.outdated(g.Outdated[q.ID], ix.resolveAll(keys), o)
		for id := range outdated {
			delete(accepted, id)
		}

		project := o.Project
		if project == ProjectTarget {
			project = ix.project[ix.first(keys)]
		}
		start := time.Now()
		res, err := svc.Search(ctx, memory.SearchInput{Query: q.Text, K: o.K, IncludeOld: o.IncludeOld, Project: project})
		lat := time.Since(start)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", q.ID, err)
		}
		qr := QueryResult{Query: q, Hits: len(res.Hits), Mode: res.Mode, Latency: lat,
			Tokens: float64(len(render.Search(res))) / charsPerToken, HasOutdated: len(g.Outdated[q.ID]) > 0,
			Inactive: ix.allInactive(keys)}
		if len(res.Hits) > 0 {
			qr.Top = res.Hits[0].Score
		}
		for i, h := range res.Hits {
			if accepted[h.ID] && qr.Rank == 0 {
				qr.Rank = i + 1
			}
			if outdated[h.ID] && qr.OutdatedRank == 0 {
				qr.OutdatedRank = i + 1
			}
		}
		out = append(out, qr)
	}
	return out, nil
}

type index struct {
	byKey   map[string][]int64  // source key -> memory ids (all parts)
	byBase  map[string][]int64  // title without a "(part n/m)" suffix -> memory ids
	base    map[int64]string    // memory id -> base title
	project map[int64]string    // memory id -> project
	active  map[int64]bool      // memory id -> status is active
	vecs    map[int64][]float32 // memory id -> unit vector
}

var partSuffix = regexp.MustCompile(` \(part \d+/\d+\)$`)

// resolve returns the memory ids a gold key names: "mc:<id>" is a memory id
// (when it exists), anything else a source key of the import map.
func (ix *index) resolve(key string) []int64 {
	if rest, ok := strings.CutPrefix(key, memoryKeyPrefix); ok {
		id, err := strconv.ParseInt(rest, 10, 64)
		if _, known := ix.base[id]; err != nil || !known {
			return nil
		}
		return []int64{id}
	}
	return ix.byKey[key]
}

func (ix *index) resolveAll(keys []string) map[int64]bool {
	set := map[int64]bool{}
	for _, k := range keys {
		for _, id := range ix.resolve(k) {
			set[id] = true
		}
	}
	return set
}

// first returns the first memory id the keys name, or 0.
func (ix *index) first(keys []string) int64 {
	for _, k := range keys {
		if ids := ix.resolve(k); len(ids) > 0 {
			return ids[0]
		}
	}
	return 0
}

// allInactive reports whether the keys name memories and none is active.
func (ix *index) allInactive(keys []string) bool {
	ids := ix.resolveAll(keys)
	for id := range ids {
		if ix.active[id] {
			return false
		}
	}
	return len(ids) > 0
}

// outdated returns the memory ids of the outdated keys, with the other parts
// of the same document unless title acceptance is off; a memory that is
// itself named as an answer is never outdated.
func (ix *index) outdated(keys []string, answers map[int64]bool, o Options) map[int64]bool {
	set := map[int64]bool{}
	for id := range ix.resolveAll(keys) {
		set[id] = true
		if !o.NoTitleAccept {
			for _, other := range ix.byBase[ix.base[id]] {
				set[other] = true
			}
		}
	}
	for id := range answers {
		delete(set, id)
	}
	return set
}

func loadIndex(ctx context.Context, db *store.DB) (*index, error) {
	ix := &index{byKey: map[string][]int64{}, byBase: map[string][]int64{}, base: map[int64]string{},
		project: map[int64]string{}, active: map[int64]bool{}, vecs: map[int64][]float32{}}
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

	rows, err = db.SQL().QueryContext(ctx, `select id, title, project, status from memories`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var title, project, status string
		if err := rows.Scan(&id, &title, &project, &status); err != nil {
			rows.Close()
			return nil, err
		}
		b := strings.ToLower(partSuffix.ReplaceAllString(title, ""))
		ix.base[id] = b
		ix.byBase[b] = append(ix.byBase[b], id)
		ix.project[id] = project
		ix.active[id] = status == "active"
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
	set := ix.resolveAll(keys)
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
	Inactive                int // questions whose every answer is superseded or archived

	// Questions that list outdated answers, and the share of them where the
	// current answer is ranked above every outdated one (current found, and no
	// outdated hit before it) or an outdated one above the current one.
	WithOutdated                int
	CurrentFirst, OutdatedFirst float64
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
		if r.Inactive {
			s.Inactive++
		}
		if r.HasOutdated {
			s.WithOutdated++
			if r.Rank > 0 && (r.OutdatedRank == 0 || r.Rank < r.OutdatedRank) {
				s.CurrentFirst++
			}
			if r.OutdatedRank > 0 && (r.Rank == 0 || r.OutdatedRank < r.Rank) {
				s.OutdatedFirst++
			}
		}
	}
	if s.WithOutdated > 0 {
		s.CurrentFirst /= float64(s.WithOutdated)
		s.OutdatedFirst /= float64(s.WithOutdated)
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
