package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/labdrian-ai/memorycard/internal/embed"
	"github.com/labdrian-ai/memorycard/internal/memory"
	"github.com/labdrian-ai/memorycard/internal/store"
)

// Granularity decides what one memory holds.
type Granularity string

const (
	// Session stores each chat session as one memory, as an agent saving a
	// whole conversation would.
	Session Granularity = "session"
	// Round stores each user turn with the assistant replies that follow it.
	Round Granularity = "round"
)

const benchProject = "bench"

// Turn is one message of a LongMemEval chat session.
type Turn struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	HasAnswer bool   `json:"has_answer"`
}

// Question is one LongMemEval question with its haystack of sessions.
type Question struct {
	ID               string          `json:"question_id"`
	Type             string          `json:"question_type"`
	Text             string          `json:"question"`
	Dates            []string        `json:"haystack_dates"`
	SessionIDs       []string        `json:"haystack_session_ids"`
	Sessions         [][]Turn        `json:"haystack_sessions"`
	AnswerSessionIDs []string        `json:"answer_session_ids"`
	Answer           json.RawMessage `json:"answer"` // a string or a number
	Date             string          `json:"question_date"`
}

// AnswerText returns the reference answer as text.
func (q Question) AnswerText() string {
	var s string
	if json.Unmarshal(q.Answer, &s) == nil {
		return s
	}
	return string(q.Answer)
}

// Abstention reports whether the question has no answer in its haystack.
func (q Question) Abstention() bool { return strings.HasSuffix(q.ID, "_abs") }

// Load reads a LongMemEval JSON file.
func Load(path string) ([]Question, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var qs []Question
	if err := json.Unmarshal(b, &qs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return qs, nil
}

// Sample returns n questions picked with seed, in a stable order; n <= 0 or
// n >= len(qs) keeps them all.
func Sample(qs []Question, n int, seed int64) []Question {
	if n <= 0 || n >= len(qs) {
		return qs
	}
	idx := rand.New(rand.NewSource(seed)).Perm(len(qs))[:n]
	sort.Ints(idx)
	out := make([]Question, n)
	for i, j := range idx {
		out[i] = qs[j]
	}
	return out
}

// Doc is one memory to save, with the session it came from.
type Doc struct {
	SessionID, Title, Body string
}

// Docs turns the haystack of q into memories.
func Docs(q Question, g Granularity) []Doc {
	var docs []Doc
	for i, turns := range q.Sessions {
		sid, date := at(q.SessionIDs, i), at(q.Dates, i)
		title := "Chat"
		if date != "" {
			title = "Chat on " + date
		}
		if g == Session {
			docs = append(docs, Doc{sid, title, render(turns)})
			continue
		}
		for r, round := range rounds(turns) {
			docs = append(docs, Doc{sid, fmt.Sprintf("%s, round %d", title, r+1), render(round)})
		}
	}
	return docs
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}

func render(turns []Turn) string {
	parts := make([]string, len(turns))
	for i, t := range turns {
		parts[i] = t.Role + ": " + t.Content
	}
	return strings.Join(parts, "\n\n")
}

// rounds groups turns so each round starts at a user turn.
func rounds(turns []Turn) [][]Turn {
	var out [][]Turn
	for _, t := range turns {
		if t.Role == "user" || len(out) == 0 {
			out = append(out, nil)
		}
		out[len(out)-1] = append(out[len(out)-1], t)
	}
	return out
}

// Result is the outcome of one question.
type Result struct {
	ID         string  `json:"id"`
	Type       string  `json:"type"`
	Abstention bool    `json:"abstention,omitempty"`
	Docs       int     `json:"docs"`
	Mode       string  `json:"mode,omitempty"`
	Ranks      []int   `json:"ranks"` // per answer session: 1-based rank of its first hit, 0 if not in the top memory.MaxK
	SearchMs   float64 `json:"search_ms"`
}

// Setup is how a question is run.
type Setup struct {
	Granularity Granularity
	Embedder    embed.Embedder // nil runs lexical only
	Hybrid      bool           // fuse dense and lexical rankings
}

// RunQuestion builds a fresh database in dir from the haystack of q, embeds it
// and searches it with the question.
func RunQuestion(ctx context.Context, q Question, setup Setup, dir string) (Result, error) {
	res := Result{ID: q.ID, Type: q.Type, Abstention: q.Abstention(), Ranks: []int{}}
	if res.Abstention {
		return res, nil
	}
	h, err := build(ctx, q, setup, dir)
	if err != nil {
		return res, err
	}
	defer h.db.Close()
	res.Docs = h.docs
	found, err := h.search(ctx, q, &res)
	if err != nil {
		return res, err
	}
	res.Ranks = h.ranks(q, found.Hits)
	return res, nil
}

// haystack is the database built from one question's sessions.
type haystack struct {
	db     *store.DB
	svc    *memory.Service
	docs   int
	owners map[int64][]string // memory id -> the sessions it came from
	dates  map[int64]string   // memory id -> the date of its session
}

// build saves the haystack of q as memories in a fresh database in dir and
// embeds them.
func build(ctx context.Context, q Question, setup Setup, dir string) (*haystack, error) {
	db, err := store.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		return nil, err
	}
	h := &haystack{db: db, svc: memory.New(db), owners: map[int64][]string{}, dates: map[int64]string{}}
	dates := map[string]string{}
	for i, sid := range q.SessionIDs {
		dates[sid] = at(q.Dates, i)
	}
	for _, d := range Docs(q, setup.Granularity) {
		saved, err := h.svc.Save(ctx, memory.SaveInput{Project: benchProject, Type: "conversation", Title: d.Title, Body: d.Body})
		if errors.Is(err, memory.ErrEmpty) {
			continue
		}
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: save %s: %w", q.ID, d.SessionID, err)
		}
		h.docs++
		ids := saved.Parts
		if len(ids) == 0 {
			ids = []int64{saved.ID}
		}
		for _, id := range ids {
			h.owners[id] = append(h.owners[id], d.SessionID)
			h.dates[id] = dates[d.SessionID]
		}
	}
	if setup.Embedder != nil {
		h.svc.SetEmbedder(setup.Embedder)
		h.svc.SetHybrid(setup.Hybrid)
		for {
			n, err := h.svc.EmbedPending(ctx, 32)
			if err != nil {
				db.Close()
				return nil, fmt.Errorf("%s: embed: %w", q.ID, err)
			}
			if n == 0 {
				break
			}
		}
	}
	return h, nil
}

