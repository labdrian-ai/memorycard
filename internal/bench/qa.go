package bench

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Model answers a prompt; the reader and the judge of end-to-end QA are both
// models.
type Model interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// QASetup is how a question is answered and judged.
type QASetup struct {
	K      int // sessions given to the reader
	Reader Model
	Judge  Model
}

// QAResult is the outcome of answering one question.
type QAResult struct {
	ID         string  `json:"id"`
	Type       string  `json:"type"`
	Abstention bool    `json:"abstention,omitempty"`
	Mode       string  `json:"mode,omitempty"`
	Ranks      []int   `json:"ranks"`
	Read       int     `json:"read"` // sessions given to the reader
	Hypothesis string  `json:"hypothesis"`
	Verdict    string  `json:"verdict"`
	Correct    bool    `json:"correct"`
	SearchMs   float64 `json:"search_ms"`
	AnswerMs   float64 `json:"answer_ms"`
}

// The reader and judge prompts are copied from the LongMemEval repository
// (src/generation/run_generation.py, chain-of-thought reader over chat
// history; src/evaluation/evaluate_qa.py), so scores compare with theirs.
const readerPrompt = "I will give you several history chats between you and a user. Please answer the question based on the relevant chat history. Answer the question step by step: first extract all the relevant information, and then reason over the information to get the answer.\n\n\nHistory Chats:\n\n%s\n\nCurrent Date: %s\nQuestion: %s\nAnswer (step by step):"

const (
	judgeIntro    = "I will give you a question, a correct answer, and a response from a model. Please answer yes if the response contains the correct answer. Otherwise, answer no."
	judgeSubset   = " If the response is equivalent to the correct answer or contains all the intermediate steps to get the correct answer, you should also answer yes. If the response only contains a subset of the information required by the answer, answer no."
	judgeBody     = "\n\nQuestion: %s\n\nCorrect Answer: %s\n\nModel Response: %s\n\nIs the model response correct? Answer yes or no only."
	judgeTemporal = judgeIntro + judgeSubset + " In addition, do not penalize off-by-one errors for the number of days. If the question asks for the number of days/weeks/months, etc., and the model makes off-by-one errors (e.g., predicting 19 days when the answer is 18), the model's response is still correct. " + judgeBody
	judgeUpdate   = judgeIntro + " If the response contains some previous information along with an updated answer, the response should be considered as correct as long as the updated answer is the required answer." + judgeBody
	judgePref     = "I will give you a question, a rubric for desired personalized response, and a response from a model. Please answer yes if the response satisfies the desired response. Otherwise, answer no. The model does not need to reflect all the points in the rubric. The response is correct as long as it recalls and utilizes the user's personal information correctly.\n\nQuestion: %s\n\nRubric: %s\n\nModel Response: %s\n\nIs the model response correct? Answer yes or no only."
	judgeAbstain  = "I will give you an unanswerable question, an explanation, and a response from a model. Please answer yes if the model correctly identifies the question as unanswerable. The model could say that the information is incomplete, or some other information is given but the asked information is not.\n\nQuestion: %s\n\nExplanation: %s\n\nModel Response: %s\n\nDoes the model correctly identify the question as unanswerable? Answer yes or no only."
)

// JudgePrompt returns the official LongMemEval judge prompt for q.
func JudgePrompt(q Question, hypothesis string) (string, error) {
	tmpl := ""
	switch {
	case q.Abstention():
		tmpl = judgeAbstain
	case q.Type == "single-session-user" || q.Type == "single-session-assistant" || q.Type == "multi-session":
		tmpl = judgeIntro + judgeSubset + " " + judgeBody
	case q.Type == "temporal-reasoning":
		tmpl = judgeTemporal
	case q.Type == "knowledge-update":
		tmpl = judgeUpdate
	case q.Type == "single-session-preference":
		tmpl = judgePref
	default:
		return "", fmt.Errorf("%s: no judge prompt for question type %q", q.ID, q.Type)
	}
	return fmt.Sprintf(tmpl, q.Text, q.AnswerText(), hypothesis), nil
}

