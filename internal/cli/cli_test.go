package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labdrian-ai/memorycard/internal/mcpserver"
)

// run executes the CLI against a temp database with the embedder off.
func run(t *testing.T, db string, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	return runEnv(t, db, nil, stdin, args...)
}

// runEnv is run with extra or overriding environment variables.
func runEnv(t *testing.T, db string, extra map[string]string, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	env := map[string]string{"MEMORYCARD_DB": db, "MEMORYCARD_EMBED": "off", "MEMORYCARD_PROJECT": "p"}
	for k, v := range extra {
		env[k] = v
	}
	var out, errOut bytes.Buffer
	code = Run(args, strings.NewReader(stdin), &out, &errOut, func(k string) string { return env[k] })
	return code, out.String(), errOut.String()
}

func newDB(t *testing.T) string { return filepath.Join(t.TempDir(), "sub", "memory.db") }

func TestSaveThenSearchAndGet(t *testing.T) {
	db := newDB(t)
	code, out, errOut := run(t, db, "", "save", "--title", "Rollout plan", "--body", "canary first", "--type", "decision")
	if code != 0 || !strings.Contains(out, "created #1") {
		t.Fatalf("save: code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, _ = run(t, db, "", "search", "canary")
	if code != 0 || !strings.Contains(out, "#1 decision: Rollout plan") {
		t.Errorf("search: code=%d out=%q", code, out)
	}
	code, out, _ = run(t, db, "", "get", "1")
	if code != 0 || !strings.Contains(out, "canary first") {
		t.Errorf("get: code=%d out=%q", code, out)
	}
}

func TestSaveReadsBodyFromStdin(t *testing.T) {
	db := newDB(t)
	code, out, errOut := run(t, db, "body from a pipe\n", "save", "--title", "Piped", "--body", "-")
	if code != 0 {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if _, out, _ := run(t, db, "", "get", "1"); !strings.Contains(out, "body from a pipe") {
		t.Errorf("get = %q", out)
	}
}

func TestFailuresExitNonZeroWithMessage(t *testing.T) {
	db := newDB(t)
	code, _, errOut := run(t, db, "", "save", "--title", "no body")
	if code != 1 || !strings.Contains(errOut, "title and body") {
		t.Errorf("save without body: code=%d err=%q", code, errOut)
	}
	code, _, errOut = run(t, db, "", "get", "42")
	if code != 1 || !strings.Contains(errOut, "not found") {
		t.Errorf("get unknown: code=%d err=%q", code, errOut)
	}
	code, _, errOut = run(t, db, "", "get", "abc")
	if code != 2 || !strings.Contains(errOut, "id") {
		t.Errorf("get bad id: code=%d err=%q", code, errOut)
	}
}

func TestUsageForNoCommandAndUnknownCommand(t *testing.T) {
	db := newDB(t)
	for _, args := range [][]string{nil, {"bogus"}} {
		code, _, errOut := run(t, db, "", args...)
		if code != 2 || !strings.Contains(errOut, "usage:") {
			t.Errorf("args %v: code=%d err=%q, want exit 2 with usage", args, code, errOut)
		}
	}
	if code, out, _ := run(t, db, "", "help"); code != 0 || !strings.Contains(out, "mcp") || !strings.Contains(out, "stats") {
		t.Errorf("help: code=%d out=%q, want a command list", code, out)
	}
}

func TestVersionPrintsTheBuildVersionWithoutOpeningTheDatabase(t *testing.T) {
	db := newDB(t)
	for _, arg := range []string{"version", "--version"} {
		code, out, errOut := run(t, db, "", arg)
		if code != 0 || out != "memorycard "+mcpserver.Version+"\n" || errOut != "" {
			t.Errorf("%s: code=%d out=%q err=%q", arg, code, out, errOut)
		}
	}
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		t.Errorf("version created the database: %v", err)
	}
}

func TestStatsCountsByStatusAndPending(t *testing.T) {
	db := newDB(t)
	run(t, db, "", "save", "--title", "one", "--body", "first")
	run(t, db, "", "save", "--title", "two", "--body", "second", "--supersedes", "1")
	code, out, errOut := run(t, db, "", "stats")
	if code != 0 {
		t.Fatalf("stats: code=%d err=%q", code, errOut)
	}
	for _, want := range []string{"memories: 2", "active: 1", "superseded: 1", "without embedding: 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("stats missing %q in %q", want, out)
		}
	}
}

func TestEmbedNeedsAnEmbedder(t *testing.T) {
	code, _, errOut := run(t, newDB(t), "", "embed")
	if code != 1 || !strings.Contains(errOut, "embedder is off") {
		t.Errorf("code=%d err=%q", code, errOut)
	}
}

func TestContextCommand(t *testing.T) {
	db := newDB(t)
	run(t, db, "", "save", "--title", "A note", "--body", "text")
	if code, out, _ := run(t, db, "", "context"); code != 0 || !strings.Contains(out, "#1 [recent]") {
		t.Errorf("context: code=%d out=%q", code, out)
	}
}
