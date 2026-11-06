package cli

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labdrian-ai/memorycard/internal/store"
)

// hook is the PostCompact event a Claude Code hook writes on stdin.
func hook(t *testing.T, fields map[string]any) string {
	t.Helper()
	ev := map[string]any{"session_id": "s1", "cwd": t.TempDir(), "transcript_path": "",
		"hook_event_name": "PostCompact", "trigger": "auto", "compact_summary": "1. Primary Request and Intent:\n   Do the thing.\n"}
	for k, v := range fields {
		if v == nil {
			delete(ev, k)
		} else {
			ev[k] = v
		}
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func ingest(t *testing.T, db string, env map[string]string, stdin string) (int, string, string) {
	t.Helper()
	return runEnv(t, db, env, stdin, "session", "ingest", "--from", "claude-postcompact")
}

type memRow struct {
	ID                                   int64
	Project, Type, Title, Body, TopicKey string
	Status                               string
}

func memRows(t *testing.T, db string) []memRow {
	t.Helper()
	d, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	rows, err := d.SQL().Query(`select id, project, type, title, body, coalesce(topic_key,''), status from memories order by id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []memRow
	for rows.Next() {
		var r memRow
		if err := rows.Scan(&r.ID, &r.Project, &r.Type, &r.Title, &r.Body, &r.TopicKey, &r.Status); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func rowsHash(t *testing.T, db string) string {
	t.Helper()
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%v", memRows(t, db)))))
}

func oneSession(t *testing.T, db string) memRow {
	t.Helper()
	rows := memRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("want exactly one memory, got %d: %+v", len(rows), rows)
	}
	return rows[0]
}

func TestSessionIngestSavesOneMemoryPerSession(t *testing.T) {
	db := newDB(t)
	code, out, errOut := ingest(t, db, nil, hook(t, nil))
	if code != 0 || out != "" || errOut != "" {
		t.Fatalf("ingest: code=%d out=%q err=%q", code, out, errOut)
	}
	first := oneSession(t, db)
	if first.Type != "session_summary" || first.TopicKey != "session/s1" || first.Status != "active" {
		t.Errorf("row = %+v, want active session_summary with topic_key session/s1", first)
	}
	code, out, errOut = ingest(t, db, nil, hook(t, map[string]any{"compact_summary": "1. Primary Request and Intent:\n   A different summary.\n"}))
	if code != 0 || out != "" || errOut != "" {
		t.Fatalf("second ingest: code=%d out=%q err=%q", code, out, errOut)
	}
	second := oneSession(t, db)
	if second.ID != first.ID || !strings.Contains(second.Body, "A different summary.") {
		t.Errorf("second row = %+v, want same id %d updated", second, first.ID)
	}
	_, stats, _ := run(t, db, "", "stats")
	if !strings.Contains(stats, "active: 1\n") {
		t.Errorf("stats = %q, want active: 1", stats)
	}
}

func TestSessionIngestNeverFailsTheHook(t *testing.T) {
	for name, in := range map[string]string{
		"malformed json":    "{not json",
		"empty stdin":       "",
		"empty summary":     hook(t, map[string]any{"compact_summary": ""}),
		"blank summary":     hook(t, map[string]any{"compact_summary": " \n\t"}),
		"missing session":   hook(t, map[string]any{"session_id": nil}),
		"summary all stars": hook(t, map[string]any{"compact_summary": "<private>x</private>"}),
	} {
		db := newDB(t)
		code, out, errOut := ingest(t, db, nil, in)
		if code != 0 || out != "" || strings.TrimSpace(errOut) == "" {
			t.Errorf("%s: code=%d out=%q err=%q, want exit 0, empty stdout and a reason on stderr", name, code, out, errOut)
		}
		if _, err := os.Stat(db); err == nil {
			if rows := memRows(t, db); len(rows) != 0 {
				t.Errorf("%s: wrote %+v", name, rows)
			}
		}
	}
}

func TestSessionIngestUnopenableStoreExitsZero(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := ingest(t, filepath.Join(blocker, "sub", "memory.db"), nil, hook(t, nil))
	if code != 0 || out != "" || strings.TrimSpace(errOut) == "" {
		t.Errorf("code=%d out=%q err=%q, want exit 0 with the reason on stderr", code, out, errOut)
	}
}

func TestSessionIngestSkipsSubagents(t *testing.T) {
	db := newDB(t)
	run(t, db, "", "save", "--title", "Existing", "--body", "kept as is")
	before := rowsHash(t, db)
	code, out, _ := ingest(t, db, nil, hook(t, map[string]any{"agent_id": "a1b2"}))
	if code != 0 || out != "" {
		t.Errorf("subagent ingest: code=%d out=%q", code, out)
	}
	if rows := memRows(t, db); len(rows) != 1 || rowsHash(t, db) != before {
		t.Errorf("subagent event changed the store: %+v", rows)
	}
}

func TestSessionIngestUsageErrors(t *testing.T) {
	db := newDB(t)
	for _, args := range [][]string{{"session"}, {"session", "ingest"}, {"session", "ingest", "--from", "codex"}, {"session", "nope"}} {
		if code, out, _ := run(t, db, "", args...); code != 2 || out != "" {
			t.Errorf("%v: code=%d out=%q, want exit 2", args, code, out)
		}
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "summaries", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSessionIngestKeepsTheEpisodicSections(t *testing.T) {
	for _, name := range []string{"plain.md", "bold.md", "hashes.md", "lowercase.md"} {
		db := newDB(t)
		code, _, errOut := ingest(t, db, nil, hook(t, map[string]any{"compact_summary": fixture(t, name)}))
		if code != 0 || errOut != "" {
			t.Fatalf("%s: code=%d err=%q", name, code, errOut)
		}
		body := oneSession(t, db).Body
		for _, keep := range []string{"KEEP1", "KEEP4", "KEEP5", "KEEP6a", "KEEP7", "KEEP8"} {
			if !strings.Contains(body, keep) {
				t.Errorf("%s: body lost %s:\n%s", name, keep, body)
			}
		}
		for _, drop := range []string{"DROP2", "DROP3", "DROP9", "artifact-content-authored-by-others"} {
			if strings.Contains(body, drop) {
				t.Errorf("%s: body kept %s:\n%s", name, drop, body)
			}
		}
	}
}

func TestSessionIngestTrimsTheUserMessageList(t *testing.T) {
	long := strings.Repeat("x", 300)
	var b strings.Builder
	b.WriteString("1. Primary Request and Intent:\n   Goal.\n\n6. All user messages:\n")
	b.WriteString("   - LONGITEM" + long + "\n")
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "   - request number %02d with some filler text to take room in the list\n", i)
	}
	b.WriteString("\n7. Pending Tasks:\n   - none\n")
	db := newDB(t)
	ingest(t, db, nil, hook(t, map[string]any{"compact_summary": b.String()}))
	body := oneSession(t, db).Body
	start := strings.Index(body, "LONGITEM")
	if start < 0 {
		t.Fatalf("body lost the first user message:\n%s", body)
	}
	item := strings.SplitN(body[start:], "\n", 2)[0]
	if n := len([]rune(item)); n > 160 {
		t.Errorf("first item is %d characters, want at most 160", n)
	}
	list := body[strings.Index(body, "## 6."):strings.Index(body, "## 7.")]
	items := strings.Count(list, "\n- ")
	if len(list) > 1500+len("## 6. All user messages\n")+2 || items >= 60 {
		t.Errorf("list is %d bytes with %d items, want it capped at 1500 bytes", len(list), items)
	}
}

func TestSessionIngestFreeFormFallsBackToTheFirst4096Bytes(t *testing.T) {
	short := fixture(t, "freeform.md")
	db := newDB(t)
	ingest(t, db, nil, hook(t, map[string]any{"compact_summary": short}))
	for _, want := range []string{"FREE1", "FREE2", "FREE3"} {
		if body := oneSession(t, db).Body; !strings.Contains(body, want) {
			t.Errorf("free-form body lost %s:\n%s", want, body)
		}
	}

	long := strings.Repeat("a", 4090) + "ENDOFKEEP" + strings.Repeat("b", 500)
	db = newDB(t)
	ingest(t, db, nil, hook(t, map[string]any{"compact_summary": long}))
	body := oneSession(t, db).Body
	if !strings.Contains(body, long[:4096]) || strings.Contains(body, long[:4097]) {
		t.Errorf("free-form body is not exactly the first 4096 bytes (len %d)", len(body))
	}
}

func TestSessionIngestTitleAndDateLine(t *testing.T) {
	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	line := `{"type":"user","timestamp":"2026-11-05T08:30:00.123Z","message":"hi"}` + "\n" + `{"timestamp":"2027-01-01T00:00:00Z"}` + "\n"
	if err := os.WriteFile(transcript, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(t.TempDir(), "demo")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	summary := "1. Primary Request and Intent:\n   " + strings.Repeat("w", 120) + "\n   second line\n"
	db := newDB(t)
	ingest(t, db, map[string]string{"MEMORYCARD_PROJECT": ""}, hook(t, map[string]any{
		"session_id": "0b9c3f2e-1111-4222-8333-444455556666", "cwd": cwd, "transcript_path": transcript, "compact_summary": summary}))
	r := oneSession(t, db)
	wantTitle := "Session 2026-11-05 · demo · " + strings.Repeat("w", 80)
	if r.Title != wantTitle {
		t.Errorf("title = %q, want %q", r.Title, wantTitle)
	}
	if strings.Contains(r.Title, "0b9c3f2e") {
		t.Errorf("title carries the session UUID: %q", r.Title)
	}
	wantLine := "Date: 2026-11-05T08:30:00Z · cwd: " + cwd + " · transcript: " + transcript
	if first := strings.SplitN(r.Body, "\n", 2)[0]; first != wantLine {
		t.Errorf("first body line = %q, want %q", first, wantLine)
	}
}

func TestSessionIngestUnreadableTranscriptUsesIngestTime(t *testing.T) {
	db := newDB(t)
	ingest(t, db, nil, hook(t, map[string]any{"transcript_path": "/nonexistent/t.jsonl"}))
	r := oneSession(t, db)
	today := time.Now().UTC().Format("2006-01-02")
	if !strings.HasPrefix(r.Title, "Session "+today+" · ") || !strings.HasPrefix(r.Body, "Date: "+today+"T") {
		t.Errorf("title=%q body=%q, want today's UTC date", r.Title, r.Body)
	}
}

func TestSessionIngestRedactsSecrets(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.sig_nature-1"
	cases := map[string]struct{ in, secret string }{
		"KEY":         {"set API_KEY=abcd1234efgh now", "abcd1234efgh"},
		"PASSWORD":    {"DB_PASSWORD=hunter2hunter2 in env", "hunter2hunter2"},
		"SECRET":      {"CLIENT_SECRET=s3cr3tvalue99", "s3cr3tvalue99"},
		"TOKEN":       {"export GH_TOKEN=tok123456789", "tok123456789"},
		"quoted":      {`password: "correct horse battery"`, "correct horse battery"},
		"Bearer":      {"Authorization: Bearer abcDEF123456xyz", "abcDEF123456xyz"},
		"ghp_":        {"ghp_" + strings.Repeat("A1b2", 6), strings.Repeat("A1b2", 6)},
		"gho_":        {"gho_" + strings.Repeat("Z9y8", 6), strings.Repeat("Z9y8", 6)},
		"github_pat_": {"github_pat_" + strings.Repeat("Q7w3", 8), strings.Repeat("Q7w3", 8)},
		"sk-":         {"key sk-" + strings.Repeat("k3Ls", 8), strings.Repeat("k3Ls", 8)},
		"AKIA":        {"AKIAIOSFODNN7EXAMPLE is the id", "AKIAIOSFODNN7EXAMPLE"},
		"JWT":         {"jwt " + jwt + " end", jwt},
		"PEM":         {"-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX\n-----END RSA PRIVATE KEY-----", "MIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX"},
		"URL":         {"clone https://deploy:pa55w0rd@git.example.com/r.git", "pa55w0rd"},
	}
	for name, c := range cases {
		db := newDB(t)
		summary := "1. Primary Request and Intent:\n   Goal " + name + ".\n\n8. Current Work:\n   " + c.in + "\n"
		code, _, errOut := ingest(t, db, nil, hook(t, map[string]any{"compact_summary": summary}))
		if code != 0 || errOut != "" {
			t.Errorf("%s: code=%d err=%q", name, code, errOut)
			continue
		}
		body := oneSession(t, db).Body
		if strings.Contains(body, c.secret) || !strings.Contains(body, "[REDACTED]") {
			t.Errorf("%s: secret survived or marker missing:\n%s", name, body)
		}
	}
}

func TestSessionIngestRedactsTheTitleToo(t *testing.T) {
	db := newDB(t)
	ingest(t, db, nil, hook(t, map[string]any{"compact_summary": "1. Primary Request and Intent:\n   rotate ghp_" + strings.Repeat("A1b2", 6) + " today\n"}))
	if r := oneSession(t, db); strings.Contains(r.Title, "A1b2A1b2") || !strings.Contains(r.Title, "[REDACTED]") {
		t.Errorf("title = %q", r.Title)
	}
}

func TestPrivateBodyKeepsItsMarkerOnSave(t *testing.T) {
	db := newDB(t)
	run(t, db, "", "save", "--title", "T", "--body", "before <private>hidden</private> after")
	if body := oneSession(t, db).Body; body != "before [REDACTED] after" {
		t.Errorf("body = %q", body)
	}
}

func TestSaveWithoutRedactionExtrasLeavesSecretsAsToday(t *testing.T) {
	db := newDB(t)
	run(t, db, "", "save", "--title", "T", "--body", "API_KEY=abcd1234efgh")
	if body := oneSession(t, db).Body; body != "API_KEY=abcd1234efgh" {
		t.Errorf("save path changed: body = %q", body)
	}
}

func TestIngestAndDeferredSaveLeaveNoVector(t *testing.T) {
	var batches []int
	srv := embedServer(t, &batches)
	on := map[string]string{"MEMORYCARD_EMBED": "on", "MEMORYCARD_OLLAMA_URL": srv.URL}

	db := newDB(t)
	if code, out, errOut := ingest(t, db, on, hook(t, nil)); code != 0 || out != "" || errOut != "" {
		t.Fatalf("ingest: code=%d out=%q err=%q", code, out, errOut)
	}
	if code, out, _ := runEnv(t, db, on, "", "save", "--defer-embed", "--title", "Deferred", "--body", "later"); code != 0 || !strings.Contains(out, "created #2") {
		t.Fatalf("save --defer-embed: code=%d out=%q", code, out)
	}
	if len(batches) != 0 {
		t.Errorf("embedder called %d times, want none", len(batches))
	}
	if _, out, _ := runEnv(t, db, on, "", "stats"); !strings.Contains(out, "without embedding: 2") {
		t.Errorf("stats = %q, want without embedding: 2", out)
	}
	if code, out, _ := runEnv(t, db, on, "", "embed"); code != 0 || out != "embedded 2\n" {
		t.Errorf("embed: code=%d out=%q", code, out)
	}
	if _, out, _ := runEnv(t, db, on, "", "stats"); !strings.Contains(out, "without embedding: 0") {
		t.Errorf("stats after embed = %q", out)
	}
}

func TestSaveWithoutDeferFlagStillEmbeds(t *testing.T) {
	var batches []int
	srv := embedServer(t, &batches)
	on := map[string]string{"MEMORYCARD_EMBED": "on", "MEMORYCARD_OLLAMA_URL": srv.URL}
	db := newDB(t)
	runEnv(t, db, on, "", "save", "--title", "Now", "--body", "embedded right away")
	if len(batches) == 0 {
		t.Errorf("plain save did not call the embedder")
	}
	if _, out, _ := runEnv(t, db, on, "", "stats"); !strings.Contains(out, "without embedding: 0") {
		t.Errorf("stats = %q", out)
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestSessionIngestResolvesAWorktreeToItsMainCheckout(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "mainproj")
	tree := filepath.Join(root, "feature-wt")
	if err := os.Mkdir(main, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, main, "init", "-q")
	gitIn(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	gitIn(t, main, "worktree", "add", "-q", tree, "-b", "feature")
	sub := filepath.Join(tree, "pkg")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{tree, sub} {
		db := newDB(t)
		ingest(t, db, map[string]string{"MEMORYCARD_PROJECT": ""}, hook(t, map[string]any{"cwd": cwd}))
		if r := oneSession(t, db); r.Project != "mainproj" {
			t.Errorf("cwd %s: project = %q, want mainproj", cwd, r.Project)
		}
	}
}

func TestSessionIngestMissingCwdFallsBackToItsName(t *testing.T) {
	db := newDB(t)
	gone := filepath.Join(t.TempDir(), "gone-dir")
	ingest(t, db, map[string]string{"MEMORYCARD_PROJECT": ""}, hook(t, map[string]any{"cwd": gone}))
	if r := oneSession(t, db); r.Project != "gone-dir" {
		t.Errorf("project = %q, want gone-dir", r.Project)
	}
}
