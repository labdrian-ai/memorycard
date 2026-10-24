package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExplicitWins(t *testing.T) {
	dir := mkdir(t, t.TempDir(), "repo")
	if got := Resolve(dir, "chosen"); got != "chosen" {
		t.Errorf("got %q, want chosen", got)
	}
}

func TestConfigFileInAncestor(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".memorycard"), []byte("  shared-name \nignored\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deep := mkdir(t, root, "a", "b")
	if got := Resolve(deep, ""); got != "shared-name" {
		t.Errorf("got %q, want shared-name", got)
	}
}

func TestGitRemoteNameBeatsDirectoryName(t *testing.T) {
	dir := mkdir(t, t.TempDir(), "checkout-dir")
	git(t, dir, "init", "-q")
	git(t, dir, "remote", "add", "origin", "git@github.com:acme/real-name.git")
	sub := mkdir(t, dir, "pkg", "inner")
	for _, d := range []string{dir, sub} {
		if got := Resolve(d, ""); got != "real-name" {
			t.Errorf("from %s: got %q, want real-name", d, got)
		}
	}
}

func TestGitWorktreeResolvesLikeTheMainCheckout(t *testing.T) {
	main := mkdir(t, t.TempDir(), "main-dir")
	git(t, main, "init", "-q")
	git(t, main, "remote", "add", "origin", "https://github.com/acme/real-name")
	git(t, main, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "x")
	wt := filepath.Join(t.TempDir(), "other-dir")
	git(t, main, "worktree", "add", "-q", wt)
	if got := Resolve(wt, ""); got != "real-name" {
		t.Errorf("got %q, want real-name", got)
	}
}

func TestGitWithoutRemoteUsesRepoRootName(t *testing.T) {
	dir := mkdir(t, t.TempDir(), "root-name")
	git(t, dir, "init", "-q")
	sub := mkdir(t, dir, "deep")
	if got := Resolve(sub, ""); got != "root-name" {
		t.Errorf("got %q, want root-name", got)
	}
}

func TestPlainDirectoryFallsBackToItsName(t *testing.T) {
	dir := mkdir(t, t.TempDir(), "plain")
	if got := Resolve(dir, ""); got != "plain" {
		t.Errorf("got %q, want plain", got)
	}
}
