package mcpserver

import (
	"context"
	"encoding/json"
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

func TestGetShowsBodyAndHistory(t *testing.T) {
	cs := connect(t, "p")
	call(t, cs, "mem_save", map[string]any{"title": "Auth", "body": "first version", "topic_key": "arch/auth"})
	call(t, cs, "mem_save", map[string]any{"title": "Auth", "body": "second version", "topic_key": "arch/auth"})
	out, isErr := call(t, cs, "mem_get", map[string]any{"id": 1})
	if isErr || !strings.Contains(out, "second version") || !strings.Contains(out, "first version") || !strings.Contains(out, "arch/auth") {
		t.Errorf("get = %q (error=%v), want current body, history and topic key", out, isErr)
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
