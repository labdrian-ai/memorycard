package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/labdrian-ai/memorycard/internal/memory"
)

// List formats a list result as one line per memory, "no results" when empty.
func List(ms []memory.Listed) string {
	if len(ms) == 0 {
		return "no results\n"
	}
	var b strings.Builder
	for _, m := range ms {
		fmt.Fprintf(&b, "#%d %s: %s\n", m.ID, m.TopicKey, m.Title)
	}
	return b.String()
}

type listedJSON struct {
	ID       int64  `json:"id"`
	TopicKey string `json:"topic_key"`
	Title    string `json:"title"`
	Body     string `json:"body"`
}

// ListJSON formats a list result as {"project", "memories"}; memories is
// always an array, [] when nothing matched.
func ListJSON(project string, ms []memory.Listed) (string, error) {
	out := struct {
		Project  string       `json:"project"`
		Memories []listedJSON `json:"memories"`
	}{Project: project, Memories: make([]listedJSON, len(ms))}
	for i, m := range ms {
		out.Memories[i] = listedJSON(m)
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // bodies hold <, > and &; keep them readable
	if err := enc.Encode(out); err != nil {
		return "", err
	}
	return b.String(), nil
}
