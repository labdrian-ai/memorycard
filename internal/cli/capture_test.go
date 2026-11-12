package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const learnings = "Done.\n\n## Key Learnings\n\n" +
	"1. The first learning is a standalone factual sentence about the store.\n" +
	"2. The second learning spans\n   two lines of text and is long enough to keep.\n" +
	"3. The third learning is also long enough to be saved as a memory.\n"

func memoriesCount(t *testing.T, db string) string {
	t.Helper()
	return statsLine(t, db, nil, "memories: ")
}

func statsLine(t *testing.T, db string, env map[string]string, prefix string) string {
	t.Helper()
	_, out, _ := runEnv(t, db, env, "", "stats")
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, prefix) {
			return strings.TrimPrefix(l, prefix)
		}
	}
	t.Fatalf("no %q line in %q", prefix, out)
	return ""
}

// fakeOllama answers /api/embed with vec(text) for every input and records
// each request's inputs.
type fakeOllama struct {
	mu    sync.Mutex
	calls [][]string
	fail  bool
	vec   func(string) []float32
}

func (f *fakeOllama) start(t *testing.T) map[string]string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []string }
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.calls = append(f.calls, req.Input)
		fail := f.fail
		f.mu.Unlock()
		if fail {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		vecs := make([][]float32, len(req.Input))
		for i, in := range req.Input {
			vecs[i] = f.vec(in)
		}
		json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
	}))
	t.Cleanup(srv.Close)
	return map[string]string{"MEMORYCARD_EMBED": "on", "MEMORYCARD_OLLAMA_URL": srv.URL}
}

func TestCaptureSavesOneMemoryPerItem(t *testing.T) {
	db := newDB(t)
	code, out, errOut := run(t, db, learnings, "capture")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if n := memoriesCount(t, db); n != "3" {
		t.Fatalf("memories = %s, want 3", n)
	}
	want := "created #1\ncreated #2\ncreated #3\ncaptured: 3 saved, 0 skipped\n"
	if out != want {
		t.Errorf("out = %q, want %q", out, want)
	}
	_, out, _ = run(t, db, "", "get", "2")
	for _, want := range []string{"learning", "second learning spans two lines"} {
		if !strings.Contains(out, want) {
			t.Errorf("get 2 missing %q: %q", want, out)
		}
	}
}

// topicOf returns the topic= field of `get id`'s header line, or "" when it has none.
func topicOf(t *testing.T, db, id string) string {
	t.Helper()
	_, out, _ := run(t, db, "", "get", id)
	header, _, _ := strings.Cut(out, "\n")
	for _, f := range strings.Fields(header) {
		if strings.HasPrefix(f, "topic=") {
			return strings.TrimPrefix(f, "topic=")
		}
	}
	return ""
}

// Every captured learning gets its own curated topic under learning/, derived from its content, so that
// promotion to the vault (which takes only pinned or curated-topic memories) can reach it, and no two
// learnings share a key (a shared key would overwrite one learning with the next).
func TestCaptureGivesEachLearningItsOwnLearningTopic(t *testing.T) {
	db := newDB(t)
	run(t, db, learnings, "capture")
	shared := "## Key Learnings\n\n" +
		"1. A learning that shares this long opening with the next one ends with the word alpha.\n" +
		"2. A learning that shares this long opening with the next one ends with the word beta.\n"
	run(t, db, shared, "capture")
	if n := memoriesCount(t, db); n != "5" {
		t.Fatalf("memories = %s, want 5", n)
	}
	seen := map[string]bool{}
	for _, id := range []string{"1", "2", "3", "4", "5"} {
		k := topicOf(t, db, id)
		if !strings.HasPrefix(k, "learning/") || len(k) <= len("learning/") {
			t.Fatalf("memory %s topic = %q, want learning/<key>", id, k)
		}
		if seen[k] {
			t.Fatalf("memory %s reuses topic %q", id, k)
		}
		seen[k] = true
	}
}

