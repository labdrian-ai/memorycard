package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// otherRepo makes a git repository whose origin is named other-repo, with one
// file, and returns that file's path.
func otherRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "git@example.com:me/other-repo.git"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	file := filepath.Join(dir, "internal", "store.go")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("package store\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

// transcript writes a subagent transcript whose tool calls read each path.
func transcript(t *testing.T, paths ...string) string {
	t.Helper()
	var b strings.Builder
	for _, p := range paths {
		b.WriteString(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"` + p + `"}}]}}` + "\n")
	}
	f := filepath.Join(t.TempDir(), "agent.jsonl")
	if err := os.WriteFile(f, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// here is a file of the repository the tests run in.
func here(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "capture.go")
}

func projectOf(t *testing.T, db, id string) string {
	t.Helper()
	_, out, _ := run(t, db, "", "get", id)
	for _, f := range strings.Fields(out) {
		if p, ok := strings.CutPrefix(f, "project="); ok {
			return p
		}
	}
	t.Fatalf("no project in %q", out)
	return ""
}

var unpinned = map[string]string{"MEMORYCARD_PROJECT": ""}

func TestCaptureFilesUnderTheRepositoryTheSubagentWorkedIn(t *testing.T) {
	db := newDB(t)
	other := otherRepo(t)
	tr := transcript(t, other, other, filepath.Dir(other), here(t))
	code, out, errOut := runEnv(t, db, unpinned, learnings, "capture", "--transcript", tr)
	if code != 0 || !strings.Contains(out, "captured: 3 saved") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	for _, id := range []string{"1", "2", "3"} {
		if p := projectOf(t, db, id); p != "other-repo" {
			t.Errorf("#%s project = %q, want other-repo", id, p)
		}
	}
}

func TestCaptureKeepsTheWorkingDirectoryProjectWhenTheSubagentWorkedThere(t *testing.T) {
	db := newDB(t)
	_, cwdProject, _ := runEnv(t, db, unpinned, "", "project")
	other := otherRepo(t)
	tr := transcript(t, here(t), here(t), other)
	runEnv(t, db, unpinned, learnings, "capture", "--transcript", tr)
	if p := projectOf(t, db, "1"); p != strings.TrimSpace(cwdProject) {
		t.Errorf("project = %q, want the working directory's %q", p, strings.TrimSpace(cwdProject))
	}
}

func TestCaptureKeepsAPinnedProjectOverTheTranscript(t *testing.T) {
	db := newDB(t)
	other := otherRepo(t)
	runEnv(t, db, nil, learnings, "capture", "--transcript", transcript(t, other, other))
	if p := projectOf(t, db, "1"); p != "p" {
		t.Errorf("project = %q, want the pinned p", p)
	}
}

func TestCaptureWithAMissingTranscriptStillSaves(t *testing.T) {
	db := newDB(t)
	_, cwdProject, _ := runEnv(t, db, unpinned, "", "project")
	code, out, _ := runEnv(t, db, unpinned, learnings, "capture", "--transcript", filepath.Join(t.TempDir(), "gone.jsonl"))
	if code != 0 || !strings.Contains(out, "captured: 3 saved") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if p := projectOf(t, db, "1"); p != strings.TrimSpace(cwdProject) {
		t.Errorf("project = %q, want %q", p, strings.TrimSpace(cwdProject))
	}
}
