package bench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// locomoFixture mirrors locomo10.json: sessions keyed session_N with their
// date_time, a date key with no session (as in conv-26), an image turn, a
// numeric answer and an adversarial question.
const locomoFixture = `[{"sample_id":"conv-1","conversation":{"speaker_a":"Ann","speaker_b":"Bo",
"session_1_date_time":"1:56 pm on 8 May, 2023",
"session_1":[{"speaker":"Ann","dia_id":"D1:1","text":"I adopted a dog."},
 {"speaker":"Bo","dia_id":"D1:2","text":"Look at mine!","img_url":["x"],"blip_caption":"a photo of a cat","query":"tabby cat sofa"}],
"session_10_date_time":"9:05 am on 2 July, 2023",
"session_10":[{"speaker":"Ann","dia_id":"D10:1","text":"We moved to Lyon."}],
"session_2_date_time":"7:55 pm on 9 June, 2023",
"session_2":[{"speaker":"Bo","dia_id":"D2:1","text":"I painted a sunrise in 2022."}],
"session_11_date_time":"10:00 am on 9 July, 2023"},
"qa":[{"question":"What pet did Ann adopt?","answer":"a dog","evidence":["D1:1"],"category":4},
 {"question":"When did Bo paint a sunrise?","answer":2022,"evidence":["D2:1"],"category":2},
 {"question":"Where did they move after the dog?","answer":"Lyon","evidence":["D1:1","D10:1"],"category":1},
 {"question":"What did Ann name the cat?","evidence":["D1:2"],"category":5,"adversarial_answer":"Tom"}]}]`

func loadFixture(t *testing.T) []Question {
	t.Helper()
	path := filepath.Join(t.TempDir(), "locomo.json")
	if err := os.WriteFile(path, []byte(locomoFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	qs, err := LoadLoCoMo(path)
	if err != nil {
		t.Fatal(err)
	}
	return qs
}

func TestLoadLoCoMoTurnsEachQuestionIntoAHaystackOfItsConversation(t *testing.T) {
	qs := loadFixture(t)
	if len(qs) != 4 {
		t.Fatalf("got %d questions, want 4", len(qs))
	}
	q := qs[0]
	if q.ID != "conv-1_q0" || q.Type != "locomo-single-hop" || q.Text != "What pet did Ann adopt?" || q.AnswerText() != "a dog" {
		t.Fatalf("question %+v", q)
	}
	if strings.Join(q.SessionIDs, ",") != "conv-1_S1,conv-1_S2,conv-1_S10" {
		t.Fatalf("sessions %v, want 1, 2, 10 in numeric order and no session for a lone date key", q.SessionIDs)
	}
	if strings.Join(q.Dates, ",") != "2023/05/08 (Mon) 13:56,2023/06/09 (Fri) 19:55,2023/07/02 (Sun) 09:05" {
		t.Fatalf("dates %v", q.Dates)
	}
	if q.Date != "2023/07/02 (Sun) 09:05" {
		t.Fatalf("question date %q, want the last session's", q.Date)
	}
	if strings.Join(q.AnswerSessionIDs, ",") != "conv-1_S1" {
		t.Fatalf("answer sessions %v", q.AnswerSessionIDs)
	}
	body := render(q.Sessions[0])
	if body != "Ann: I adopted a dog.\n\nBo: Look at mine! [image: a photo of a cat]" || strings.Contains(body, "tabby") {
		t.Fatalf("session 1 rendered as %q, want speakers, the caption and no image query", body)
	}
	if qs[1].Type != "locomo-temporal" || qs[1].AnswerText() != "2022" {
		t.Fatalf("numeric answer: %+v", qs[1])
	}
	if qs[2].Type != "locomo-multi-hop" || strings.Join(qs[2].AnswerSessionIDs, ",") != "conv-1_S1,conv-1_S10" {
		t.Fatalf("multi-hop: %+v", qs[2])
	}
	adv := qs[3]
	if adv.ID != "conv-1_q3_abs" || !adv.Abstention() || adv.Type != "locomo-adversarial" || adv.AnswerText() == "Tom" {
		t.Fatalf("adversarial question %+v, want an abstention without the adversarial answer as gold", adv)
	}
}

func TestLoCoMoQuestionsUseMem0sJudgeAndItsLabel(t *testing.T) {
	qs := loadFixture(t)
	p, err := JudgePrompt(qs[0], "Ann adopted a dog.")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"\nYour task is to label an answer to a question as ’CORRECT’ or ’WRONG’.",
		"as long as it touches on the same topic as the gold answer, it should be counted as CORRECT. \n",
		"Question: What pet did Ann adopt?\nGold answer: a dog\nGenerated answer: Ann adopted a dog.\n\nFirst, provide",
		"in a json format with the key as \"label\".\n",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("judge prompt lacks %q:\n%s", want, p)
		}
	}
	abs, _ := JudgePrompt(qs[3], "Not mentioned.")
	if !strings.Contains(abs, "I will give you an unanswerable question") {
		t.Fatalf("adversarial questions must use the abstention judge:\n%s", abs)
	}

	for verdict, want := range map[string]bool{
		`{"label": "CORRECT"}`:                               true,
		"It names the same pet.\n{\"label\":\"CORRECT\"}":    true,
		"The answer is not correct.\n{\"label\": \"WRONG\"}": false,
		"INCORRECT": false,
		"WRONG":     false,
		"CORRECT":   true,
		"yes":       false,
	} {
		if got := Correct(qs[0], verdict); got != want {
			t.Errorf("Correct(locomo, %q) = %v, want %v", verdict, got, want)
		}
	}
	if !Correct(qs[3], "Yes.") || Correct(qs[3], "No.") {
		t.Error("an adversarial question is judged by the abstention judge's yes")
	}
	if !Correct(Question{Type: "multi-session"}, "Yes.") || Correct(Question{Type: "multi-session"}, "CORRECT") {
		t.Error("LongMemEval verdicts still count only a yes")
	}

	emb := keywordEmbedder{words: []string{"pet", "dog", "adopt"}}
	judge := &scripted{replies: []string{`{"label": "CORRECT"}`}}
	r, err := RunQA(context.Background(), qs[0], Setup{Granularity: Session, Embedder: emb}, t.TempDir(),
		QASetup{K: 2, Reader: &scripted{replies: []string{"A dog."}}, Judge: judge})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Correct || r.Read != 2 || len(r.Ranks) != 1 || r.Ranks[0] != 1 {
		t.Fatalf("result %+v, want correct, 2 sessions read and the answer session ranked first", r)
	}
	if !strings.Contains(judge.prompts[0], "Gold answer: a dog\nGenerated answer: A dog.") {
		t.Fatalf("judge prompt:\n%s", judge.prompts[0])
	}
}

func TestLoadLoCoMoRejectsAnUnreadableDate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locomo.json")
	bad := strings.Replace(locomoFixture, "1:56 pm on 8 May, 2023", "sometime in May", 1)
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLoCoMo(path); err == nil || !strings.Contains(err.Error(), `conv-1 session_1: date "sometime in May"`) {
		t.Fatalf("err %v, want the session and its date named", err)
	}
}
