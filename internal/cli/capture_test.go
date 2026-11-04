package cli

import (
	"strings"
	"testing"
)

const learnings = "Done.\n\n## Key Learnings\n\n1. The first learning is a standalone factual sentence.\n2. The second learning spans\n   two lines of text.\n3. The third learning is also long enough.\n"

func memoriesCount(t *testing.T, db string) string {
	t.Helper()
	_, out, _ := run(t, db, "", "stats")
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "memories: ") {
			return strings.TrimPrefix(l, "memories: ")
		}
	}
	t.Fatalf("no memories line in %q", out)
	return ""
}

func TestCaptureSavesOneMemoryPerItem(t *testing.T) {
	db := newDB(t)
	code, _, errOut := run(t, db, learnings, "capture")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if n := memoriesCount(t, db); n != "3" {
		t.Fatalf("memories = %s, want 3", n)
	}
	_, out, _ := run(t, db, "", "get", "2")
	for _, want := range []string{"learning", "second learning spans two lines"} {
		if !strings.Contains(out, want) {
			t.Errorf("get 2 missing %q: %q", want, out)
		}
	}
}

func TestCaptureSameInputTwiceAddsNothing(t *testing.T) {
	db := newDB(t)
	run(t, db, learnings, "capture")
	if code, _, errOut := run(t, db, learnings, "capture"); code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if n := memoriesCount(t, db); n != "3" {
		t.Errorf("memories = %s, want 3", n)
	}
}

func TestCaptureWithoutBlockOrItemsSavesNothing(t *testing.T) {
	db := newDB(t)
	for _, in := range []string{"", "just text\n1. not in a block\n", "## Key Learnings\n\nnone today\n"} {
		code, _, errOut := run(t, db, in, "capture")
		if code != 0 || errOut != "" {
			t.Errorf("input %q: code=%d err=%q", in, code, errOut)
		}
	}
	if n := memoriesCount(t, db); n != "0" {
		t.Errorf("memories = %s, want 0", n)
	}
}

func TestCaptureStopsAtNextHeading(t *testing.T) {
	db := newDB(t)
	run(t, db, "## Key Learnings\n1. Only this one counts as a learning.\n\n## Next\n1. Not a learning at all.\n", "capture")
	if n := memoriesCount(t, db); n != "1" {
		t.Errorf("memories = %s, want 1", n)
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
