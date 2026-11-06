package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/labdrian-ai/memorycard/internal/memory"
	"github.com/labdrian-ai/memorycard/internal/project"
)

const (
	// artifactPrefix opens summaries that carry relayed content; it is noise.
	artifactPrefix = "<artifact-content-authored-by-others/>"
	// freeFormBytes is how much of a summary without numbered sections is kept.
	freeFormBytes = 4096
	// userItemRunes cuts each item of the user-messages list.
	userItemRunes = 160
	// userListBytes caps the user-messages list.
	userListBytes = 1500
	// titleRunes cuts the first line of the primary request in the title.
	titleRunes = 80
)

// sectionKeywords names, for each numbered section of a compact summary, a
// lower-case phrase its heading contains. A numbered line is a section heading
// only when its text holds the phrase of its number, so numbered items inside
// a section, or in a free-form summary, are not mistaken for headings.
var sectionKeywords = map[int]string{
	1: "primary request", 2: "key technical", 3: "files and code", 4: "error",
	5: "problem solving", 6: "user messages", 7: "pending task", 8: "current work", 9: "next step",
}

// keptSections are the sections the stored body keeps, in order.
var keptSections = []int{1, 4, 5, 6, 7, 8}

var (
	sectionLine = regexp.MustCompile(`^ {0,3}(?:#{1,6}[ \t]*)?(?:\*\*)?(\d)\.[ \t]*(.*)$`)
	listMarker  = regexp.MustCompile(`^\s*(?:\d+[.)]|[-*•])\s+`)
)

type section struct {
	title string
	lines []string
}

// splitSections cuts a compact summary into its numbered sections.
func splitSections(summary string) map[int]*section {
	secs := map[int]*section{}
	var cur *section
	last := 0
	for _, line := range strings.Split(summary, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if m := sectionLine.FindStringSubmatch(line); m != nil {
			n := int(m[1][0] - '0')
			title := strings.Trim(strings.ReplaceAll(m[2], "*", ""), " \t:")
			if kw := sectionKeywords[n]; kw != "" && n > last && strings.Contains(strings.ToLower(title), kw) {
				cur = &section{title: title}
				secs[n] = cur
				last = n
				continue
			}
		}
		if cur != nil {
			cur.lines = append(cur.lines, line)
		}
	}
	return secs
}

// content is the section text without surrounding blank lines and common
// indentation.
func (s *section) content() string {
	lines := s.lines
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	indent := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if n := len(l) - len(strings.TrimLeft(l, " \t")); indent < 0 || n < indent {
			indent = n
		}
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		if len(l) >= indent && indent > 0 {
			l = l[indent:]
		}
		out[i] = l
	}
	return strings.Join(out, "\n")
}

// userList is the user-messages section as a short list: one line per item,
// each cut to userItemRunes, the whole list at most userListBytes.
func (s *section) userList() string {
	var items []string
	for _, l := range s.lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if listMarker.MatchString(l) || len(items) == 0 {
			items = append(items, strings.TrimSpace(listMarker.ReplaceAllString(l, "")))
		} else {
			items[len(items)-1] += " " + strings.TrimSpace(l)
		}
	}
	var b strings.Builder
	for _, it := range items {
		if r := []rune(it); len(r) > userItemRunes {
			it = string(r[:userItemRunes-3]) + "..."
		}
		line := "- " + it + "\n"
		if b.Len()+len(line) > userListBytes {
			break
		}
		b.WriteString(line)
	}
	return strings.TrimRight(b.String(), "\n")
}

// cutBytes returns s cut to at most n bytes on a character boundary.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func cutRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// firstLine is the first non-blank line of text without list or bold marks.
func firstLine(text string) string {
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(listMarker.ReplaceAllString(l, ""))
		l = strings.TrimSpace(strings.ReplaceAll(l, "**", ""))
		if l != "" && !strings.EqualFold(strings.TrimRight(l, ":"), "summary") {
			return l
		}
	}
	return ""
}

