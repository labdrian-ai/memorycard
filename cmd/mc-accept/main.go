// Command mc-accept runs the v0 acceptance checks (S8) against a memorycard
// database imported from Engram, using gold question sets kept outside the
// repository. It exits 1 when a dense-mode threshold is missed.
//
// By default it measures as the lab index did: superseded and archived
// memories compete and every project is searched. To measure what an agent
// sees, run it with -include-old=false -project @target -hybrid: only active
// memories, searched inside the project of each question's answer, with the
// product's default (hybrid) ranking.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/labdrian-ai/memorycard/internal/accept"
	"github.com/labdrian-ai/memorycard/internal/embed"
	"github.com/labdrian-ai/memorycard/internal/memory"
	"github.com/labdrian-ai/memorycard/internal/store"
)

const usage = "usage: mc-accept -db memory.db {-gold dir[,dir] | -digest project[,project]} [-include-old=false] [-project @target|name] [-similarity 0.97] [-k 10]"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv)) }

func run(args []string, out, errOut io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	fs.SetOutput(errOut)
	dbPath := fs.String("db", "", "memorycard database imported from Engram")
	golds := fs.String("gold", "", "comma-separated gold directories (queries.tsv [+ relevant.tsv] [+ outdated.tsv]); keys are Engram ids or mc:<memorycard id>")
	includeOld := fs.Bool("include-old", true, "let superseded and archived memories compete, as the lab index did (false = active only, what agents see)")
	project := fs.String("project", "", "search scope: empty = all projects (default), "+accept.ProjectTarget+" = the project of each question's answer, or a project name")
	digest := fs.String("digest", "", "comma-separated projects: report the share of their memorycard context lines that are session summaries, further parts of a listed document or cut titles")
	sim := fs.Float64("similarity", 0, "also accept memories with cosine >= this to the target (0 = off)")
	k := fs.Int("k", 10, "results examined per query")
	negatives := fs.String("negatives", "", "file with one off-topic question per line: prints their top-score distribution")
	neighbors := fs.Int("neighbors", 0, "if > 0, print the nearest-neighbour cosine distribution of every Nth memory")
	misses := fs.Bool("misses", false, "list the dense questions whose answer is not ranked first")
	hybrid := fs.Bool("hybrid", false, "fuse the dense and lexical rankings (top score is then a fusion score, not a cosine)")
	if fs.Parse(args) != nil {
		return 2
	}
	if *dbPath == "" || (*golds == "" && *digest == "") {
		fmt.Fprintln(errOut, usage)
		return 2
	}
	var digestProjects []string
	if *digest != "" {
		for _, p := range strings.Split(*digest, ",") {
			if p = strings.TrimSpace(p); p == "" {
				fmt.Fprintln(errOut, "-digest: empty project name")
				return 2
			}
			digestProjects = append(digestProjects, p)
		}
	}
	db, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintln(errOut, "error:", err)
		return 1
	}
	defer db.Close()

	ctx := context.Background()
	failed := false
	dense, lexical := memory.New(db), memory.New(db)
	if *golds != "" {
		emb := embed.FromEnv(getenv)
		if emb == nil {
			fmt.Fprintln(errOut, "error: acceptance needs the embedder (unset MEMORYCARD_EMBED=off)")
			return 1
		}
		dense.SetEmbedder(emb)
		dense.SetHybrid(*hybrid)
		opts := accept.Options{K: *k, IncludeOld: *includeOld, SimilarityAccept: *sim, Project: *project}
		for _, dir := range strings.Split(*golds, ",") {
			ok, err := runGold(ctx, out, dense, lexical, db, dir, opts, *misses, *negatives != "" || *neighbors > 0)
			if err != nil {
				fmt.Fprintln(errOut, "error:", err)
				return 1
			}
			failed = failed || !ok
		}
	}
	if *negatives != "" {
		raw, err := os.ReadFile(*negatives)
		if err != nil {
			fmt.Fprintln(errOut, "error:", err)
			return 1
		}
		var tops []float64
		for _, line := range strings.Split(string(raw), "\n") {
			if line = strings.TrimSpace(line); line == "" {
				continue
			}
			res, err := dense.Search(ctx, memory.SearchInput{Query: line, K: 1})
			if err != nil {
				fmt.Fprintln(errOut, "error:", err)
				return 1
			}
			if len(res.Hits) > 0 {
				tops = append(tops, res.Hits[0].Score)
			}
		}
		fmt.Fprintln(out, "== off-topic questions, top cosine:", spread(tops))
	}
	if *neighbors > 0 {
		cos, err := accept.NeighborCosines(ctx, db, *neighbors)
		if err != nil {
			fmt.Fprintln(errOut, "error:", err)
			return 1
		}
		fmt.Fprintf(out, "== nearest distinct neighbour of %d memories, cosine: %s\n", len(cos), spread(cos))
		over := 0
		for _, c := range cos {
			if c >= memory.DuplicateHintCosine {
				over++
			}
		}
		fmt.Fprintf(out, "   at or above the duplicate hint (%.2f): %d\n", memory.DuplicateHintCosine, over)
	}
	if len(digestProjects) > 0 {
		cs, err := accept.DigestNoise(ctx, lexical, digestProjects)
		if err != nil {
			fmt.Fprintln(errOut, "error:", err)
			return 1
		}
		fmt.Fprintln(out, "== digest noise (session summaries, further parts, cut titles)")
		for _, c := range append(cs, accept.Total(cs)) {
			fmt.Fprintf(out, "  %-22s lines %3d  summaries %d  parts %d  cut %d  noisy %d (%.0f%%)\n",
				c.Project, c.Lines, c.Summaries, c.Parts, c.Truncated, c.Noisy, c.Share()*100)
		}
	}
	if err := integrity(ctx, out, db); err != nil {
		fmt.Fprintln(out, "  FAIL:", err)
		failed = true
	}
	if failed {
		fmt.Fprintln(out, "RESULT: FAIL")
		return 1
	}
	fmt.Fprintln(out, "RESULT: PASS")
	return 0
}