// RunQA builds the haystack of q, gives its top K sessions to the reader and
// asks the judge whether the answer is right. Abstention questions are asked
// too: the right answer says the history does not hold it.
func RunQA(ctx context.Context, q Question, setup Setup, dir string, qa QASetup) (QAResult, error) {
	res := QAResult{ID: q.ID, Type: q.Type, Abstention: q.Abstention(), Ranks: []int{}}
	if _, err := JudgePrompt(q, ""); err != nil {
		return res, err
	}
	h, err := build(ctx, q, setup, dir)
	if err != nil {
		return res, err
	}
	defer h.db.Close()
	var r Result
	found, err := h.search(ctx, q, &r)
	if err != nil {
		return res, err
	}
	res.Mode, res.SearchMs, res.Ranks = r.Mode, r.SearchMs, h.ranks(q, found.Hits)

	type session struct{ date, body string }
	var read []session
	for _, hit := range found.Hits {
		if len(read) == qa.K {
			break
		}
		m, err := h.svc.Get(ctx, hit.ID)
		if err != nil {
			return res, fmt.Errorf("%s: get %d: %w", q.ID, hit.ID, err)
		}
		read = append(read, session{h.dates[hit.ID], m.Body})
	}
	res.Read = len(read)
	// As the official reader does, the top sessions are shown oldest first.
	sort.SliceStable(read, func(i, j int) bool { return read[i].date < read[j].date })
	var history strings.Builder
	for i, s := range read {
		fmt.Fprintf(&history, "\n### Session %d:\nSession Date: %s\nSession Content:\n\n%s\n", i+1, s.date, s.body)
	}

	start := time.Now()
	if res.Hypothesis, err = qa.Reader.Complete(ctx, fmt.Sprintf(readerPrompt, history.String(), q.Date, q.Text)); err != nil {
		return res, fmt.Errorf("%s: reader: %w", q.ID, err)
	}
	res.AnswerMs = float64(time.Since(start).Microseconds()) / 1000
	res.Hypothesis = strings.TrimSpace(res.Hypothesis)
	judgePrompt, _ := JudgePrompt(q, res.Hypothesis)
	if res.Verdict, err = qa.Judge.Complete(ctx, judgePrompt); err != nil {
		return res, fmt.Errorf("%s: judge: %w", q.ID, err)
	}
	res.Verdict = strings.TrimSpace(res.Verdict)
	// The official evaluator counts a verdict as correct when it contains "yes".
	res.Correct = strings.Contains(strings.ToLower(res.Verdict), "yes")
	return res, nil
}

// QAScore is the accuracy over N answered questions.
type QAScore struct {
	N        int     `json:"n"`
	Accuracy float64 `json:"accuracy"`
}

// QASummary aggregates QA results, overall, per question type and over the
// abstention questions.
type QASummary struct {
	QAScore
	ByType     map[string]QAScore `json:"by_type"`
	Abstention QAScore            `json:"abstention"`
}

// SummarizeQA computes the accuracy of results.
func SummarizeQA(results []QAResult) QASummary {
	score := func(rs []QAResult) QAScore {
		x := QAScore{N: len(rs)}
		for _, r := range rs {
			if r.Correct {
				x.Accuracy++
			}
		}
		if x.N > 0 {
			x.Accuracy /= float64(x.N)
		}
		return x
	}
	byType := map[string][]QAResult{}
	var abs []QAResult
	for _, r := range results {
		byType[r.Type] = append(byType[r.Type], r)
		if r.Abstention {
			abs = append(abs, r)
		}
	}
	s := QASummary{QAScore: score(results), ByType: map[string]QAScore{}, Abstention: score(abs)}
	for t, rs := range byType {
		s.ByType[t] = score(rs)
	}
	return s
}