// summaryBody turns a compact summary into the stored body (without the date
// line) and the first line of its primary request.
func summaryBody(summary string) (body, request string) {
	secs := splitSections(summary)
	if secs[1] == nil && secs[7] == nil && secs[8] == nil {
		return cutBytes(summary, freeFormBytes), firstLine(summary)
	}
	var parts []string
	for _, n := range keptSections {
		s := secs[n]
		if s == nil {
			continue
		}
		text := s.content()
		if n == 6 {
			text = s.userList()
		}
		if text == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("## %d. %s\n%s", n, s.title, text))
	}
	if secs[1] != nil {
		request = firstLine(secs[1].content())
	}
	return strings.Join(parts, "\n\n"), request
}

// hookEvent is the part of a Claude Code PostCompact event that ingest reads.
type hookEvent struct {
	SessionID      string `json:"session_id"`
	CWD            string `json:"cwd"`
	TranscriptPath string `json:"transcript_path"`
	AgentID        string `json:"agent_id"`
	CompactSummary string `json:"compact_summary"`
}

// transcriptStart is the timestamp of the first line of the transcript.
func transcriptStart(path string) (time.Time, bool) {
	if path == "" {
		return time.Time{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false
	}
	defer func() { _ = f.Close() }()
	b, _ := io.ReadAll(io.LimitReader(f, 1<<20))
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		b = b[:i]
	}
	var l struct {
		Timestamp string `json:"timestamp"`
	}
	if json.Unmarshal(b, &l) != nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, l.Timestamp)
	return t.UTC().Truncate(time.Second), err == nil
}

// session runs "session <verb>". Only "ingest" exists.
func (a *app) session(args []string) (code int) {
	if len(args) == 0 || args[0] != "ingest" {
		fmt.Fprintln(a.err, "usage: memorycard session ingest --from claude-postcompact")
		return 2
	}
	fs := a.flags("session ingest")
	from := fs.String("from", "", "hook event format: claude-postcompact")
	if fs.Parse(args[1:]) != nil {
		return 2
	}
	if *from != "claude-postcompact" {
		fmt.Fprintf(a.err, "unknown --from %q (want claude-postcompact)\n", *from)
		return 2
	}
	// A hook must never fail the session: every error below ends in exit 0.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(a.err, "session ingest skipped: %v\n", r)
			code = 0
		}
	}()
	if err := a.ingest(context.Background()); err != nil {
		fmt.Fprintf(a.err, "session ingest skipped: %v\n", err)
	}
	return 0
}

// ingest saves the compact summary of one PostCompact event as the session's
// memory, without embedding it: the hook must return fast, and `embed` indexes
// it later.
func (a *app) ingest(ctx context.Context) error {
	var ev hookEvent
	if err := json.NewDecoder(a.in).Decode(&ev); err != nil {
		return fmt.Errorf("unreadable hook event: %w", err)
	}
	if ev.AgentID != "" {
		return nil // a subagent's compaction is not the session's
	}
	summary := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ev.CompactSummary), artifactPrefix))
	if summary == "" {
		return fmt.Errorf("empty compact_summary")
	}
	if strings.TrimSpace(ev.SessionID) == "" {
		return fmt.Errorf("missing session_id")
	}
	if summary = memory.RedactSecrets(summary); memory.BlankAfterRedaction(summary) {
		return fmt.Errorf("compact_summary is empty after redaction")
	}

	start, ok := transcriptStart(ev.TranscriptPath)
	if !ok {
		start = time.Now().UTC().Truncate(time.Second)
	}
	proj := a.getenv("MEMORYCARD_PROJECT")
	if ev.CWD != "" {
		proj = project.ResolveMain(ev.CWD, proj)
	} else if proj == "" {
		proj = a.project()
	}

	body, request := summaryBody(summary)
	title := fmt.Sprintf("Session %s · %s", start.Format("2006-01-02"), proj)
	if request = cutRunes(request, titleRunes); request != "" {
		title += " · " + request
	}
	dateLine := fmt.Sprintf("Date: %s · cwd: %s · transcript: %s", start.Format(time.RFC3339), ev.CWD, ev.TranscriptPath)
	body = cutBytes(memory.RedactSecrets(dateLine+"\n\n"+body), memory.HardCapBytes)

	svc, _, closeDB, err := a.open()
	if err != nil {
		return err
	}
	defer closeDB()
	_, err = svc.Save(ctx, memory.SaveInput{
		Project: proj, Scope: "project", Type: "session_summary",
		Title: memory.RedactSecrets(title), Body: body,
		TopicKey: "session/" + strings.TrimSpace(ev.SessionID), DeferEmbed: true,
	})
	return err
}