// runGold runs one gold directory in dense and lexical mode and prints the
// summaries; it reports false when a threshold is missed.
func runGold(ctx context.Context, out io.Writer, dense, lexical *memory.Service, db *store.DB, dir string, opts accept.Options, misses, spreads bool) (bool, error) {
	ok := true
	g, err := accept.LoadGold(dir)
	if err != nil {
		return false, err
	}
	scope := "" // the default (all projects) keeps the header it always had
	if opts.Project != "" {
		scope = " project=" + opts.Project
	}
	fmt.Fprintf(out, "== %s: %d questions, include-old=%v%s similarity=%v\n", dir, len(g.Queries), opts.IncludeOld, scope, opts.SimilarityAccept)
	rs, err := accept.Run(ctx, dense, db, g, opts)
	if err != nil {
		return false, err
	}
	s := accept.Summarize(rs)
	show(out, "dense", s, !opts.IncludeOld)
	if test := onlySplit(rs, "test"); len(test) > 0 && len(test) < len(rs) {
		show(out, "dense/test", accept.Summarize(test), !opts.IncludeOld)
	}
	if spreads {
		var found, lost []float64
		for _, r := range rs {
			if r.Rank > 0 {
				found = append(found, r.Top)
			} else {
				lost = append(lost, r.Top)
			}
		}
		fmt.Fprintln(out, "  top cosine, answer in top 5:   ", spread(found))
		fmt.Fprintln(out, "  top cosine, answer missed:     ", spread(lost))
	}
	if misses {
		for _, r := range rs {
			if r.Rank != 1 {
				extra := ""
				if r.HasOutdated {
					extra = fmt.Sprintf("  outdated rank %d", r.OutdatedRank)
				}
				fmt.Fprintf(out, "  rank %d%s  id %s  [%s] %s\n", r.Rank, extra, r.ID, r.Lang, r.Text)
			}
		}
	}
	for _, f := range s.Check() {
		fmt.Fprintln(out, "  FAIL:", f)
		ok = false
	}

	lrs, err := accept.Run(ctx, lexical, db, g, opts)
	if err != nil {
		return false, err
	}
	ls := accept.Summarize(lrs)
	show(out, "lexical (no embedder)", ls, !opts.IncludeOld)
	if ls.Lexical != ls.N {
		fmt.Fprintf(out, "  FAIL: %d of %d fallback answers did not say they were lexical\n", ls.N-ls.Lexical, ls.N)
		ok = false
	}
	return ok, nil
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

func show(out io.Writer, name string, s accept.Summary, activeOnly bool) {
	fmt.Fprintf(out, "  %-22s hit@1 %3.0f%%  hit@3 %3.0f%%  hit@5 %3.0f%%  hit@10 %3.0f%%  MRR %.2f  tokens %.0f  p50 %v  non-empty %d/%d  es@5 %.0f%% en@5 %.0f%%\n",
		name, s.Hit1*100, s.Hit3*100, s.Hit5*100, s.Hit10*100, s.MRR, s.AvgTokens, s.P50.Round(1e6), s.NonEmpty, s.N,
		s.Hit5ByLang["es"]*100, s.Hit5ByLang["en"]*100)
	if s.WithOutdated > 0 {
		fmt.Fprintf(out, "  %-22s current above outdated %3.0f%%  outdated above current %3.0f%%  (%d questions)\n",
			"", s.CurrentFirst*100, s.OutdatedFirst*100, s.WithOutdated)
	}
	if activeOnly && s.Inactive > 0 { // unreachable answers matter only when old memories cannot compete
		fmt.Fprintf(out, "  %-22s %d questions answered only by superseded or archived memories\n", "", s.Inactive)
	}
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
func integrity(ctx context.Context, out io.Writer, db *store.DB) error {
	var largest, unmapped int
	if err := db.SQL().QueryRowContext(ctx, `select coalesce(max(length(cast(body as blob))), 0) from memories`).Scan(&largest); err != nil {
		return err
	}
	if err := db.SQL().QueryRowContext(ctx,
		`select count(*) from memories m where not exists (select 1 from import_map i where i.memory_id = m.id)`).Scan(&unmapped); err != nil {
		return err
	}
	fmt.Fprintf(out, "  import integrity: largest body %d bytes (cap %d), memories without import mapping %d\n", largest, memory.HardCapBytes, unmapped)
	if largest > memory.HardCapBytes {
		return fmt.Errorf("a body of %d bytes exceeds the hard cap", largest)
	}
	return nil
}
