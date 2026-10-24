package memory

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/labdrian-ai/memorycard/internal/embed"
)

const (
	// DefaultK is how many results Search returns when K is not set.
	DefaultK = 5
	// MaxK caps K.
	MaxK = 20
	// SnippetRunes is the snippet length returned with DetailSnippet.
	SnippetRunes = 150
	// ConfidenceCosine is the top similarity below which a dense answer is
	// flagged low confidence. Calibrated (T9) between the best score of 20
	// off-topic questions (max 0.469) and the worst of 100 answerable ones
	// (min 0.499).
	ConfidenceCosine = 0.48
	// StaleAfter is the age after which decision and policy memories are flagged.
	StaleAfter = 183 * 24 * time.Hour
)

// Search modes and confidence levels.
const (
	ModeDense   = "dense"
	ModeLexical = "lexical"

	ConfidenceOK      = "ok"
	ConfidenceLow     = "low"
	ConfidenceUnknown = "unknown" // lexical scores are not comparable to a threshold

	DetailTitle   = "title"
	DetailSnippet = "snippet"
)

// ErrEmptyQuery is returned for a blank query.
var ErrEmptyQuery = errors.New("query must not be empty")

// SearchInput describes a search.
type SearchInput struct {
	Query      string
	Project    string // empty means all projects
	Type       string
	K          int    // defaults to DefaultK, capped at MaxK
	Detail     string // DetailSnippet (default) or DetailTitle
	IncludeOld bool   // include superseded and archived memories
}

// Hit is one search result.
type Hit struct {
	ID      int64
	Type    string
	Title   string
	Snippet string // empty with DetailTitle
	Score   float64
	Stale   bool
	Project string
}

// SearchResult is the answer to a search.
type SearchResult struct {
	Mode       string
	Confidence string
	Widened    bool // the project had no useful answer, so all projects were searched
	Hits       []Hit
}

type candidate struct {
	Hit
	updated string
	body    string
}

// Search finds memories. It uses dense similarity when an embedder is
// available and falls back to lexical BM25 when it is not. When the project
// has nothing that answers the query (no hits, or low confidence) and another
// project does, the answer comes from all projects and is marked Widened, so
// work filed under a different project name is still found.
func (s *Service) Search(ctx context.Context, in SearchInput) (SearchResult, error) {
	res, err := s.find(ctx, in)
	if err != nil || in.Project == "" || res.Confidence == ConfidenceOK || res.Confidence == ConfidenceUnknown {
		return res, err
	}
	all := in
	all.Project = ""
	wide, err := s.find(ctx, all)
	if err != nil {
		return SearchResult{}, err
	}
	if wide.Confidence != ConfidenceOK {
		return res, nil
	}
	wide.Widened = true
	return wide, nil
}

func (s *Service) find(ctx context.Context, in SearchInput) (SearchResult, error) {
	q := strings.TrimSpace(in.Query)
	if q == "" {
		return SearchResult{}, ErrEmptyQuery
	}
	k := in.K
	if k <= 0 {
		k = DefaultK
	}
	if k > MaxK {
		k = MaxK
	}
	f := buildFilter(in)

	var topic []candidate
	if strings.Contains(q, "/") {
		var err error
		if topic, err = s.byTopic(ctx, topicKey(q), f, k); err != nil {
			return SearchResult{}, err
		}
	}

	var res SearchResult
	var found []candidate
	if s.embedder != nil {
		dense, err := s.dense(ctx, q, f, k)
		if err == nil {
			res.Mode, found = ModeDense, dense
		} else if !errors.Is(err, embed.ErrUnavailable) {
			return SearchResult{}, err
		}
	}
	if res.Mode == "" {
		lex, err := s.lexical(ctx, q, f, k)
		if err != nil {
			return SearchResult{}, err
		}
		res.Mode, found = ModeLexical, lex
	}

	merged := mergeUnique(topic, found, k)
	res.Hits = s.finish(merged, in.Detail)
	switch {
	case len(topic) > 0:
		res.Confidence = ConfidenceOK
	case len(merged) == 0:
		res.Confidence = ConfidenceLow
	case res.Mode == ModeLexical:
		res.Confidence = ConfidenceUnknown
	case merged[0].Score >= ConfidenceCosine:
		res.Confidence = ConfidenceOK
	default:
		res.Confidence = ConfidenceLow
	}
	return res, nil
}

type filter struct {
	where string
	args  []any
}