// The same learning captured again, reworded only in spacing or case, adds no memory and leaves the first
// one's topic as it was.
func TestCaptureLearningTopicIgnoresCaseAndSpacing(t *testing.T) {
	db := newDB(t)
	run(t, db, learnings, "capture")
	k := topicOf(t, db, "1")
	again := "## Key Learnings\n\n1. The FIRST learning   is a standalone factual sentence about the store.\n"
	code, out, errOut := run(t, db, again, "capture")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if n := memoriesCount(t, db); n != "3" {
		t.Errorf("memories = %s, want 3 (out %q)", n, out)
	}
	if got := topicOf(t, db, "1"); got != k {
		t.Errorf("topic after re-capture = %q, want %q", got, k)
	}
}

// A learning whose text an untopiced memory already holds (one captured before learnings had topics) stays a
// duplicate: capture adds no second copy.
func TestCaptureLearningAlreadySavedWithoutTopicIsADuplicate(t *testing.T) {
	db := newDB(t)
	item := "The first learning is a standalone factual sentence about the store."
	run(t, db, item, "save", "--type", "learning", "--title", "The first learning is a standalone factual sentence about the store", "--body", "-")
	code, out, errOut := run(t, db, "## Key Learnings\n\n1. "+item+"\n", "capture")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if n := memoriesCount(t, db); n != "1" {
		t.Errorf("memories = %s, want 1 (out %q)", n, out)
	}
}

// A learning edited after it was captured keeps the edit when its original text is captured again: capture
// never overwrites the memory under a learning's topic.
func TestCaptureNeverOverwritesAnEditedLearning(t *testing.T) {
	db := newDB(t)
	run(t, db, learnings, "capture")
	k := topicOf(t, db, "1")
	edited := "The first learning, corrected by hand: it names the store and the reason it holds."
	if code, _, errOut := run(t, db, edited, "save", "--type", "learning", "--title", "First learning, corrected", "--topic", k, "--body", "-"); code != 0 {
		t.Fatalf("edit: code=%d err=%q", code, errOut)
	}
	code, out, errOut := run(t, db, learnings, "capture")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if n := memoriesCount(t, db); n != "3" {
		t.Errorf("memories = %s, want 3 (out %q)", n, out)
	}
	if !strings.HasSuffix(out, "captured: 0 saved, 3 skipped (3 duplicate)\n") {
		t.Errorf("out = %q", out)
	}
	if _, got, _ := run(t, db, "", "get", "1"); !strings.Contains(got, "corrected by hand") {
		t.Errorf("get 1 lost the edit: %q", got)
	}
}

func TestCaptureSameInputTwiceAddsNothing(t *testing.T) {
	db := newDB(t)
	run(t, db, learnings, "capture")
	code, out, errOut := run(t, db, learnings, "capture")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if n := memoriesCount(t, db); n != "3" {
		t.Errorf("memories = %s, want 3", n)
	}
	if !strings.HasSuffix(out, "captured: 0 saved, 3 skipped (3 duplicate)\n") {
		t.Errorf("out = %q", out)
	}
}

func TestCaptureWithoutBlockOrItemsSavesNothing(t *testing.T) {
	db := newDB(t)
	for _, in := range []string{"", "just text\n1. not in a block\n", "## Key Learnings\n\nnone today\n"} {
		code, out, errOut := run(t, db, in, "capture")
		if code != 0 || errOut != "" || out != "" {
			t.Errorf("input %q: code=%d out=%q err=%q", in, code, out, errOut)
		}
	}
	if n := memoriesCount(t, db); n != "0" {
		t.Errorf("memories = %s, want 0", n)
	}
}

func TestCaptureStopsAtNextHeading(t *testing.T) {
	db := newDB(t)
	run(t, db, "## Key Learnings\n1. Only this one counts as a learning, and it is long enough to keep.\n\n## Next\n1. Not a learning at all, even though it is long enough.\n", "capture")
	if n := memoriesCount(t, db); n != "1" {
		t.Errorf("memories = %s, want 1", n)
	}
}

