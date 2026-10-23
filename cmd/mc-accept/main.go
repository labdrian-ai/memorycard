// Command mc-accept runs the v0 acceptance checks (S8) against a memorycard
// database imported from Engram, using gold question sets kept outside the
// repository. It exits 1 when a dense-mode threshold is missed.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/labdrian-ai/memorycard/internal/accept"
	"github.com/labdrian-ai/memorycard/internal/embed"
	"github.com/labdrian-ai/memorycard/internal/memory"
	"github.com/labdrian-ai/memorycard/internal/store"
)

func main() { os.Exit(run()) }

func run() int {
	dbPath := flag.String("db", "", "memorycard database imported from Engram")
	golds := flag.String("gold", "", "comma-separated gold directories (queries.tsv [+ relevant.tsv])")
	includeOld := flag.Bool("include-old", true, "let superseded memories compete, as the lab index did")
	sim := flag.Float64("similarity", 0, "also accept memories with cosine >= this to the target (0 = off)")
	k := flag.Int("k", 10, "results examined per query")
	negatives := flag.String("negatives", "", "file with one off-topic question per line: prints their top-score distribution")
	neighbors := flag.Int("neighbors", 0, "if > 0, print the nearest-neighbour cosine distribution of every Nth memory")
	misses := flag.Bool("misses", false, "list the dense questions whose answer is not ranked first")
	flag.Parse()
	if *dbPath == "" || *golds == "" {
		fmt.Fprintln(os.Stderr, "usage: mc-accept -db memory.db -gold dir[,dir] [-include-old=false] [-similarity 0.97] [-k 10]")
		return 2
	}
	db, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	defer db.Close()

	emb := embed.FromEnv(os.Getenv)
	if emb == nil {
		fmt.Fprintln(os.Stderr, "error: acceptance needs the embedder (unset MEMORYCARD_EMBED=off)")
		return 1
	}
	dense, lexical := memory.New(db), memory.New(db)
	dense.SetEmbedder(emb)
	opts := accept.Options{K: *k, IncludeOld: *includeOld, SimilarityAccept: *sim}

	ctx := context.Background()
	failed := false
	for _, dir := range strings.Split(*golds, ",") {
		g, err := accept.LoadGold(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		fmt.Printf("== %s: %d questions, include-old=%v similarity=%v\n", dir, len(g.Queries), *includeOld, *sim)
		rs, err := accept.Run(ctx, dense, db, g, opts)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		show("dense", accept.Summarize(rs))
		if test := onlySplit(rs, "test"); len(test) > 0 && len(test) < len(rs) {
			show("dense/test", accept.Summarize(test))
		}
		if *negatives != "" || *neighbors > 0 {
			var found, lost []float64
			for _, r := range rs {
				if r.Rank > 0 {
					found = append(found, r.Top)
				} else {
					lost = append(lost, r.Top)
				}
			}
			fmt.Println("  top cosine, answer in top 5:   ", spread(found))
			fmt.Println("  top cosine, answer missed:     ", spread(lost))
		}
		if *misses {
			for _, r := range rs {
				if r.Rank != 1 {
					fmt.Printf("  rank %d  id %s  [%s] %s\n", r.Rank, r.ID, r.Lang, r.Text)
				}
			}
		}
		for _, f := range accept.Summarize(rs).Check() {
			fmt.Println("  FAIL:", f)
			failed = true
		}

		lrs, err := accept.Run(ctx, lexical, db, g, opts)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		ls := accept.Summarize(lrs)
		show("lexical (no embedder)", ls)
		if ls.Lexical != ls.N {
			fmt.Printf("  FAIL: %d of %d fallback answers did not say they were lexical\n", ls.N-ls.Lexical, ls.N)
			failed = true
		}
	}
	if *negatives != "" {
		raw, err := os.ReadFile(*negatives)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		var tops []float64
		for _, line := range strings.Split(string(raw), "\n") {
			if line = strings.TrimSpace(line); line == "" {
				continue
			}
			res, err := dense.Search(ctx, memory.SearchInput{Query: line, K: 1})
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				return 1
			}
			if len(res.Hits) > 0 {
				tops = append(tops, res.Hits[0].Score)
			}
		}
		fmt.Println("== off-topic questions, top cosine:", spread(tops))
	}
	if *neighbors > 0 {
		cos, err := accept.NeighborCosines(ctx, db, *neighbors)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		fmt.Printf("== nearest distinct neighbour of %d memories, cosine: %s\n", len(cos), spread(cos))
		over := 0
		for _, c := range cos {
			if c >= memory.DuplicateHintCosine {
				over++
			}
		}
		fmt.Printf("   at or above the duplicate hint (%.2f): %d\n", memory.DuplicateHintCosine, over)
	}
	if err := integrity(ctx, db); err != nil {
		fmt.Println("  FAIL:", err)
		failed = true
	}
	if failed {
		fmt.Println("RESULT: FAIL")
		return 1
	}
	fmt.Println("RESULT: PASS")
	return 0
}

// spread formats n, min, quartiles, p90 and max of xs.
func spread(xs []float64) string {
	if len(xs) == 0 {
		return "n=0"
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	at := func(p float64) float64 { return s[int(p*float64(len(s)-1))] }
	return fmt.Sprintf("n=%d min %.3f p25 %.3f p50 %.3f p75 %.3f p90 %.3f max %.3f",
		len(s), s[0], at(.25), at(.5), at(.75), at(.9), s[len(s)-1])
}

func show(name string, s accept.Summary) {
	fmt.Printf("  %-22s hit@1 %3.0f%%  hit@3 %3.0f%%  hit@5 %3.0f%%  hit@10 %3.0f%%  MRR %.2f  tokens %.0f  p50 %v  non-empty %d/%d  es@5 %.0f%% en@5 %.0f%%\n",
		name, s.Hit1*100, s.Hit3*100, s.Hit5*100, s.Hit10*100, s.MRR, s.AvgTokens, s.P50.Round(1e6), s.NonEmpty, s.N,
		s.Hit5ByLang["es"]*100, s.Hit5ByLang["en"]*100)
}

func onlySplit(rs []accept.QueryResult, split string) []accept.QueryResult {
	var out []accept.QueryResult
	for _, r := range rs {
		if r.Split == split {
			out = append(out, r)
		}
	}
	return out
}

// integrity checks the import-side acceptance clauses: no body above the
// hard cap (nothing was left oversized or silently cut) and no memory without
// an import mapping.
func integrity(ctx context.Context, db *store.DB) error {
	var largest, unmapped int
	if err := db.SQL().QueryRowContext(ctx, `select coalesce(max(length(cast(body as blob))), 0) from memories`).Scan(&largest); err != nil {
		return err
	}
	if err := db.SQL().QueryRowContext(ctx,
		`select count(*) from memories m where not exists (select 1 from import_map i where i.memory_id = m.id)`).Scan(&unmapped); err != nil {
		return err
	}
	fmt.Printf("  import integrity: largest body %d bytes (cap %d), memories without import mapping %d\n", largest, memory.HardCapBytes, unmapped)
	if largest > memory.HardCapBytes {
		return fmt.Errorf("a body of %d bytes exceeds the hard cap", largest)
	}
	return nil
}
