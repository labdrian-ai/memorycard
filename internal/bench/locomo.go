package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LoCoMo category codes as the data holds them (they differ from the order
// the paper lists them in).
var locomoTypes = map[int]string{
	1: "locomo-multi-hop",
	2: "locomo-temporal",
	3: "locomo-open-domain",
	4: "locomo-single-hop",
	5: "locomo-adversarial",
}

const locomoNoAnswer = "The conversation does not mention it."

type locomoSample struct {
	ID           string                     `json:"sample_id"`
	Conversation map[string]json.RawMessage `json:"conversation"`
	QA           []struct {
		Question string          `json:"question"`
		Answer   json.RawMessage `json:"answer"`
		Evidence []string        `json:"evidence"`
		Category int             `json:"category"`
	} `json:"qa"`
}

type locomoTurn struct {
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
	Caption string `json:"blip_caption"`
}

var (
	locomoSessionKey = regexp.MustCompile(`^session_(\d+)$`)
	locomoEvidence   = regexp.MustCompile(`D(\d+):`)
)

// LoadLoCoMo reads locomo10.json and turns every question into a Question
// whose haystack is its whole conversation, one session per LoCoMo session,
// so the LongMemEval harness runs it unchanged. Adversarial questions
// (category 5) become abstention questions, and the question date is the
// date of the conversation's last session.
func LoadLoCoMo(path string) ([]Question, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var samples []locomoSample
	if err := json.Unmarshal(b, &samples); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var qs []Question
	for _, s := range samples {
		var nums []int
		for key := range s.Conversation {
			if m := locomoSessionKey.FindStringSubmatch(key); m != nil {
				n, _ := strconv.Atoi(m[1])
				nums = append(nums, n)
			}
		}
		sort.Ints(nums)
		var ids, dates []string
		var sessions [][]Turn
		for _, n := range nums {
			key := fmt.Sprintf("session_%d", n)
			var raw string
			if err := json.Unmarshal(s.Conversation[key+"_date_time"], &raw); err != nil {
				return nil, fmt.Errorf("%s %s: no date: %w", s.ID, key, err)
			}
			t, err := time.Parse("3:04 pm on 2 January, 2006", raw)
			if err != nil {
				return nil, fmt.Errorf("%s %s: date %q: %w", s.ID, key, raw, err)
			}
			var turns []locomoTurn
			if err := json.Unmarshal(s.Conversation[key], &turns); err != nil {
				return nil, fmt.Errorf("%s %s: %w", s.ID, key, err)
			}
			session := make([]Turn, len(turns))
			for i, tr := range turns {
				text := tr.Text
				if tr.Caption != "" {
					text += " [image: " + tr.Caption + "]"
				}
				session[i] = Turn{Role: tr.Speaker, Content: text}
			}
			ids = append(ids, fmt.Sprintf("%s_S%d", s.ID, n))
			dates = append(dates, t.Format("2006/01/02 (Mon) 15:04"))
			sessions = append(sessions, session)
		}
		if len(sessions) == 0 {
			return nil, fmt.Errorf("%s: no sessions", s.ID)
		}
		for i, qa := range s.QA {
			typ, ok := locomoTypes[qa.Category]
			if !ok {
				return nil, fmt.Errorf("%s question %d: unknown category %d", s.ID, i, qa.Category)
			}
			q := Question{
				ID: fmt.Sprintf("%s_q%d", s.ID, i), Type: typ, Text: qa.Question, Answer: qa.Answer,
				Dates: dates, SessionIDs: ids, Sessions: sessions, Date: dates[len(dates)-1],
			}
			if qa.Category == 5 {
				q.ID += "_abs"
				q.Answer, _ = json.Marshal(locomoNoAnswer)
			}
			seen := map[string]bool{}
			for _, ev := range qa.Evidence {
				for _, m := range locomoEvidence.FindAllStringSubmatch(ev, -1) {
					sid := s.ID + "_S" + m[1]
					if !seen[sid] {
						seen[sid] = true
						q.AnswerSessionIDs = append(q.AnswerSessionIDs, sid)
					}
				}
			}
			qs = append(qs, q)
		}
	}
	return qs, nil
}

// IsLoCoMo reports whether q came from LoCoMo.
func (q Question) IsLoCoMo() bool { return strings.HasPrefix(q.Type, "locomo-") }
