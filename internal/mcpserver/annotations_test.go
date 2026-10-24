package mcpserver

import (
	"context"
	"testing"
)

// Clients such as Codex ask for approval on every tool that does not declare
// itself read-only, so the reading tools must say so and the writing tools
// must say they never destroy data.
func TestToolAnnotations(t *testing.T) {
	cs := connect(t, "p")
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	readOnly := map[string]bool{"mem_search": true, "mem_get": true, "mem_context": true}
	for _, tool := range res.Tools {
		a := tool.Annotations
		if a == nil {
			t.Errorf("%s has no annotations", tool.Name)
			continue
		}
		if a.ReadOnlyHint != readOnly[tool.Name] {
			t.Errorf("%s ReadOnlyHint = %v, want %v", tool.Name, a.ReadOnlyHint, readOnly[tool.Name])
		}
		if !readOnly[tool.Name] && (a.DestructiveHint == nil || *a.DestructiveHint) {
			t.Errorf("%s must declare DestructiveHint=false", tool.Name)
		}
	}
}
