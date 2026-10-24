// Package render formats memory results as compact text. The MCP server and
// the CLI share it, so an agent and a human read the same output.
package render

import (
	"fmt"
	"strings"

	"github.com/labdrian-ai/memorycard/internal/memory"
)

// Search formats a search result. Notes appear only when something is off
// (lexical fallback, low confidence), so the common answer is just the hits.
func Search(r memory.SearchResult) string {
	var b strings.Builder
	if r.Mode == memory.ModeLexical {
		b.WriteString("mode: lexical (embedder unavailable)\n")
	}
	if r.Confidence == memory.ConfidenceLow && len(r.Hits) > 0 {
		b.WriteString("confidence: low (results may not answer the query)\n")
	}
	if r.Widened {
		b.WriteString("note: nothing relevant in this project; showing all projects\n")
	}
	if len(r.Hits) == 0 {
		b.WriteString("no results\n")
		return b.String()
	}
	for _, h := range r.Hits {
		fmt.Fprintf(&b, "#%d %s: %s", h.ID, h.Type, h.Title)
		if r.Widened {
			fmt.Fprintf(&b, " [%s]", h.Project)
		}
		if h.Stale {
			b.WriteString(" [stale]")
		}
		b.WriteByte('\n')
		if h.Snippet != "" {
			fmt.Fprintf(&b, "  %s\n", h.Snippet)
		}
	}
	return b.String()
}

// Save formats the outcome of a save.
func Save(r memory.SaveResult) string {
	var b strings.Builder
	if r.Action == memory.ActionDuplicate {
		fmt.Fprintf(&b, "duplicate of #%d (nothing written)\n", r.ID)
	} else {
		fmt.Fprintf(&b, "%s #%d\n", r.Action, r.ID)
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "warning: %s\n", w)
	}
	if r.Hint != nil {
		fmt.Fprintf(&b, "possible duplicate of #%d (%.2f): %s\n", r.Hint.ID, r.Hint.Score, r.Hint.Title)
	}
	return b.String()
}

// Memory formats one full memory with its history.
func Memory(m memory.Memory) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#%d %s project=%s scope=%s status=%s", m.ID, m.Type, m.Project, m.Scope, m.Status)
	if m.Pinned {
		b.WriteString(" pinned")
	}
	if m.TopicKey != "" {
		fmt.Fprintf(&b, " topic=%s", m.TopicKey)
	}
	if m.SupersededBy != 0 {
		fmt.Fprintf(&b, " superseded_by=#%d", m.SupersededBy)
	}
	fmt.Fprintf(&b, "\nupdated %s\n\n%s\n\n%s\n", m.UpdatedAt, m.Title, m.Body)
	for _, v := range m.History {
		fmt.Fprintf(&b, "\n--- history v%d (%s): %s\n%s\n", v.Version, v.SavedAt, v.Title, v.Body)
	}
	return b.String()
}
