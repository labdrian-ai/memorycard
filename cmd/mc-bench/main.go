// Command mc-bench measures memorycard retrieval on LongMemEval. Each question
// gets a fresh database built from its own haystack; vectors come through a
// content cache, so a rerun or a resumed run only embeds texts it never saw.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/labdrian-ai/memorycard/internal/bench"
	"github.com/labdrian-ai/memorycard/internal/embed"
)

var ks = []int{1, 3, 5, 10}

func main() { os.Exit(run()) }

func run() int {
	data := flag.String("data", "", "LongMemEval JSON file (longmemeval_s_cleaned.json)")
	gran := flag.String("granularity", "session", "what one memory holds: session or round")
	sample := flag.Int("sample", 0, "questions to run, picked with -seed (0 = all)")
	seed := flag.Int64("seed", 20261029, "sample seed")
	cachePath := flag.String("cache", "", "embedding cache file (required unless -lexical)")
	lexical := flag.Bool("lexical", false, "run without an embedder (BM25 baseline)")
	out := flag.String("out", "", "results file, one JSON line per question; questions already in it are skipped")
	flag.Parse()
	g := bench.Granularity(*gran)
	if *data == "" || *out == "" || (g != bench.Session && g != bench.Round) || (*cachePath == "" && !*lexical) {
		fmt.Fprintln(os.Stderr, "usage: mc-bench -data longmemeval_s_cleaned.json -out results.jsonl [-granularity session|round] [-sample 50] [-cache cache.db | -lexical]")
		return 2
	}

	qs, err := bench.Load(*data)
	if err != nil {
		return fail(err)
	}
	qs = bench.Sample(qs, *sample, *seed)

	var emb embed.Embedder
	var cache *bench.Cache
	if !*lexical {
		inner := embed.FromEnv(os.Getenv)
		if inner == nil {
			return fail(fmt.Errorf("the embedder is off (MEMORYCARD_EMBED=off); use -lexical for a BM25 run"))
		}
		if cache, err = bench.OpenCache(*cachePath, inner); err != nil {
			return fail(err)
		}
		defer cache.Close()
		emb = cache
	}

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
	start := time.Now()
	for i, q := range qs {
		if done[q.ID] {
			continue
		}
		dir, err := os.MkdirTemp("", "mc-bench-")
		if err != nil {
			return fail(err)
		}
		t := time.Now()
		r, err := bench.RunQuestion(ctx, q, g, emb, dir)
		os.RemoveAll(dir)
		if err != nil {
			return fail(err)
		}
		line, _ := json.Marshal(r)
		if _, err := f.Write(append(line, '\n')); err != nil {
			return fail(err)
		}
		results = append(results, r)
		progress := ""
		if cache != nil {
			h, m := cache.Stats()
			progress = fmt.Sprintf(" cache %d hits %d misses", h, m)
		}
		fmt.Fprintf(os.Stderr, "[%d/%d] %s ranks %v docs %d in %s (total %s)%s\n",
			i+1, len(qs), q.ID, r.Ranks, r.Docs, time.Since(t).Round(time.Second), time.Since(start).Round(time.Second), progress)
	}
	report(keep(results, qs), g)
	return 0
}

// keep returns the results of the selected questions, in their order.
func keep(results []bench.Result, qs []bench.Question) []bench.Result {
	by := map[string]bench.Result{}
	for _, r := range results {
		by[r.ID] = r
	}
	var out []bench.Result
	for _, q := range qs {
		if r, ok := by[q.ID]; ok {
			out = append(out, r)
		}
	}
	return out
}

func readResults(path string) ([]bench.Result, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []bench.Result
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var r bench.Result
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

func report(results []bench.Result, g bench.Granularity) {
	s := bench.Summarize(results, ks)
	var ms []float64
	modes := map[string]int{}
	for _, r := range results {
		if !r.Abstention {
			ms = append(ms, r.SearchMs)
			modes[r.Mode]++
		}
	}
	sort.Float64s(ms)
	fmt.Printf("granularity %s: %d scored, %d abstentions skipped, modes %v", g, s.Scored, s.Abstentions, modes)
	if len(ms) > 0 {
		fmt.Printf(", search p50 %.0f ms", ms[len(ms)/2])
	}
	fmt.Println()
	fmt.Printf("%-28s %5s", "type", "n")
	for _, k := range ks {
		fmt.Printf("  any@%-2d all@%-2d", k, k)
	}
	fmt.Println()
	row := func(name string, x bench.Summary) {
		fmt.Printf("%-28s %5d", name, x.Scored)
		for _, k := range ks {
			fmt.Printf("  %5.1f%% %5.1f%%", 100*x.Any[k], 100*x.All[k])
		}
		fmt.Println()
	}
	types := make([]string, 0, len(s.ByType))
	for t := range s.ByType {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		if s.ByType[t].Scored > 0 {
			row(t, *s.ByType[t])
		}
	}
	row("all", s)
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "error:", err)
	return 1
}
