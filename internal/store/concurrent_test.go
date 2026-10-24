package store

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

const helperEnv = "MEMORYCARD_STORE_HELPER_DB"

// TestHelperWriter is not a test: it is the body of one "agent" process
// started by TestConcurrentProcesses.
func TestHelperWriter(t *testing.T) {
	path := os.Getenv(helperEnv)
	if path == "" {
		t.Skip("helper process only")
	}
	agent := os.Getenv("MEMORYCARD_STORE_HELPER_ID")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < 15; i++ {
		if _, err := db.SQL().Exec(
			`insert into memories(project,title,body,content_hash,created_at,updated_at) values('p',?,?,?,'x','x')`,
			fmt.Sprintf("a%s-%d", agent, i), "b", fmt.Sprintf("h%s-%d", agent, i)); err != nil {
			t.Fatal(err)
		}
	}
}

// Several agents start at once on a brand-new file, each as its own process
// (that is how Claude Code, Codex or OpenCode launch the MCP server); none of
// them may fail with "database is locked" or a half-applied migration.
func TestConcurrentProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	const agents = 6
	cmds := make([]*exec.Cmd, agents)
	outs := make([]*bytes.Buffer, agents)
	for a := range cmds {
		outs[a] = &bytes.Buffer{}
		cmds[a] = exec.Command(os.Args[0], "-test.run=^TestHelperWriter$")
		cmds[a].Env = append(os.Environ(), helperEnv+"="+path, "MEMORYCARD_STORE_HELPER_ID="+strconv.Itoa(a))
		cmds[a].Stdout, cmds[a].Stderr = outs[a], outs[a]
		if err := cmds[a].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for a, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Errorf("agent %d failed: %v\n%s", a, err, outs[a].String())
		}
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.SQL().QueryRow(`select count(*) from memories`).Scan(&n); err != nil || n != agents*15 {
		t.Errorf("rows=%d err=%v, want %d", n, err, agents*15)
	}
}
