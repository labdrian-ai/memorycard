package cli

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/labdrian-ai/memorycard/internal/project"
)

const (
	// transcriptMaxBytes caps how much of a transcript is scanned for paths.
	transcriptMaxBytes = 32 << 20
	// transcriptMaxPaths caps the distinct paths looked up on disk.
	transcriptMaxPaths = 5000
)

// absPath matches an absolute path that starts a token: after the start, a
// space, a quote, '=', '(' or ':'.
var absPath = regexp.MustCompile(`(?:^|[\s"'=(:])(/[\w.@+\-]+(?:/[\w.@+\-]+)*)`)

// captureProject returns the project for captured memories. A pinned
// MEMORYCARD_PROJECT always wins; otherwise the repository the transcript
// names most, if it is named more often than the working directory's; else
// the working directory's project. A missing or unreadable transcript is
// ignored, since capture runs from a hook that must never fail.
func (a *app) captureProject(transcript string) string {
	cwdProject := a.project()
	if transcript == "" || strings.TrimSpace(a.getenv("MEMORYCARD_PROJECT")) != "" {
		return cwdProject
	}
	counts := repoMentions(transcript, a.getenv("HOME"))
	wd, err := os.Getwd()
	if err != nil {
		return cwdProject
	}
	cwdRoot := gitRoot(wd, map[string]string{})
	best, n := "", 0
	for root, c := range counts {
		if c > n || (c == n && root < best) {
			best, n = root, c
		}
	}
	if best == "" || best == cwdRoot || n <= counts[cwdRoot] {
		return cwdProject
	}
	return project.Resolve(best, "")
}

// repoMentions counts, per git repository root, the absolute paths in the
// transcript that lie inside it. The home directory and / never count as a
// repository, since a dotfiles repo there would contain every path.
func repoMentions(transcript, home string) map[string]int {
	f, err := os.Open(transcript)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, transcriptMaxBytes))
	if err != nil {
		return nil
	}
	perPath := map[string]int{}
	for _, m := range absPath.FindAllSubmatch(b, -1) {
		p := filepath.Clean(string(m[1]))
		if _, seen := perPath[p]; seen || len(perPath) < transcriptMaxPaths {
			perPath[p]++
		}
	}
	cache := map[string]string{}
	counts := map[string]int{}
	for p, c := range perPath {
		if root := gitRoot(p, cache); root != "" && root != "/" && root != filepath.Clean(home) {
			counts[root] += c
		}
	}
	return counts
}

// gitRoot returns the nearest directory at or above path that holds a .git
// entry, or "". Path need not exist: the search starts at its nearest
// existing ancestor. cache maps directories already resolved to their root.
func gitRoot(path string, cache map[string]string) string {
	var walked []string
	root := ""
	for d := path; ; d = filepath.Dir(d) {
		if r, ok := cache[d]; ok {
			root = r
			break
		}
		walked = append(walked, d)
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			root = d
			break
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	for _, d := range walked {
		cache[d] = root
	}
	return root
}
