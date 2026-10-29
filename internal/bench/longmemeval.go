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
	ID               string   `json:"question_id"`
	Type             string   `json:"question_type"`
	Text             string   `json:"question"`
	Dates            []string `json:"haystack_dates"`
	SessionIDs       []string `json:"haystack_session_ids"`
	Sessions         [][]Turn `json:"haystack_sessions"`
	AnswerSessionIDs []string `json:"answer_session_ids"`
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

// RunQuestion builds a fresh database in dir from the haystack of q, embeds it
// with emb (nil runs lexical only) and searches it with the question.
func RunQuestion(ctx context.Context, q Question, g Granularity, emb embed.Embedder, dir string) (Result, error) {
	res := Result{ID: q.ID, Type: q.Type, Abstention: q.Abstention(), Ranks: []int{}}
	if res.Abstention {
		return res, nil
	}
	db, err := store.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		return res, err
	}
	defer db.Close()
	svc := memory.New(db)

	owners := map[int64][]string{}
	for _, d := range Docs(q, g) {
		saved, err := svc.Save(ctx, memory.SaveInput{Project: benchProject, Type: "conversation", Title: d.Title, Body: d.Body})
		if errors.Is(err, memory.ErrEmpty) {
			continue
		}
		if err != nil {
			return res, fmt.Errorf("%s: save %s: %w", q.ID, d.SessionID, err)
		}
		res.Docs++
		ids := saved.Parts
		if len(ids) == 0 {
			ids = []int64{saved.ID}
		}
		for _, id := range ids {
			owners[id] = append(owners[id], d.SessionID)
		}
	}
	if emb != nil {
		svc.SetEmbedder(emb)
		for {
			n, err := svc.EmbedPending(ctx, 32)
			if err != nil {
				return res, fmt.Errorf("%s: embed: %w", q.ID, err)
			}
			if n == 0 {
				break
			}
		}
	}

	start := time.Now()
	found, err := svc.Search(ctx, memory.SearchInput{Query: q.Text, Project: benchProject, K: memory.MaxK, Detail: memory.DetailTitle})
	if err != nil {
		return res, fmt.Errorf("%s: search: %w", q.ID, err)
	}
	res.SearchMs = float64(time.Since(start).Microseconds()) / 1000
	res.Mode = found.Mode
	for _, sid := range q.AnswerSessionIDs {
		rank := 0
		for i, h := range found.Hits {
			if contains(owners[h.ID], sid) {
				rank = i + 1
				break
			}
		}
		res.Ranks = append(res.Ranks, rank)
	}
	return res, nil
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
