package mcpserver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SourceDir is the repository the binary was built from. Builds set it with
// -ldflags "-X ...mcpserver.SourceDir=$PWD" so the update notice can list the
// commits a newer install brings; when empty the notice names only versions.
var SourceDir = ""

// maxNoticeChanges caps the commit subjects a notice lists, newest kept, since
// the notice lands in the agent's context.
const maxNoticeChanges = 8

// updateNotice notices that the binary this server was started from has been
// replaced by a newer build. A running MCP server keeps the old code until the
// client reconnects, so the next tool result says so, once per new version.
type updateNotice struct {
	exe     string // path of the installed binary
	running string // version of this process
	source  string // repository for the change list, or ""

	mu   sync.Mutex
	seen os.FileInfo // the binary as last examined
	told string      // the newest version already announced
}

func newUpdateNotice(exe, running, source string) *updateNotice {
	n := &updateNotice{exe: exe, running: running, source: source, told: running}
	n.seen, _ = os.Stat(exe)
	return n
}

// defaultUpdateNotice watches the executable of this process, or nothing when
// its path cannot be found.
func defaultUpdateNotice() *updateNotice {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	return newUpdateNotice(exe, Version, SourceDir)
}

// check returns the notice to append to a tool result, or "". It runs the
// installed binary only when its file changed since the last check.
func (n *updateNotice) check(ctx context.Context) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	cur, err := os.Stat(n.exe)
	if err != nil || (n.seen != nil && os.SameFile(n.seen, cur) && cur.ModTime().Equal(n.seen.ModTime()) && cur.Size() == n.seen.Size()) {
		return ""
	}
	n.seen = cur
	installed := n.installedVersion(ctx)
	if installed == "" || installed == n.told {
		return ""
	}
	n.told = installed
	msg := fmt.Sprintf("memorycard: a newer binary is installed (%s → %s); this server still runs the old code.", n.running, installed)
	if changes := n.changes(ctx, installed); len(changes) > 0 {
		more := ""
		if len(changes) > maxNoticeChanges {
			more = fmt.Sprintf("; +%d more", len(changes)-maxNoticeChanges)
			changes = changes[len(changes)-maxNoticeChanges:]
		}
		msg += "\nChanges: " + strings.Join(changes, "; ") + more
	}
	return msg + "\nReconnect memorycard with /mcp to use it."
}

func (n *updateNotice) installedVersion(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, n.exe, "version").Output()
	if err != nil {
		return ""
	}
	v, ok := strings.CutPrefix(strings.TrimSpace(string(out)), "memorycard ")
	if !ok {
		return ""
	}
	return v
}

// changes lists the commit subjects between the running and the installed
// build, leaving out merges and feature-document bookkeeping.
func (n *updateNotice) changes(ctx context.Context, installed string) []string {
	from, to := commitOf(n.running), commitOf(installed)
	if n.source == "" || from == "" || to == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", n.source, "log", "--no-merges", "--reverse", "--format=%s", from+".."+to).Output()
	if err != nil {
		return nil
	}
	var subjects []string
	for _, s := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if s != "" && !strings.HasPrefix(s, "docs(odd)") {
			subjects = append(subjects, s)
		}
	}
	return subjects
}

// commitOf returns the commit a version names after "+", as in
// 0.4.0-dev+1c0411e, or "".
func commitOf(version string) string {
	_, c, _ := strings.Cut(version, "+")
	return c
}

// middleware appends the notice to the result of a tool call.
func (n *updateNotice) middleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		res, err := next(ctx, method, req)
		if method != "tools/call" || err != nil {
			return res, err
		}
		if r, ok := res.(*mcp.CallToolResult); ok {
			if msg := n.check(ctx); msg != "" {
				r.Content = append(r.Content, &mcp.TextContent{Text: "\n\n" + msg})
			}
		}
		return res, err
	}
}
