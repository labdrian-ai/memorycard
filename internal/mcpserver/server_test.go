package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/labdrian-ai/memorycard/internal/memory"
	"github.com/labdrian-ai/memorycard/internal/store"
)

// connect starts a server over an in-memory transport and returns a client session.
func connect(t *testing.T, project string) *mcp.ClientSession {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := New(memory.New(db), project).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// call invokes a tool and returns its text and whether it reported an error.
func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func TestExposesExactlySixToolsWithinTokenBudget(t *testing.T) {
	cs := connect(t, "p")
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	want := "mem_context mem_get mem_save mem_search mem_session mem_update"
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("tools = %q, want %q", got, want)
	}
	raw, err := json.Marshal(res.Tools)
	if err != nil {
		t.Fatal(err)
	}
	tokens := float64(len(raw)) / 3.6
	t.Logf("tool definitions: %d bytes, about %.0f tokens", len(raw), tokens)
	if tokens > 1500 {
		t.Errorf("definitions are about %.0f tokens, budget is 1500", tokens)
	}
}

func TestSaveThenSearchRoundTrip(t *testing.T) {
	cs := connect(t, "p")
	out, isErr := call(t, cs, "mem_save", map[string]any{"title": "Rollout plan", "body": "canary first, then region by region", "type": "decision"})
	if isErr || !strings.Contains(out, "created #1") {
		t.Fatalf("save = %q (error=%v)", out, isErr)
	}
	out, isErr = call(t, cs, "mem_search", map[string]any{"query": "canary"})
	if isErr {
		t.Fatalf("search error: %q", out)
	}
	if !strings.Contains(out, "#1 decision: Rollout plan") {
		t.Errorf("search = %q, want the saved memory listed", out)
	}
	if !strings.Contains(out, "mode: lexical") {
		t.Errorf("search = %q, want it to say it ran lexical (no embedder)", out)
	}
	out, _ = call(t, cs, "mem_search", map[string]any{"query": "canary", "detail": "snippet"})
	if !strings.Contains(out, "canary first") {
		t.Errorf("snippet detail = %q, want the body snippet", out)
	}
	out, _ = call(t, cs, "mem_search", map[string]any{"query": "zzzzqq"})
	if !strings.Contains(out, "no results") {
		t.Errorf("empty search = %q, want an explicit no-results line", out)
	}
}

func TestToolFailuresAreReportedAsToolErrors(t *testing.T) {
	cs := connect(t, "p")
	out, isErr := call(t, cs, "mem_save", map[string]any{"title": "only a title", "body": "  "})
	if !isErr || !strings.Contains(out, "title and body") {
		t.Errorf("blank body: %q (error=%v)", out, isErr)
	}
	out, isErr = call(t, cs, "mem_save", map[string]any{"title": "only a title"})
	if !isErr || !strings.Contains(out, "body") {
		t.Errorf("missing body: %q (error=%v), want the schema to name the field", out, isErr)
	}
	out, isErr = call(t, cs, "mem_get", map[string]any{"id": 99})
	if !isErr || !strings.Contains(out, "not found") {
		t.Errorf("unknown id: %q (error=%v)", out, isErr)
	}
	out, isErr = call(t, cs, "mem_update", map[string]any{"id": 99, "pinned": true})
	if !isErr || !strings.Contains(out, "not found") {
		t.Errorf("update unknown id: %q (error=%v)", out, isErr)
	}
}

func TestProjectDefaultAndExplicit(t *testing.T) {
	cs := connect(t, "")
	if out, isErr := call(t, cs, "mem_save", map[string]any{"title": "t", "body": "b"}); !isErr || !strings.Contains(out, "project is required") {
		t.Errorf("no project anywhere: %q (error=%v)", out, isErr)
	}
	if out, isErr := call(t, cs, "mem_save", map[string]any{"title": "t", "body": "b", "project": "x"}); isErr {
		t.Errorf("explicit project rejected: %q", out)
	}
	cs2 := connect(t, "dflt")
	call(t, cs2, "mem_save", map[string]any{"title": "needle", "body": "haystack"})
	if out, _ := call(t, cs2, "mem_search", map[string]any{"query": "needle", "project": "other"}); !strings.Contains(out, "no results") {
		t.Errorf("other project leaked: %q", out)
	}
}

