package mcpserver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/labdrian-ai/memorycard/internal/memory"
	"github.com/labdrian-ai/memorycard/internal/store"
)

// fakeBinary writes an executable that prints `memorycard <version>` for the
// version command, as the installed binary does.
func fakeBinary(t *testing.T, path, version string, mtime time.Time) {
	t.Helper()
	script := "#!/bin/sh\necho 'memorycard " + version + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// gitRepo makes a repository with one commit per subject and returns the
// directory and the short hash of every commit, oldest first.
func gitRepo(t *testing.T, subjects ...string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	var hashes []string
	for _, s := range subjects {
		git("commit", "-q", "--allow-empty", "-m", s)
		hashes = append(hashes, git("rev-parse", "--short", "HEAD"))
	}
	return dir, hashes
}

func connectWith(t *testing.T, n *updateNotice) *mcp.ClientSession {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := newServer(memory.New(db), "p", n).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestAToolCallSaysOnceThatANewerBinaryIsInstalledAndWhatItBrings(t *testing.T) {
	src, h := gitRepo(t, "feat: base", "feat(search): rank summaries", "docs(odd): log T9", "fix(store): close rows")
	exe := filepath.Join(t.TempDir(), "memorycard")
	start := time.Now().Add(-time.Hour)
	fakeBinary(t, exe, "0.4.0-dev+"+h[0], start)
	cs := connectWith(t, newUpdateNotice(exe, "0.4.0-dev+"+h[0], src))

	if out, _ := call(t, cs, "mem_context", nil); strings.Contains(out, "newer binary") {
		t.Fatalf("notice before any install:\n%s", out)
	}

	fakeBinary(t, exe, "0.4.0-dev+"+h[3], start.Add(time.Minute))
	out, isErr := call(t, cs, "mem_context", nil)
	if isErr {
		t.Fatalf("tool error: %s", out)
	}
	for _, want := range []string{
		"a newer binary is installed (0.4.0-dev+" + h[0] + " → 0.4.0-dev+" + h[3] + ")",
		"feat(search): rank summaries", "fix(store): close rows", "/mcp",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("notice lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "docs(odd)") {
		t.Errorf("notice lists a docs(odd) commit:\n%s", out)
	}

	if out, _ := call(t, cs, "mem_context", nil); strings.Contains(out, "newer binary") {
		t.Fatalf("notice repeated for the same version:\n%s", out)
	}
}

func TestTheNoticeStillNamesTheVersionsWithoutTheSourceRepository(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "memorycard")
	start := time.Now().Add(-time.Hour)
	fakeBinary(t, exe, "0.4.0-dev+aaaaaaa", start)
	cs := connectWith(t, newUpdateNotice(exe, "0.4.0-dev+aaaaaaa", ""))

	fakeBinary(t, exe, "0.4.0-dev+bbbbbbb", start.Add(time.Minute))
	out, _ := call(t, cs, "mem_search", map[string]any{"query": "x"})
	if !strings.Contains(out, "a newer binary is installed (0.4.0-dev+aaaaaaa → 0.4.0-dev+bbbbbbb)") || strings.Contains(out, "Changes:") {
		t.Fatalf("want the versions and no change list:\n%s", out)
	}
}

func TestTheNoticeListsOnlyTheNewestChanges(t *testing.T) {
	subjects := []string{"feat: base"}
	for i := 1; i <= 10; i++ {
		subjects = append(subjects, fmt.Sprintf("feat: change %d", i))
	}
	src, h := gitRepo(t, subjects...)
	exe := filepath.Join(t.TempDir(), "memorycard")
	start := time.Now().Add(-time.Hour)
	fakeBinary(t, exe, "0.4.0-dev+"+h[0], start)
	cs := connectWith(t, newUpdateNotice(exe, "0.4.0-dev+"+h[0], src))

	fakeBinary(t, exe, "0.4.0-dev+"+h[10], start.Add(time.Minute))
	out, _ := call(t, cs, "mem_context", nil)
	if !strings.Contains(out, "Changes: feat: change 3; ") || !strings.Contains(out, "feat: change 10; +2 more") || strings.Contains(out, "change 2;") {
		t.Fatalf("want changes 3-10 and +2 more:\n%s", out)
	}
}

func TestReinstallingTheSameVersionSaysNothing(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "memorycard")
	start := time.Now().Add(-time.Hour)
	fakeBinary(t, exe, "0.4.0-dev+aaaaaaa", start)
	cs := connectWith(t, newUpdateNotice(exe, "0.4.0-dev+aaaaaaa", ""))

	fakeBinary(t, exe, "0.4.0-dev+aaaaaaa", start.Add(time.Minute))
	if out, _ := call(t, cs, "mem_context", nil); strings.Contains(out, "newer binary") {
		t.Fatalf("notice for the same version:\n%s", out)
	}
}
