package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func seedList(t *testing.T, db string) {
	t.Helper()
	for _, m := range [][3]string{
		{"Cand A", "body a", "procedural/candidates/a"},
		{"Draft B", "body \"b\"\nline two", "procedural/drafts/b"},
		{"Other", "elsewhere", "notes/c"},
	} {
		if code, out, errOut := run(t, db, "", "save", "--title", m[0], "--body", m[1], "--topic", m[2]); code != 0 {
			t.Fatalf("save: code=%d out=%q err=%q", code, out, errOut)
		}
	}
}

type listJSON struct {
	Project  string `json:"project"`
	Memories []struct {
		ID       int64  `json:"id"`
		TopicKey string `json:"topic_key"`
		Scope    string `json:"scope"`
		Title    string `json:"title"`
		Body     string `json:"body"`
	} `json:"memories"`
}

func TestListJSONShape(t *testing.T) {
	db := newDB(t)
	seedList(t, db)
	code, out, errOut := run(t, db, "", "list", "--json",
		"--topic-prefix", "procedural/candidates/", "--topic-prefix", "procedural/drafts/")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	var got listJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not json: %v\n%s", err, out)
	}
	if got.Project != "p" || len(got.Memories) != 2 {
		t.Fatalf("got %+v", got)
	}
	a, b := got.Memories[0], got.Memories[1]
	if a.ID != 1 || a.TopicKey != "procedural/candidates/a" || a.Title != "Cand A" || a.Body != "body a" || a.Scope != "project" {
		t.Errorf("first = %+v", a)
	}
	if b.ID != 2 || b.TopicKey != "procedural/drafts/b" || b.Body != "body \"b\"\nline two" {
		t.Errorf("second = %+v", b)
	}
	// Exactly the five documented keys per memory, nothing else.
	var raw struct {
		Memories []map[string]json.RawMessage `json:"memories"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Memories[0]) != 5 {
		t.Errorf("memory keys = %v, want id, topic_key, scope, title, body", raw.Memories[0])
	}
	// The same key in two scopes comes back twice, told apart by scope.
	if code, _, errOut := run(t, db, "", "save", "--title", "Mine", "--body", "m", "--topic", "procedural/candidates/a", "--scope", "personal"); code != 0 {
		t.Fatalf("save personal: %q", errOut)
	}
	_, out, _ = run(t, db, "", "list", "--json", "--topic-prefix", "procedural/candidates/")
	var two listJSON
	if err := json.Unmarshal([]byte(out), &two); err != nil || len(two.Memories) != 2 ||
		two.Memories[0].Scope != "project" || two.Memories[1].Scope != "personal" {
		t.Errorf("same key in two scopes: err=%v %+v", err, two.Memories)
	}
}

func TestListJSONEmptyResultIsAnArray(t *testing.T) {
	db := newDB(t)
	seedList(t, db)
	code, out, errOut := run(t, db, "", "list", "--json", "--topic-prefix", "nothing/")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if !strings.Contains(out, `"memories":[]`) && !strings.Contains(out, `"memories": []`) {
		t.Errorf("out = %q, want memories as an empty array", out)
	}
	if code, out, _ := run(t, db, "", "list", "--topic-prefix", "nothing/"); code != 0 || out != "no results\n" {
		t.Errorf("text empty: code=%d out=%q", code, out)
	}
}

func TestListTextAndProjectFlag(t *testing.T) {
	db := newDB(t)
	seedList(t, db)
	code, out, _ := run(t, db, "", "list", "--topic-prefix", "procedural/")
	if code != 0 || out != "#1 procedural/candidates/a: Cand A\n#2 procedural/drafts/b: Draft B\n" {
		t.Errorf("text: code=%d out=%q", code, out)
	}
	if code, out, _ := run(t, db, "", "list", "--project", "zzz", "--topic-prefix", "procedural/"); code != 0 || out != "no results\n" {
		t.Errorf("other project: code=%d out=%q", code, out)
	}
}

func TestListBadUsageExitsTwo(t *testing.T) {
	db := newDB(t)
	for _, args := range [][]string{
		{"list"},
		{"list", "--topic-prefix", ""},
		{"list", "--topic-prefix", "a/", "--project", ""},
		{"list", "--topic-prefix", "a/", "extra"},
		{"list", "extra", "--topic-prefix", "a/"},
		{"list", "--nope"},
	} {
		code, out, errOut := run(t, db, "", args...)
		if code != 2 || out != "" || errOut == "" {
			t.Errorf("%v: code=%d out=%q err=%q, want exit 2", args, code, out, errOut)
		}
	}
	if _, _, errOut := run(t, db, "", "list"); !strings.Contains(errOut, "usage: memorycard list") {
		t.Errorf("err = %q, want the list usage", errOut)
	}
	if _, out, _ := run(t, db, "", "help"); !strings.Contains(out, "list ") || !strings.Contains(out, "--topic-prefix") {
		t.Errorf("help does not name list: %q", out)
	}
}