// workspace returns a directory whose .memorycard file names project.
func workspace(t *testing.T, project string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".memorycard"), []byte(project+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCwdResolvesTheProjectOnEachCall(t *testing.T) {
	cs := connect(t, "dflt")
	a, b := workspace(t, "alpha"), workspace(t, "beta")
	for _, arg := range []string{"cwd", "directory"} {
		dir := map[string]string{"cwd": a, "directory": b}[arg]
		if out, isErr := call(t, cs, "mem_save", map[string]any{"title": "from " + arg, "body": "b " + arg, arg: dir}); isErr {
			t.Fatalf("save with %s: %q", arg, out)
		}
	}
	for project, want := range map[string]string{"alpha": "from cwd", "beta": "from directory"} {
		dir := map[string]string{"alpha": a, "beta": b}[project]
		if out, _ := call(t, cs, "mem_context", map[string]any{"cwd": dir}); !strings.Contains(out, want) {
			t.Errorf("context of %s by cwd = %q, want %q", project, out, want)
		}
		if out, _ := call(t, cs, "mem_context", map[string]any{"directory": dir}); !strings.Contains(out, want) {
			t.Errorf("context of %s by directory = %q, want %q", project, out, want)
		}
	}
	// The server default and an explicit project are unchanged.
	if out, _ := call(t, cs, "mem_context", map[string]any{}); !strings.Contains(out, "no memories yet") {
		t.Errorf("default project got cwd saves: %q", out)
	}
	call(t, cs, "mem_save", map[string]any{"title": "named", "body": "explicit wins", "project": "gamma", "cwd": a})
	if out, _ := call(t, cs, "mem_context", map[string]any{"project": "gamma"}); !strings.Contains(out, "named") {
		t.Errorf("explicit project lost to cwd: %q", out)
	}
	if out, _ := call(t, cs, "mem_context", map[string]any{"cwd": a}); strings.Contains(out, "named") {
		t.Errorf("explicit-project save landed in the cwd project: %q", out)
	}
}

func TestCwdMustBeAnExistingAbsoluteDirectory(t *testing.T) {
	cs := connect(t, "dflt")
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	a, b := workspace(t, "alpha"), workspace(t, "beta")
	for name, args := range map[string]map[string]any{
		"relative":  {"cwd": "some/dir"},
		"missing":   {"cwd": filepath.Join(t.TempDir(), "nope")},
		"file":      {"directory": file},
		"disagreed": {"cwd": a, "directory": b},
	} {
		for _, tool := range []string{"mem_save", "mem_context"} {
			in := map[string]any{}
			if tool == "mem_save" {
				in["title"], in["body"] = "t", "b"
			}
			for k, v := range args {
				in[k] = v
			}
			if out, isErr := call(t, cs, tool, in); !isErr || !strings.Contains(out, "cwd") {
				t.Errorf("%s %s: %q (error=%v), want an error naming cwd", tool, name, out, isErr)
			}
		}
	}
}

func TestGetWithHistoryShowsBodyAndHistory(t *testing.T) {
	cs := connect(t, "p")
	call(t, cs, "mem_save", map[string]any{"title": "Auth", "body": "first version", "topic_key": "arch/auth"})
	call(t, cs, "mem_save", map[string]any{"title": "Auth", "body": "second version", "topic_key": "arch/auth"})
	out, isErr := call(t, cs, "mem_get", map[string]any{"id": 1, "history": true})
	if isErr || !strings.Contains(out, "second version") || !strings.Contains(out, "--- history v1") ||
		!strings.Contains(out, "first version") || !strings.Contains(out, "arch/auth") {
		t.Errorf("get = %q (error=%v), want current body, history and topic key", out, isErr)
	}
	if strings.Contains(out, "earlier version") {
		t.Errorf("get with history = %q, want no hint line", out)
	}
}

func TestGetShowsOnlyTheCurrentVersionByDefault(t *testing.T) {
	cs := connect(t, "p")
	for _, body := range []string{"first version", "second version", "third version"} {
		call(t, cs, "mem_save", map[string]any{"title": "Auth", "body": body, "topic_key": "arch/auth"})
	}
	out, isErr := call(t, cs, "mem_get", map[string]any{"id": 1})
	if isErr || !strings.Contains(out, "third version") || !strings.Contains(out, "arch/auth") {
		t.Fatalf("get = %q (error=%v), want the current body and topic key", out, isErr)
	}
	if strings.Contains(out, "first version") || strings.Contains(out, "second version") || strings.Contains(out, "--- history") {
		t.Errorf("get = %q, want no earlier versions by default", out)
	}
	if !strings.HasSuffix(out, "\n(2 earlier versions not shown; history=true shows them)\n") {
		t.Errorf("get = %q, want a closing line naming 2 earlier versions and history=true", out)
	}
}

func TestGetOfAMemoryWithoutHistoryIsUnchanged(t *testing.T) {
	cs := connect(t, "p")
	call(t, cs, "mem_save", map[string]any{"title": "Solo", "body": "only version"})
	out, isErr := call(t, cs, "mem_get", map[string]any{"id": 1})
	if isErr || !strings.HasSuffix(out, "\n\nSolo\n\nonly version\n") {
		t.Errorf("get = %q (error=%v), want title and body as the last lines, no hint", out, isErr)
	}
}

func TestUpdateThenContext(t *testing.T) {
	cs := connect(t, "p")
	call(t, cs, "mem_save", map[string]any{"title": "Pinned rule", "body": "always do this"})
	call(t, cs, "mem_save", map[string]any{"title": "Other note", "body": "minor"})
	if out, isErr := call(t, cs, "mem_update", map[string]any{"id": 1, "pinned": true}); isErr {
		t.Fatalf("update: %q", out)
	}
	out, isErr := call(t, cs, "mem_context", nil)
	if isErr || !strings.HasPrefix(out, "#1 [pinned]") || !strings.Contains(out, "#2 [recent]") {
		t.Errorf("context = %q (error=%v), want pinned first then recent", out, isErr)
	}
	if out, isErr := call(t, cs, "mem_update", map[string]any{"id": 2, "archive": true}); isErr {
		t.Fatalf("archive: %q", out)
	}
	if out, _ := call(t, cs, "mem_context", nil); strings.Contains(out, "#2") {
		t.Errorf("archived memory in digest: %q", out)
	}
	if out, isErr := call(t, cs, "mem_update", map[string]any{"id": 1}); !isErr || !strings.Contains(out, "nothing to update") {
		t.Errorf("empty update: %q (error=%v)", out, isErr)
	}
}

func TestSaveOverAnArchivedMemoryRestoresIt(t *testing.T) {
	cs := connect(t, "p")
	call(t, cs, "mem_save", map[string]any{"title": "Plan", "body": "v1", "topic_key": "plan"})
	if out, isErr := call(t, cs, "mem_update", map[string]any{"id": 1, "archive": true}); isErr {
		t.Fatalf("archive: %q", out)
	}
	out, isErr := call(t, cs, "mem_save", map[string]any{"title": "Plan", "body": "v2", "topic_key": "plan"})
	if isErr || !strings.Contains(out, "updated #1") || !strings.Contains(out, "restored #1 from the archive") {
		t.Errorf("save = %q (error=%v), want updated #1 and a restored warning", out, isErr)
	}
	if out, _ := call(t, cs, "mem_context", nil); !strings.Contains(out, "#1") {
		t.Errorf("restored memory missing from the digest: %q", out)
	}
}

func TestSessionActions(t *testing.T) {
	cs := connect(t, "p")
	if out, isErr := call(t, cs, "mem_session", map[string]any{"action": "start", "id": "s1"}); isErr {
		t.Fatalf("start: %q", out)
	}
	out, isErr := call(t, cs, "mem_session", map[string]any{"action": "summary", "id": "s1", "text": "did the thing"})
	if isErr || !strings.Contains(out, "created #1") {
		t.Fatalf("summary: %q (error=%v)", out, isErr)
	}
	if out, _ := call(t, cs, "mem_session", map[string]any{"action": "summary", "id": "s1", "text": "did more"}); !strings.Contains(out, "updated #1") {
		t.Errorf("second summary = %q, want it to update #1", out)
	}
	if out, isErr := call(t, cs, "mem_session", map[string]any{"action": "end", "id": "s1"}); isErr {
		t.Errorf("end: %q", out)
	}
	if out, isErr := call(t, cs, "mem_session", map[string]any{"action": "bogus", "id": "s1"}); !isErr || !strings.Contains(out, "start, end or summary") {
		t.Errorf("bad action: %q (error=%v)", out, isErr)
	}
}
