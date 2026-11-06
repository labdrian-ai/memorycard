package memory

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
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
	//
	// It only separates off-topic questions. A question about a known subject
	// whose specific fact is not stored scores like an answerable one (40
	// near-topic negatives: top cosine p50 0.597 vs 0.605 for 190 answerable
	// questions; margins and lexical coverage overlap too), so "ok" means
	// related memories were found, not that they contain the answer. Judging
	// that is left to the caller, who reads the excerpts anyway.
	ConfidenceCosine = 0.48
	// FusionPool is how many dense candidates hybrid search ranks, and
	// FusionLexPool how many lexical ones join them; FusionRRF is the constant
	// of reciprocal rank fusion (score = sum of weight/(FusionRRF + rank)); 60 is
	// the usual value. The lexical list counts at FusionLexWeight and only its
	// head, because BM25 is noisy on short Spanish questions.
	FusionPool      = 50
	FusionLexPool   = 10
	FusionLexWeight = 0.5
	FusionRRF       = 60
	// OverlapBoost scales a dense score by 1 + OverlapBoost * the share of
	// query terms in the title and the best OverlapWindow bytes of the body.
	// The window is the embed window's size, so long bodies gain no edge from
	// length, while words beyond what was embedded still count.
	OverlapBoost  = 0.3
	OverlapWindow = 1500
	// SummaryWeight scales a session summary's dense score in a search that
	// does not filter by type. Summaries recap whole sessions and match almost
	// anything, yet some facts live only in one. Swept on the gold sets: 0.90
	// was the only weight that lowered no set (gold2 hit@5 75 -> 83, others
	// equal or better); 0.95 and 1.0 lost points on gold and gold3, 0.85 and
	// below gained little on gold2.
	SummaryWeight = 0.90
	// StaleAfter is the age after which decision and policy memories are flagged.
	StaleAfter = 183 * 24 * time.Hour
)

// Search modes and confidence levels.
const (
	ModeDense   = "dense"
	ModeLexical = "lexical"
	ModeHybrid  = "hybrid" // dense and lexical rankings fused

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
	// Siblings are the ids of the other parts when the hit is one part of a
	// document saved in parts; only the best-ranked part is returned.
	Siblings []int64
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
	if s.embedder != nil && in.Project != "" {
		// A widened search asks the same question again: embed it once.
		memo := *s
		memo.embedder = &queryMemo{Embedder: s.embedder}
		s = &memo
	}
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

// queryMemo remembers the vectors of query texts for the length of one
// search, failures included, so the widened pass does not embed again.
type queryMemo struct {
	embed.Embedder
	text string
	vecs [][]float32
	err  error
	done bool
}

func (m *queryMemo) Embed(ctx context.Context, texts []string, kind embed.Kind) ([][]float32, error) {
	if kind != embed.KindQuery || len(texts) != 1 {
		return m.Embedder.Embed(ctx, texts, kind)
	}
	if !m.done || m.text != texts[0] {
		m.vecs, m.err = m.Embedder.Embed(ctx, texts, kind)
		m.text, m.done = texts[0], true
	}
	return m.vecs, m.err
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
		// An exact topic key is asked for by name, so the lookup sees every
		// type, session summaries included.
		var err error
		if topic, err = s.byTopic(ctx, topicKey(q), topicFilter(in), k); err != nil {
			return SearchResult{}, err
		}
	}

	// The ranked lists are cut to k only after the parts of one document are
	// collapsed, so k distinct documents come back.
	pool := max(k, FusionPool)
	var res SearchResult
	var found []candidate
	var cosine float64 // best dense similarity; confidence is judged on it in every mode
	if s.embedder != nil {
		dense, err := s.dense(ctx, q, topicFilter(in), pool)
		if err == nil && in.Type == "" {
			dense = weighSummaries(dense)
		}
		if err == nil && len(dense) > 0 {
			cosine = dense[0].Score
		}
		switch {
		case err == nil && s.hybrid:
			lex, err := s.lexical(ctx, q, f, FusionLexPool, false)
			if err != nil {
				return SearchResult{}, err
			}
			res.Mode, found = ModeHybrid, fuse(boostOverlap(q, dense), lex, len(dense)+len(lex))
		case err == nil:
			res.Mode, found = ModeDense, dense
		case !errors.Is(err, embed.ErrUnavailable):
			return SearchResult{}, err
		}
	}
	if res.Mode == "" {
		lex, err := s.lexical(ctx, q, f, pool, true)
		if err != nil {
			return SearchResult{}, err
		}
		res.Mode, found = ModeLexical, lex
	}

	merged := collapseParts(mergeUnique(topic, found, len(topic)+len(found)))
	if len(merged) > k {
		merged = merged[:k]
	}
	if err := s.siblings(ctx, merged); err != nil {
		return SearchResult{}, err
	}
	res.Hits = s.finish(merged, in.Detail)
	// A topic-key match is certain, but it says nothing about the rest of the
	// list: confidence is judged as for any other query.
	switch {
	case len(merged) == 0:
		res.Confidence = ConfidenceLow
	case res.Mode == ModeLexical:
		res.Confidence = ConfidenceUnknown
	case cosine >= ConfidenceCosine:
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

// weighSummaries scales the dense score of each session summary by
// SummaryWeight and re-sorts, so a summary ranks below a note that is about as
// close to the query but still answers when no note does.
func weighSummaries(cs []candidate) []candidate {
	for i := range cs {
		if cs[i].Type == "session_summary" {
			cs[i].Score *= SummaryWeight
		}
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].Score > cs[j].Score })
	return cs
}

