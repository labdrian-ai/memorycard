package cli

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/labdrian-ai/memorycard/internal/memory"
	"github.com/labdrian-ai/memorycard/internal/render"
)

const (
	// captureTitleRunes caps a capture title; a longer first sentence is cut
	// at the last word boundary within it.
	captureTitleRunes = 100
	// captureMinRunes is the shortest item worth keeping: shorter ones are
	// fragments without enough context to be found or trusted later.
	captureMinRunes = 60
)

// Reasons capture gives for not saving an item.
const (
	skipShort     = "too short"
	skipProbe     = "probe marker"
	skipDuplicate = "duplicate"
)

var (
	learningsHeading = regexp.MustCompile(`(?i)^#{1,6}\s+key learnings\s*$`)
	numberedItem     = regexp.MustCompile(`^\s*\d+[.)]\s+(.*\S)\s*$`)
	// probeMarker matches the tokens hook and pipeline checks write on
	// purpose, such as codexprobe1730, probe42 or PROBE-42: a word ending in
	// "probe" followed by digits, with an optional - or _ between.
	probeMarker = regexp.MustCompile(`(?i)\b[a-z]*probe[-_]?\d+\b`)
	// sentenceEnd is the end of a sentence: a stop, question or exclamation
	// mark followed by white space.
	sentenceEnd = regexp.MustCompile(`[.!?]\s`)
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

// captureTitle is the item's first sentence without its final stop. A first
// sentence over captureTitleRunes is cut at the last space within the limit
// and marked with "...".
func captureTitle(item string) string {
	first := item
	if loc := sentenceEnd.FindStringIndex(item); loc != nil {
		first = item[:loc[0]+1]
	}
	if first = strings.TrimSuffix(strings.TrimSpace(first), "."); first == "" {
		first = item
	}
	if utf8.RuneCountInString(first) <= captureTitleRunes {
		return first
	}
	r := []rune(first)[:captureTitleRunes+1]
	cut := string(r)
	if i := strings.LastIndexByte(cut, ' '); i > 0 {
		cut = cut[:i]
	} else {
		cut = string(r[:captureTitleRunes])
	}
	return strings.TrimRight(cut, " ,;:") + "..."
}

// skipReason says why item is not worth saving, or "" to save it.
func skipReason(item string) string {
	switch {
	case utf8.RuneCountInString(item) < captureMinRunes:
		return skipShort
	case probeMarker.MatchString(item):
		return skipProbe
	}
	return ""
}

// capture saves each Key Learnings item read from stdin as one memory, through
// the same Save every other command uses, so redaction, caps and dedup by
// content are the save's own. Short items and probe markers are skipped. The
// saved items are embedded together in one embedder call at the end, and a
// last line counts what was saved and skipped, and why.
//
// With --transcript, the memories go to the project of the repository the
// subagent's transcript names most, when that is not the working directory's
// repository: a subagent started here may have worked in another repo.
func (a *app) capture(ctx context.Context, svc *memory.Service, args []string) int {
	fs := a.flags("capture")
	transcript := fs.String("transcript", "", "subagent transcript: file under the project of the repository it worked in")
	if fs.Parse(args) != nil {
		return 2
	}
	b, err := io.ReadAll(a.in)
	if err != nil {
		return a.fail(err)
	}
	items := keyLearnings(string(b))
	if len(items) == 0 {
		return 0
	}
	proj := a.captureProject(*transcript)
	skipped := map[string]int{}
	// Output keeps item order. A save result is rendered only after the batch
	// embed has set its duplicate hint and neighbours.
	type line struct {
		text string
		res  *memory.SaveResult
	}
	var lines []line
	var results []*memory.SaveResult
	flush := func() {
		for _, l := range lines {
			if l.res != nil {
				fmt.Fprint(a.out, render.Save(*l.res))
			} else {
				fmt.Fprint(a.out, l.text)
			}
		}
	}
	nSaved := 0
	for _, item := range items {
		title := captureTitle(item)
		if why := skipReason(item); why != "" {
			skipped[why]++
			lines = append(lines, line{text: fmt.Sprintf("skipped (%s): %s\n", why, title)})
			continue
		}
		r, err := svc.Save(ctx, memory.SaveInput{
			Project: proj, Scope: "project", Type: "learning",
			Title: title, Body: item, DeferEmbed: true,
		})
		if err != nil {
			flush()
			return a.fail(err)
		}
		lines = append(lines, line{res: &r})
		results = append(results, &r)
		switch r.Action {
		case memory.ActionCreated, memory.ActionUpdated:
			nSaved++
		default:
			skipped[skipDuplicate]++
		}
	}
	embedErr := svc.FinishDeferred(ctx, results)
	flush()
	if embedErr != nil {
		fmt.Fprintf(a.out, "warning: embedding pending: %v\n", embedErr)
	}
	fmt.Fprintf(a.out, "captured: %d saved, %d skipped%s\n", nSaved,
		skipped[skipShort]+skipped[skipProbe]+skipped[skipDuplicate], skipDetail(skipped))
	return 0
}

// skipDetail lists the skip reasons with their counts, in a fixed order.
func skipDetail(skipped map[string]int) string {
	var parts []string
	for _, why := range []string{skipShort, skipProbe, skipDuplicate} {
		if n := skipped[why]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, why))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}