// search runs the question and records its mode and latency in res.
func (h *haystack) search(ctx context.Context, q Question, res *Result) (memory.SearchResult, error) {
	start := time.Now()
	found, err := h.svc.Search(ctx, memory.SearchInput{Query: q.Text, Project: benchProject, K: memory.MaxK, Detail: memory.DetailTitle})
	if err != nil {
		return found, fmt.Errorf("%s: search: %w", q.ID, err)
	}
	res.SearchMs = float64(time.Since(start).Microseconds()) / 1000
	res.Mode = found.Mode
	return found, nil
}

// ranks gives, per answer session of q, the 1-based rank of its first hit, 0
// if no hit came from it.
func (h *haystack) ranks(q Question, hits []memory.Hit) []int {
	out := []int{}
	for _, sid := range q.AnswerSessionIDs {
		rank := 0
		for i, hit := range hits {
			if contains(h.owners[hit.ID], sid) {
				rank = i + 1
				break
			}
		}
		out = append(out, rank)
	}
	return out
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// Summary aggregates results. Any[k] is the share of scored questions with at
// least one answer session in the top k; All[k] with every answer session in
// the top k.
type Summary struct {
	Scored      int                 `json:"scored"`
	Abstentions int                 `json:"abstentions"`
	Any         map[int]float64     `json:"recall_any"`
	All         map[int]float64     `json:"recall_all"`
	ByType      map[string]*Summary `json:"by_type,omitempty"`
}

// Summarize computes recall at each k, overall and per question type.
func Summarize(results []Result, ks []int) Summary {
	s := summarize(results, ks)
	groups := map[string][]Result{}
	for _, r := range results {
		groups[r.Type] = append(groups[r.Type], r)
	}
	s.ByType = map[string]*Summary{}
	for t, rs := range groups {
		sub := summarize(rs, ks)
		s.ByType[t] = &sub
	}
	return s
}

func summarize(results []Result, ks []int) Summary {
	s := Summary{Any: map[int]float64{}, All: map[int]float64{}}
	for _, r := range results {
		if r.Abstention {
			s.Abstentions++
			continue
		}
		if len(r.Ranks) == 0 {
			continue
		}
		s.Scored++
		for _, k := range ks {
			anyHit, allHit := false, true
			for _, rank := range r.Ranks {
				in := rank >= 1 && rank <= k
				anyHit = anyHit || in
				allHit = allHit && in
			}
			if anyHit {
				s.Any[k]++
			}
			if allHit {
				s.All[k]++
			}
		}
	}
	if s.Scored > 0 {
		for _, k := range ks {
			s.Any[k] /= float64(s.Scored)
			s.All[k] /= float64(s.Scored)
		}
	}
	return s
}