func TestCaptureDropsShortItemsAndProbeMarkers(t *testing.T) {
	db := newDB(t)
	in := "## Key Learnings\n" +
		"1. Too short to carry any context.\n" +
		"2. The hook wrote codexprobe1730 into the transcript to check that capture runs.\n" +
		"3. A marker like PROBE-42 in a long enough sentence is also a test artefact.\n" +
		"4. This one is a real learning with enough context to stand on its own later.\n"
	code, out, errOut := run(t, db, in, "capture")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	want := "skipped (too short): Too short to carry any context\n" +
		"skipped (probe marker): The hook wrote codexprobe1730 into the transcript to check that capture runs\n" +
		"skipped (probe marker): A marker like PROBE-42 in a long enough sentence is also a test artefact\n" +
		"created #1\n" +
		"captured: 1 saved, 3 skipped (1 too short, 2 probe marker)\n"
	if out != want {
		t.Errorf("out = %q\nwant %q", out, want)
	}
	if n := memoriesCount(t, db); n != "1" {
		t.Errorf("memories = %s, want 1", n)
	}
	_, got, _ := run(t, db, "", "get", "1")
	if !strings.Contains(got, "real learning with enough context") {
		t.Errorf("get 1 = %q", got)
	}
}

func TestCaptureTitleIsTheFirstSentence(t *testing.T) {
	db := newDB(t)
	long := "This first sentence keeps going well past the old eighty rune cut because it explains a whole idea at length. Then more."
	in := "## Key Learnings\n" +
		"1. Capture titles come from the first sentence. The rest stays only in the body of the memory.\n" +
		"2. " + long + "\n" +
		"3. A single sentence without a final stop that is long enough to be kept as is\n" +
		"4. . A leading stop leaves the first sentence empty, so the whole item is the title\n"
	if code, _, errOut := run(t, db, in, "capture"); code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	for id, title := range map[string]string{
		"1": "Capture titles come from the first sentence",
		"2": "This first sentence keeps going well past the old eighty rune cut because it explains a whole idea...",
		"3": "A single sentence without a final stop that is long enough to be kept as is",
		"4": ". A leading stop leaves the first sentence empty, so the whole item is the title",
	} {
		_, out, _ := run(t, db, "", "get", id)
		lines := strings.Split(out, "\n")
		if len(lines) < 4 || lines[3] != title {
			t.Errorf("get %s title line = %q, want %q (out %q)", id, lines[min(3, len(lines)-1)], title, out)
		}
	}
	_, out, _ := run(t, db, "", "get", "1")
	if !strings.Contains(out, "The rest stays only in the body") {
		t.Errorf("body lost: %q", out)
	}
}

func TestCaptureEmbedsAllItemsInOneBatch(t *testing.T) {
	db := newDB(t)
	f := &fakeOllama{vec: func(string) []float32 { return []float32{1, 0, 0} }}
	env := f.start(t)
	code, out, errOut := runEnv(t, db, env, learnings, "capture")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if len(f.calls) != 1 || len(f.calls[0]) != 3 {
		t.Errorf("embed requests = %d (%v), want one request with 3 inputs", len(f.calls), f.calls)
	}
	if !strings.HasSuffix(out, "captured: 3 saved, 0 skipped\n") {
		t.Errorf("out = %q", out)
	}
	if n := statsLine(t, db, env, "without embedding: "); n != "0" {
		t.Errorf("without embedding = %s, want 0", n)
	}
}