// buildFilter restricts the lexical search. Session summaries recap whole
// sessions and match almost anything; BM25 has no score to weigh them by, so
// they only compete lexically when the caller asks for that type.
func buildFilter(in SearchInput) filter {
	f := topicFilter(in)
	if in.Type == "" {
		f.where += " and m.type != 'session_summary'"
	}
	return f
}

// topicFilter restricts the exact topic-key lookup.
func topicFilter(in SearchInput) filter {
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
	rows.Close()
	// A long memory is lifted towards its best chunk: its first-window cosine
	// plus ChunkLift times the gap up to the best chunk's cosine. A plain
	// maximum let long notes, which hold more chunks, overtake short notes
	// that answer the query better (T6 benchmark), and it moved scores off the
	// first-window scale ConfidenceCosine is calibrated on.
	pos := make(map[int64]int, len(out))
	first := make([]float64, len(out))
	for i, c := range out {
		pos[c.ID] = i
		first[i] = c.Score
	}
	best := append([]float64(nil), first...)
	chunks, err := s.db.SQL().QueryContext(ctx,
		`select c.memory_id, c.vec from vector_chunks c
		 join vectors v on v.memory_id = c.memory_id
		 join memories m on m.id = c.memory_id
		 where v.model = ? and `+f.where,
		append([]any{s.embedder.Model()}, f.args...)...)
	if err != nil {
		return nil, err
	}
	defer chunks.Close()
	for chunks.Next() {
		var id int64
		var blob []byte
		if err := chunks.Scan(&id, &blob); err != nil {
			return nil, err
		}
		i, ok := pos[id]
		v := decodeVec(blob)
		if !ok || len(v) != len(qv) {
			continue
		}
		if sc := dot(qv, v); sc > best[i] {
			best[i] = sc
		}
	}
	if err := chunks.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Score = first[i] + ChunkLift*(best[i]-first[i])
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

// foldAccents strips Spanish accents, as the FTS tokenizer does
// (remove_diacritics 2), so "más" is a stopword and "configuración" matches
// "configuracion".
var foldAccents = strings.NewReplacer("á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n")

// ftsQuery builds an OR query of quoted, accent-folded terms, so no FTS
// operator in the user's text is ever interpreted. With translate, when the
// query reads as Spanish (see readsSpanish), each Spanish word is followed by
// its English equivalents (see esStems), so a Spanish question also matches
// English notes. Only the lexical-only search translates: in hybrid search
// the dense list already bridges languages, and translating its lexical list
// lowered the gold set's Spanish hit@3 and MRR (S9 measurement).
func ftsQuery(q string, translate bool) string {
	terms := queryTerms(q, translate)
	for i, t := range terms {
		terms[i] = `"` + t + `"`
	}
	return strings.Join(terms, " OR ")
}

// queryTerms returns the distinct accent-folded words of q of three letters or
// more that are not stopwords, in order; with translate, a query that reads as
// Spanish also gets each Spanish word's English equivalents after it.
func queryTerms(q string, translate bool) []string {
	lowered := strings.ToLower(q)
	var words []string
	for _, w := range wordRe.FindAllString(lowered, -1) {
		words = append(words, foldAccents.Replace(w))
	}
	translate = translate && readsSpanish(lowered, words)
	seen := map[string]bool{}
	var terms []string
	add := func(w string) {
		if len([]rune(w)) < 3 || stopwords[w] || seen[w] {
			return
		}
		seen[w] = true
		terms = append(terms, w)
	}
	for _, w := range words {
		if len([]rune(w)) < 3 || stopwords[w] {
			continue
		}
		add(w)
		if translate {
			for _, e := range english(w) {
				add(e)
			}
		}
	}
	return terms
}

// lexical ranks memories by BM25 over the FTS index; translate is passed to
// ftsQuery.
func (s *Service) lexical(ctx context.Context, q string, f filter, k int, translate bool) ([]candidate, error) {
	match := ftsQuery(q, translate)
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

// fuse merges two rankings by weighted reciprocal rank fusion and keeps the top
// k. Rank positions, not scores, are summed, so cosine and BM25 need no common
// scale. Ties keep the dense order, and a dense winner (strictly above the
// second) stays first: the lexical list only reorders what follows it.
func fuse(dense, lex []candidate, k int) []candidate {
	score := map[int64]float64{}
	var order []candidate
	for li, list := range [][]candidate{dense, lex} {
		w := 1.0
		if li == 1 {
			w = FusionLexWeight
		}
		for i, c := range list {
			if _, seen := score[c.ID]; !seen {
				order = append(order, c)
			}
			score[c.ID] += w / float64(FusionRRF+i+1)
		}
	}
	for i := range order {
		order[i].Score = score[order[i].ID]
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].Score > order[j].Score })
	if len(dense) == 1 || len(dense) > 1 && dense[0].Score > dense[1].Score {
		for i := range order {
			if order[i].ID == dense[0].ID {
				top := order[i]
				copy(order[1:i+1], order[:i])
				order[0] = top
				break
			}
		}
	}
	if len(order) > k {
		order = order[:k]
	}
	return order
}