func buildFilter(in SearchInput) filter {
	parts := []string{"1=1"}
	var args []any
	if !in.IncludeOld {
		parts = append(parts, "m.status = 'active'")
	}
	if in.Project != "" {
		parts = append(parts, "(m.project = ? or m.scope = 'global')")
		args = append(args, in.Project)
	}
	if in.Type != "" {
		parts = append(parts, "m.type = ?")
		args = append(args, in.Type)
	}
	return filter{where: strings.Join(parts, " and "), args: args}
}

func (s *Service) byTopic(ctx context.Context, key string, f filter, k int) ([]candidate, error) {
	rows, err := s.db.SQL().QueryContext(ctx,
		`select m.id, m.type, m.title, m.body, m.updated_at, m.project from memories m
		 where m.topic_key = ? and `+f.where+` order by m.updated_at desc limit ?`,
		append(append([]any{key}, f.args...), k)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		c := candidate{Hit: Hit{Score: 1}}
		if err := rows.Scan(&c.ID, &c.Type, &c.Title, &c.body, &c.updated, &c.Project); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Service) dense(ctx context.Context, q string, f filter, k int) ([]candidate, error) {
	vecs, err := s.embedder.Embed(ctx, []string{q}, embed.KindQuery)
	if err != nil {
		return nil, err
	}
	qv := unit(vecs[0])
	rows, err := s.db.SQL().QueryContext(ctx,
		`select m.id, m.type, m.title, m.body, m.updated_at, m.project, v.vec
		 from vectors v join memories m on m.id = v.memory_id
		 where v.model = ? and `+f.where,
		append([]any{s.embedder.Model()}, f.args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var c candidate
		var blob []byte
		if err := rows.Scan(&c.ID, &c.Type, &c.Title, &c.body, &c.updated, &c.Project, &blob); err != nil {
			return nil, err
		}
		v := decodeVec(blob)
		if len(v) != len(qv) {
			continue
		}
		c.Score = dot(qv, v)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`the and for with that this from have what when where which why how does did was were
		are can could del las los una uno por que como para con sus sin sobre entre cuando donde porque cual cuales fue
		era son ser hay unos unas esta este esto esos esas pero mas muy`) {
		stopwords[w] = true
	}
}

var wordRe = regexp.MustCompile(`[\p{L}\p{N}_]+`)

// ftsQuery builds an OR query of quoted terms, so no FTS operator in the
// user's text is ever interpreted.
func ftsQuery(q string) string {
	seen := map[string]bool{}
	var terms []string
	for _, w := range wordRe.FindAllString(strings.ToLower(q), -1) {
		if len([]rune(w)) < 3 || stopwords[w] || seen[w] {
			continue
		}
		seen[w] = true
		terms = append(terms, `"`+w+`"`)
	}
	return strings.Join(terms, " OR ")
}

func (s *Service) lexical(ctx context.Context, q string, f filter, k int) ([]candidate, error) {
	match := ftsQuery(q)
	if match == "" {
		return nil, nil
	}
	rows, err := s.db.SQL().QueryContext(ctx,
		`select m.id, m.type, m.title, m.body, m.updated_at, m.project, bm25(fts, 5.0, 1.0)
		 from fts join memories m on m.id = fts.rowid
		 where fts match ? and `+f.where+` order by bm25(fts, 5.0, 1.0) limit ?`,
		append(append([]any{match}, f.args...), k)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.ID, &c.Type, &c.Title, &c.body, &c.updated, &c.Project, &c.Score); err != nil {
			return nil, err
		}
		c.Score = -c.Score // bm25() is lower-is-better; flip so higher is better
		out = append(out, c)
	}
	return out, rows.Err()
}

func mergeUnique(first, second []candidate, k int) []candidate {
	seen := map[int64]bool{}
	var out []candidate
	for _, c := range append(append([]candidate{}, first...), second...) {
		if seen[c.ID] || len(out) == k {
			continue
		}
		seen[c.ID] = true
		out = append(out, c)
	}
	return out
}

func (s *Service) finish(cs []candidate, detail string) []Hit {
	now := s.now()
	hits := make([]Hit, 0, len(cs))
	for _, c := range cs {
		h := c.Hit
		h.Stale = isStale(c.Type, c.updated, now)
		if detail != DetailTitle {
			h.Snippet = snippet(c.body)
		}
		hits = append(hits, h)
	}
	return hits
}

func isStale(typ, updated string, now time.Time) bool {
	if typ != "decision" && typ != "policy" {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, updated)
	return err == nil && now.Sub(t) > StaleAfter
}

func snippet(body string) string {
	r := []rune(strings.Join(strings.Fields(strings.ReplaceAll(body, "**", "")), " "))
	if len(r) > SnippetRunes {
		r = r[:SnippetRunes]
	}
	return string(r)
}
