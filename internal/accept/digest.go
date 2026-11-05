package accept

import (
	"context"
	"strings"

	"github.com/labdrian-ai/memorycard/internal/memory"
)

// DigestCount is the noise in one project's session-start digest (the lines
// `memorycard context` prints): session summaries, further parts of a
// document already listed, and titles cut with "...". A line counts once in
// Noisy even when it is noise for more than one reason.
type DigestCount struct {
	Project                                   string
	Lines, Summaries, Parts, Truncated, Noisy int
}

// Share is the fraction of lines that are noise.
func (d DigestCount) Share() float64 {
	if d.Lines == 0 {
		return 0
	}
	return float64(d.Noisy) / float64(d.Lines)
}

// DigestNoise builds the default-budget digest of each project and counts
// its noisy lines.
func DigestNoise(ctx context.Context, svc *memory.Service, projects []string) ([]DigestCount, error) {
	var out []DigestCount
	for _, p := range projects {
		d, err := svc.Context(ctx, p, memory.ContextBudgetBytes)
		if err != nil {
			return nil, err
		}
		c := DigestCount{Project: p, Lines: len(d.Entries)}
		listed := map[string]bool{}
		for _, e := range d.Entries {
			summary := e.Type == "session_summary"
			part := false
			if partSuffix.MatchString(e.Title) {
				base := strings.ToLower(partSuffix.ReplaceAllString(e.Title, ""))
				part = listed[base]
				listed[base] = true
			}
			cut := strings.HasSuffix(e.Title, "...") || strings.HasSuffix(e.Title, "…")
			for _, hit := range []struct {
				on bool
				n  *int
			}{{summary, &c.Summaries}, {part, &c.Parts}, {cut, &c.Truncated}} {
				if hit.on {
					*hit.n++
				}
			}
			if summary || part || cut {
				c.Noisy++
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// Total adds the counts of every project.
func Total(cs []DigestCount) DigestCount {
	t := DigestCount{Project: "total"}
	for _, c := range cs {
		t.Lines += c.Lines
		t.Summaries += c.Summaries
		t.Parts += c.Parts
		t.Truncated += c.Truncated
		t.Noisy += c.Noisy
	}
	return t
}
