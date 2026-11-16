package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/labdrian-ai/memorycard/internal/memory"
)

// Resolve formats a resolve result as one line per key, in the order given:
// "<source>:<key> #<id>" or "<source>:<key> unknown".
func Resolve(source string, rs []memory.Resolved) string {
	var b strings.Builder
	for _, r := range rs {
		if r.Found {
			fmt.Fprintf(&b, "%s:%s #%d\n", source, r.Key, r.MemoryID)
		} else {
			fmt.Fprintf(&b, "%s:%s unknown\n", source, r.Key)
		}
	}
	return b.String()
}

type resolvedJSON struct {
	Key      string `json:"key"`
	MemoryID *int64 `json:"memory_id"` // null when the key was never imported
}

// ResolveJSON formats a resolve result as {"source", "ids"}, one entry per
// key in the order given.
func ResolveJSON(source string, rs []memory.Resolved) (string, error) {
	out := struct {
		Source string         `json:"source"`
		IDs    []resolvedJSON `json:"ids"`
	}{Source: source, IDs: make([]resolvedJSON, len(rs))}
	for i, r := range rs {
		out.IDs[i].Key = r.Key
		if r.Found {
			id := r.MemoryID
			out.IDs[i].MemoryID = &id
		}
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return "", err
	}
	return b.String(), nil
}
