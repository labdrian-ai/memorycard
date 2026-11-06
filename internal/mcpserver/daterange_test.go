package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSearchDateFieldsAreOptionalAndFilter(t *testing.T) {
	cs := connect(t, "p")
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if tool.Name != "mem_search" {
			continue
		}
		raw, _ := json.Marshal(tool.InputSchema)
		var sch struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(raw, &sch); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"since", "until"} {
			if _, ok := sch.Properties[f]; !ok {
				t.Errorf("mem_search schema lacks %q", f)
			}
			for _, r := range sch.Required {
				if r == f {
					t.Errorf("%q must be optional, required = %v", f, sch.Required)
				}
			}
		}
		if len(sch.Required) != 1 || sch.Required[0] != "query" {
			t.Errorf("required = %v, want only query so existing callers are unchanged", sch.Required)
		}
	}
	call(t, cs, "mem_save", map[string]any{"title": "Rollout plan", "body": "canary first"})
	today := time.Now().UTC().Format("2006-01-02")
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	if out, _ := call(t, cs, "mem_search", map[string]any{"query": "canary", "since": today, "until": today}); !strings.Contains(out, "Rollout plan") {
		t.Errorf("today's range = %q, want the memory", out)
	}
	if out, _ := call(t, cs, "mem_search", map[string]any{"query": "canary", "since": tomorrow}); !strings.Contains(out, "no results") {
		t.Errorf("since tomorrow = %q, want no results", out)
	}
	if out, isErr := call(t, cs, "mem_search", map[string]any{"query": "canary", "since": "2026-13-40"}); !isErr || !strings.Contains(out, "invalid since date") {
		t.Errorf("bad date = %q (error=%v), want a tool error 'invalid since date'", out, isErr)
	}
}