// boostOverlap rescales each dense score by the share of query terms in its
// title and best body window (see OverlapBoost) and reorders the list. Its
// terms are the query's own words, never the Spanish lookups of ftsQuery.
func boostOverlap(q string, cs []candidate) []candidate {
	terms := queryTerms(q, false)
	if len(terms) == 0 {
		return cs
	}
	out := append([]candidate{}, cs...)
	for i := range out {
		n := windowOverlap(out[i].Title, out[i].body, terms, OverlapWindow)
		out[i].Score *= 1 + OverlapBoost*float64(n)/float64(len(terms))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// windowOverlap counts the distinct terms found in the title plus those found
// together in the best window of win bytes of the body. Terms are distinct and
// accent-folded.
func windowOverlap(title, body string, terms []string, win int) int {
	idx := map[string]int{}
	for i, t := range terms {
		idx[t] = i
	}
	inTitle := make([]bool, len(terms))
	base := 0
	for _, w := range wordRe.FindAllString(strings.ToLower(title), -1) {
		if j, ok := idx[foldAccents.Replace(w)]; ok && !inTitle[j] {
			inTitle[j] = true
			base++
		}
	}
	lower := strings.ToLower(body)
	type hit struct{ pos, term int }
	var hits []hit
	for _, loc := range wordRe.FindAllStringIndex(lower, -1) {
		if j, ok := idx[foldAccents.Replace(lower[loc[0]:loc[1]])]; ok && !inTitle[j] {
			hits = append(hits, hit{loc[0], j})
		}
	}
	counts := make([]int, len(terms))
	best, cur, lo := 0, 0, 0
	for _, h := range hits {
		if counts[h.term] == 0 {
			cur++
		}
		counts[h.term]++
		for hits[lo].pos < h.pos-win {
			if counts[hits[lo].term]--; counts[hits[lo].term] == 0 {
				cur--
			}
			lo++
		}
		best = max(best, cur)
	}
	return base + best
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

// docKey names the document a candidate belongs to: its parts share project,
// type, base title and part count. A whole memory is its own document.
func docKey(c candidate) (string, bool) {
	m := partTitle.FindStringSubmatch(c.Title)
	if m == nil {
		return "", false
	}
	return strings.Join([]string{c.Project, c.Type, m[1], m[3]}, "\x00"), true
}

// collapseParts keeps the first, best-ranked part of each split document.
func collapseParts(cs []candidate) []candidate {
	seen := map[string]bool{}
	out := cs[:0:0]
	for _, c := range cs {
		if key, ok := docKey(c); ok {
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		out = append(out, c)
	}
	return out
}

// siblings fills in, for each hit that is a part, the ids of the other parts
// of its document (same project, scope, type and status), in part order.
func (s *Service) siblings(ctx context.Context, cs []candidate) error {
	for i := range cs {
		m := partTitle.FindStringSubmatch(cs[i].Title)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[3])
		if err != nil || n < 2 || n > maxSiblingParts {
			continue
		}
		titles := make([]any, 0, n)
		for p := 1; p <= n; p++ {
			titles = append(titles, fmt.Sprintf("%s (part %d/%d)", m[1], p, n))
		}
		rows, err := s.db.SQL().QueryContext(ctx,
			`select m.id, m.title from memories m join memories h on h.id = ?
			 where m.id != h.id and m.project = h.project and m.scope = h.scope and m.type = h.type
			   and m.status = h.status and m.title in (?`+strings.Repeat(",?", n-1)+`)`,
			append([]any{cs[i].ID}, titles...)...)
		if err != nil {
			return err
		}
		type part struct {
			id  int64
			idx int
		}
		var ps []part
		for rows.Next() {
			var id int64
			var title string
			if err := rows.Scan(&id, &title); err != nil {
				rows.Close()
				return err
			}
			idx, _ := strconv.Atoi(partTitle.FindStringSubmatch(title)[2])
			ps = append(ps, part{id, idx})
		}
		if err := rows.Close(); err != nil {
			return err
		}
		sort.SliceStable(ps, func(a, b int) bool {
			if ps[a].idx != ps[b].idx {
				return ps[a].idx < ps[b].idx
			}
			return ps[a].id < ps[b].id
		})
		for _, p := range ps {
			cs[i].Siblings = append(cs[i].Siblings, p.id)
		}
	}
	return nil
}

// maxSiblingParts bounds the sibling lookup of one hit; a document is split
// into 16 KB parts, so this is far above any real one.
const maxSiblingParts = 200

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