// Regression: batch embedding must keep the "possible duplicate" hint that
// capture printed when each item was embedded on save.
func TestCaptureKeepsDuplicateHint(t *testing.T) {
	db := newDB(t)
	f := &fakeOllama{vec: func(s string) []float32 {
		if strings.Contains(s, "batches every learning") {
			return []float32{1, 0, 0}
		}
		return []float32{0, 0, 1}
	}}
	env := f.start(t)
	first := "## Key Learnings\n1. The memorycard capture hook batches every learning of one block into a single ollama embed request, which halves the wall time.\n"
	code, out, errOut := runEnv(t, db, env, first, "capture")
	if code != 0 || errOut != "" || out != "created #1\ncaptured: 1 saved, 0 skipped\n" {
		t.Fatalf("first capture: code=%d out=%q err=%q", code, out, errOut)
	}
	second := "## Key Learnings\n" +
		"1. The memorycard capture hook batches every learning of one block into a single ollama embed request, which halves its wall time.\n" +
		"2. An unrelated learning that is long enough to be kept and points elsewhere.\n"
	code, out, errOut = runEnv(t, db, env, second, "capture")
	want := "created #2\n" +
		"possible duplicate of #1 (1.00): The memorycard capture hook batches every learning of one block into a single ollama embed request...\n" +
		"created #3\n" +
		"captured: 2 saved, 0 skipped\n"
	if code != 0 || errOut != "" || out != want {
		t.Errorf("second capture: code=%d err=%q\nout  %q\nwant %q", code, errOut, out, want)
	}
	if len(f.calls) != 2 {
		t.Errorf("embed requests = %d, want one per capture", len(f.calls))
	}
}

func TestCaptureKeepsMemoriesPendingWhenEmbeddingFails(t *testing.T) {
	db := newDB(t)
	f := &fakeOllama{fail: true, vec: func(string) []float32 { return []float32{1, 0, 0} }}
	env := f.start(t)
	code, out, errOut := runEnv(t, db, env, learnings, "capture")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if !strings.Contains(out, "warning: embedding pending:") || !strings.HasSuffix(out, "captured: 3 saved, 0 skipped\n") {
		t.Errorf("out = %q", out)
	}
	if n := statsLine(t, db, env, "without embedding: "); n != "3" {
		t.Errorf("without embedding = %s, want 3", n)
	}
}

func TestSaveNamesSimilarMemories(t *testing.T) {
	db := newDB(t)
	f := &fakeOllama{vec: func(s string) []float32 {
		switch {
		case strings.Contains(s, "alpha"):
			return []float32{1, 0, 0}
		case strings.Contains(s, "beta"):
			return []float32{0.8, 0.6, 0}
		}
		return []float32{0, 0, 1}
	}}
	env := f.start(t)
	runEnv(t, db, env, "", "save", "--type", "decision", "--title", "alpha plan", "--body", "dense search")
	runEnv(t, db, env, "", "save", "--type", "discovery", "--title", "alpha other type", "--body", "x")
	code, out, errOut := runEnv(t, db, env, "", "save", "--type", "decision", "--title", "beta plan", "--body", "hybrid search")
	want := "created #3\nsimilar active memories (if one is outdated, save again with supersedes=<id>):\n  #1 (0.80): alpha plan\n"
	if code != 0 || out != want {
		t.Errorf("code=%d out=%q err=%q, want %q", code, out, errOut, want)
	}
}

func TestProjectPrintsResolvedProject(t *testing.T) {
	db := newDB(t)
	code, out, _ := run(t, db, "", "project")
	if code != 0 || out != "p\n" {
		t.Errorf("code=%d out=%q", code, out)
	}
	_, out, _ = runEnv(t, db, map[string]string{"MEMORYCARD_PROJECT": "other"}, "", "project")
	if out != "other\n" {
		t.Errorf("out=%q", out)
	}
}

func TestSaveAndContextUnchangedByCaptureCommands(t *testing.T) {
	db := newDB(t)
	code, out, _ := run(t, db, "", "save", "--title", "T", "--body", "B")
	if code != 0 || out != "created #1\n" {
		t.Errorf("save: code=%d out=%q", code, out)
	}
	code, out, _ = run(t, db, "", "context")
	if code != 0 || !strings.Contains(out, "T") {
		t.Errorf("context: code=%d out=%q", code, out)
	}
}
