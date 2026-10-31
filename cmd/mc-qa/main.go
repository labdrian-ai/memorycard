// Command mc-qa measures memorycard end to end on LongMemEval: each question
// gets a fresh database built from its own haystack, its top sessions go to a
// reader model and a judge model scores the answer with the official
// LongMemEval prompts. Both models run through the Codex CLI.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/labdrian-ai/memorycard/internal/bench"
	"github.com/labdrian-ai/memorycard/internal/embed"
)

func main() { os.Exit(run()) }

func run() int {
	data := flag.String("data", "", "LongMemEval JSON file (longmemeval_s_cleaned.json)")
	sample := flag.Int("sample", 0, "questions to run, picked with -seed (0 = all)")
	seed := flag.Int64("seed", 20261029, "sample seed")
	cachePath := flag.String("cache", "", "embedding cache file")
	dense := flag.Bool("dense", false, "rank by meaning only instead of the default hybrid search")
	k := flag.Int("k", 5, "sessions given to the reader")
	model := flag.String("model", "gpt-6.1-sol", "reader and judge model, run through codex exec")
	out := flag.String("out", "", "results file, one JSON line per question; questions already in it are skipped")
	flag.Parse()
	if *data == "" || *out == "" || *cachePath == "" || *k < 1 {
		fmt.Fprintln(os.Stderr, "usage: mc-qa -data longmemeval_s_cleaned.json -cache cache.db -out qa.jsonl [-sample 50] [-k 5] [-model gpt-6.1-sol] [-dense]")
		return 2
	}

	qs, err := bench.Load(*data)
	if err != nil {
		return fail(err)
	}
	qs = bench.Sample(qs, *sample, *seed)

	inner := embed.FromEnv(os.Getenv)
	if inner == nil {
		return fail(fmt.Errorf("the embedder is off (MEMORYCARD_EMBED=off)"))
	}
	cache, err := bench.OpenCache(*cachePath, inner)
	if err != nil {
		return fail(err)
	}
	defer cache.Close()

	work, err := os.MkdirTemp("", "mc-qa-codex-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(work)
	codex := codexModel{model: *model, dir: work}

	results, err := readResults(*out)
	if err != nil {
		return fail(err)
	}
	done := map[string]bool{}
	for _, r := range results {
		done[r.ID] = true
	}
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fail(err)
	}
	defer f.Close()

	ctx := context.Background()
	setup := bench.Setup{Granularity: bench.Session, Embedder: cache, Hybrid: !*dense}
	start := time.Now()
	for i, q := range qs {
		if done[q.ID] {
			continue
		}
		dir, err := os.MkdirTemp("", "mc-qa-")
		if err != nil {
			return fail(err)
		}
		t := time.Now()
		r, err := bench.RunQA(ctx, q, setup, dir, bench.QASetup{K: *k, Reader: codex, Judge: codex})
		os.RemoveAll(dir)
		if err != nil {
			return fail(err)
		}
		line, _ := json.Marshal(r)
		if _, err := f.Write(append(line, '\n')); err != nil {
			return fail(err)
		}
		results = append(results, r)
		fmt.Fprintf(os.Stderr, "[%d/%d] %s correct %v ranks %v in %s (total %s)\n",
			i+1, len(qs), q.ID, r.Correct, r.Ranks, time.Since(t).Round(time.Second), time.Since(start).Round(time.Second))
	}
	report(keep(results, qs), *model, *k)
	return 0
}

// codexModel answers a prompt with one non-interactive codex run, in an empty
// directory, read-only, without the user's config, rules or session history.
type codexModel struct{ model, dir string }

func (c codexModel) Complete(ctx context.Context, prompt string) (string, error) {
	last := filepath.Join(c.dir, "last.txt")
	os.Remove(last)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codex", "exec", "-m", c.model, "--ephemeral", "--skip-git-repo-check",
		"--ignore-user-config", "--ignore-rules", "-s", "read-only", "-C", c.dir, "-o", last, "-")
	cmd.Stdin = strings.NewReader(prompt)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stderr, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("codex: %w: %s", err, tail(stderr.String(), 500))
	}
	b, err := os.ReadFile(last)
	if err != nil {
		return "", fmt.Errorf("codex wrote no answer: %w", err)
	}
	return string(b), nil
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// keep returns the results of the selected questions, in their order.
func keep(results []bench.QAResult, qs []bench.Question) []bench.QAResult {
	by := map[string]bench.QAResult{}
	for _, r := range results {
		by[r.ID] = r
	}
	var out []bench.QAResult
	for _, q := range qs {
		if r, ok := by[q.ID]; ok {
			out = append(out, r)
		}
	}
	return out
}

func readResults(path string) ([]bench.QAResult, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []bench.QAResult
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<22), 1<<22)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var r bench.QAResult
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

func report(results []bench.QAResult, model string, k int) {
	s := bench.SummarizeQA(results)
	fmt.Printf("model %s, top %d sessions: %d questions, accuracy %.1f%%, abstention %d at %.1f%%\n",
		model, k, s.N, 100*s.Accuracy, s.Abstention.N, 100*s.Abstention.Accuracy)
	types := make([]string, 0, len(s.ByType))
	for t := range s.ByType {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		fmt.Printf("%-28s %5d  %5.1f%%\n", t, s.ByType[t].N, 100*s.ByType[t].Accuracy)
	}
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "error:", err)
	return 1
}
