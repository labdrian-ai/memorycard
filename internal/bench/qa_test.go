package bench

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// scripted is a Model that records its prompts and replies in turn.
type scripted struct {
	replies []string
	prompts []string
}

func (m *scripted) Complete(_ context.Context, prompt string) (string, error) {
	m.prompts = append(m.prompts, prompt)
	r := m.replies[0]
	m.replies = m.replies[1:]
	return r, nil
}

func TestRunQAReadsTheRetrievedSessionsAndJudgesTheAnswer(t *testing.T) {
	qs, err := Load(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	emb := keywordEmbedder{words: []string{"denver", "moved", "pizza"}}
	reader := &scripted{replies: []string{"You moved to Denver."}}
	judge := &scripted{replies: []string{"Yes."}}

	r, err := RunQA(context.Background(), qs[0], Setup{Granularity: Round, Embedder: emb}, t.TempDir(), QASetup{K: 2, Reader: reader, Judge: judge})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Correct || r.Hypothesis != "You moved to Denver." || r.Verdict != "Yes." || len(r.Ranks) != 1 || r.Ranks[0] != 1 || r.Read != 2 {
		t.Fatalf("result %+v, want correct, the reader's answer, ranks [1] and 2 sessions read", r)
	}
	p := reader.prompts[0]
	for _, want := range []string{
		"I will give you several history chats between you and a user. Please answer the question based on the relevant chat history. Answer the question step by step",
		"Session Date: 2023/05/02\nSession Content:\n",
		"I moved to Denver last month.",
		"### Session 2:",
		"Current Date: 2023/05/04\nQuestion: Where did I say I moved to? denver\nAnswer (step by step):",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("reader prompt lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "### Session 3:") {
		t.Fatalf("reader prompt holds more than K=2 sessions:\n%s", p)
	}
	j := judge.prompts[0]
	if !strings.Contains(j, "Correct Answer: Denver\n\nModel Response: You moved to Denver.\n\nIs the model response correct?") {
		t.Fatalf("judge prompt:\n%s", j)
	}
}

// The official reader sorts the retrieved sessions by date. Here the later
// session matches both query words and ranks first.
func TestRunQAGivesTheReaderSessionsInDateOrder(t *testing.T) {
	q := Question{
		ID: "q3", Type: "knowledge-update", Text: "Where do I live now? denver moved", Answer: json.RawMessage(`"Denver"`), Date: "2023/07/01",
		Dates: []string{"2023/06/01", "2023/01/01"}, SessionIDs: []string{"late", "early"}, AnswerSessionIDs: []string{"late"},
		Sessions: [][]Turn{
			{{Role: "user", Content: "I moved again, to Denver."}},
			{{Role: "user", Content: "I moved to Austin."}},
		},
	}
	emb := keywordEmbedder{words: []string{"denver", "moved"}}
	reader := &scripted{replies: []string{"Denver."}}
	r, err := RunQA(context.Background(), q, Setup{Granularity: Session, Embedder: emb}, t.TempDir(), QASetup{K: 2, Reader: reader, Judge: &scripted{replies: []string{"yes"}}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Read != 2 || r.Ranks[0] != 1 {
		t.Fatalf("result %+v, want 2 sessions read and the later session ranked first", r)
	}
	p := reader.prompts[0]
	if !strings.Contains(p, "### Session 1:\nSession Date: 2023/01/01\nSession Content:\n\nuser: I moved to Austin.") ||
		!strings.Contains(p, "### Session 2:\nSession Date: 2023/06/01\nSession Content:\n\nuser: I moved again, to Denver.") {
		t.Fatalf("sessions not in date order:\n%s", p)
	}
}

func TestRunQAAsksAbstentionQuestionsToo(t *testing.T) {
	qs, err := Load(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	judge := &scripted{replies: []string{"no"}}
	emb := keywordEmbedder{words: []string{"cat"}}
	r, err := RunQA(context.Background(), qs[1], Setup{Granularity: Session, Embedder: emb}, t.TempDir(), QASetup{K: 5, Reader: &scripted{replies: []string{"Whiskers."}}, Judge: judge})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Abstention || r.Correct || r.Read != 1 {
		t.Fatalf("result %+v, want an abstention judged wrong after reading 1 session", r)
	}
	if !strings.Contains(judge.prompts[0], "I will give you an unanswerable question, an explanation") ||
		!strings.Contains(judge.prompts[0], "Explanation: unknown") {
		t.Fatalf("abstention judge prompt:\n%s", judge.prompts[0])
	}
}

func TestJudgePromptFollowsTheQuestionType(t *testing.T) {
	for _, c := range []struct{ typ, want string }{
		{"multi-session", "If the response only contains a subset of the information required by the answer, answer no. \n\nQuestion:"},
		{"temporal-reasoning", "predicting 19 days when the answer is 18"},
		{"knowledge-update", "as long as the updated answer is the required answer"},
		{"single-session-preference", "Rubric: 3"},
	} {
		q := Question{ID: "x", Type: c.typ, Text: "q?", Answer: json.RawMessage(`3`)}
		p, err := JudgePrompt(q, "resp")
		if err != nil {
			t.Fatalf("%s: %v", c.typ, err)
		}
		if !strings.Contains(p, c.want) {
			t.Fatalf("%s prompt lacks %q:\n%s", c.typ, c.want, p)
		}
	}
	if _, err := JudgePrompt(Question{Type: "trivia"}, "resp"); err == nil {
		t.Fatal("an unknown question type must be an error, as in the official evaluator")
	}
}

func TestSummarizeQA(t *testing.T) {
	s := SummarizeQA([]QAResult{
		{Type: "multi-session", Correct: true},
		{Type: "multi-session"},
		{Type: "temporal-reasoning", Abstention: true, Correct: true},
	})
	if s.N != 3 || s.Accuracy != 2.0/3 || s.ByType["multi-session"].Accuracy != 0.5 || s.Abstention.N != 1 || s.Abstention.Accuracy != 1 {
		t.Fatalf("summary %+v", s)
	}
}
