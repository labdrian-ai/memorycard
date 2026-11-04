package cli

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/labdrian-ai/memorycard/internal/memory"
	"github.com/labdrian-ai/memorycard/internal/render"
)

const captureTitleRunes = 80

var (
	learningsHeading = regexp.MustCompile(`(?i)^#{1,6}\s+key learnings\s*$`)
	numberedItem     = regexp.MustCompile(`^\s*\d+[.)]\s+(.*\S)\s*$`)
)

// keyLearnings returns the numbered items of every "## Key Learnings" block
// in text. A block ends at the next heading; an item continues over the
// non-blank lines that follow it.
func keyLearnings(text string) []string {
	var items []string
	in, open := false, false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case learningsHeading.MatchString(strings.TrimSpace(line)):
			in, open = true, false
		case !in:
		case strings.HasPrefix(strings.TrimSpace(line), "#"):
			in, open = false, false
		case numberedItem.MatchString(line):
			items = append(items, numberedItem.FindStringSubmatch(line)[1])
			open = true
		case strings.TrimSpace(line) == "":
			open = false
		case open:
			items[len(items)-1] += " " + strings.TrimSpace(line)
		}
	}
	return items
}

func captureTitle(item string) string {
	r := []rune(item)
	if len(r) <= captureTitleRunes {
		return item
	}
	return strings.TrimSpace(string(r[:captureTitleRunes])) + "..."
}

// capture saves each Key Learnings item read from stdin as one memory, through
// the same Save every other command uses, so redaction, caps and dedup by
// content are the save's own.
func (a *app) capture(ctx context.Context, svc *memory.Service) int {
	b, err := io.ReadAll(a.in)
	if err != nil {
		return a.fail(err)
	}
	proj := a.project()
	for _, item := range keyLearnings(string(b)) {
		r, err := svc.Save(ctx, memory.SaveInput{
			Project: proj, Scope: "project", Type: "learning",
			Title: captureTitle(item), Body: item,
		})
		if err != nil {
			return a.fail(err)
		}
		fmt.Fprint(a.out, render.Save(r))
	}
	return 0
}
