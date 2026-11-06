// Package cli implements the memorycard command line: the MCP server entry
// point plus the diagnostics and manual commands.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/labdrian-ai/memorycard/internal/embed"
	"github.com/labdrian-ai/memorycard/internal/importer"
	"github.com/labdrian-ai/memorycard/internal/mcpserver"
	"github.com/labdrian-ai/memorycard/internal/memory"
	"github.com/labdrian-ai/memorycard/internal/project"
	"github.com/labdrian-ai/memorycard/internal/render"
	"github.com/labdrian-ai/memorycard/internal/store"
)

const usage = `usage: memorycard <command> [flags]

commands:
  mcp                      serve the MCP tools over stdio
  save   --title T --body B [--type X] [--topic K] [--scope S] [--supersedes ID]
                           save a memory (--body - reads stdin)
  search [--type X] [--k N] [--titles] [--all] [--project P] <query>
                           find memories by meaning; --all includes superseded
                           and archived ones, --project "" searches every project
  get    [--history] <id>  print one memory; --history adds its earlier versions
  context [--project P] [--query Q | <query>]
                           print the project digest: pinned, up to 3 stale
                           decisions, then recent memories, one line per
                           document; session summaries and sdd/ odd/ project/
                           mirrors one line each; a query appends its top 5 hits
  capture                  read text on stdin and save each numbered item of its
                           "## Key Learnings" block as a memory (type learning);
                           items under 60 characters or with a probe marker are
                           skipped; no block or no items saves nothing;
                           --transcript FILE files them under the repository
                           the subagent's transcript names most
  project                print the project the other commands resolve
  stats                    counts and embedding backlog
  embed  [--limit N]       embed every memory that has no vector yet, N per batch;
                           long memories get one vector per chunk (windows of
                           1500 characters with 200 of overlap, each with the
                           title), and memories embedded before chunking get
                           their missing chunks
  import engram <path>     copy an Engram database in (read-only, resumable)
  version                  print the build version
  help

environment:
  MEMORYCARD_DB            database path (default ~/.memorycard/memory.db)
  MEMORYCARD_PROJECT       default project; otherwise a .memorycard file in the
                           directory or above, the git remote name, the git root
                           name, then the directory name
  MEMORYCARD_EMBED         "off" disables the embedder (text search only)
  MEMORYCARD_OLLAMA_URL    ollama address (default http://localhost:11434)
  MEMORYCARD_EMBED_MODEL   embedding model (default bge-m3)
  MEMORYCARD_SEARCH        "dense" ranks by meaning only (default "hybrid":
                           meaning boosted by the query's keywords)
`

type app struct {
	in     io.Reader
	out    io.Writer
	err    io.Writer
	getenv func(string) string
}

// Run executes one command and returns the process exit code: 0 success,
// 1 operation failed, 2 bad usage.
func Run(args []string, in io.Reader, out, errOut io.Writer, getenv func(string) string) int {
	a := &app{in: in, out: out, err: errOut, getenv: getenv}
	if len(args) == 0 {
		fmt.Fprint(errOut, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprint(out, usage)
		return 0
	case "version", "--version":
		fmt.Fprintf(out, "memorycard %s\n", mcpserver.Version)
		return 0
	case "project":
		fmt.Fprintln(out, a.project())
		return 0
	case "mcp", "save", "search", "get", "context", "stats", "embed", "import", "capture":
	default:
		fmt.Fprintf(errOut, "unknown command %q\n%s", cmd, usage)
		return 2
	}

	svc, emb, closeDB, err := a.open()
	if err != nil {
		fmt.Fprintln(errOut, "error:", err)
		return 1
	}
	defer closeDB()

	ctx := context.Background()
	switch cmd {
	case "mcp":
		return a.fail(mcpserver.New(svc, a.project()).Run(ctx, &mcp.StdioTransport{}))
	case "save":
		return a.save(ctx, svc, rest)
	case "capture":
		return a.capture(ctx, svc, rest)
	case "search":
		return a.search(ctx, svc, rest)
	case "get":
		return a.get(ctx, svc, rest)
	case "context":
		return a.context(ctx, svc, rest)
	case "stats":
		return a.stats(ctx, svc, emb)
	case "import":
		return a.importCmd(ctx, svc, rest)
	default: // embed
		return a.embed(ctx, svc, emb, rest)
	}
}

func (a *app) fail(err error) int {
	if err == nil {
		return 0
	}
	fmt.Fprintln(a.err, "error:", err)
	return 1
}

func (a *app) project() string {
	wd, err := os.Getwd()
	if err != nil {
		return a.getenv("MEMORYCARD_PROJECT")
	}
	return project.Resolve(wd, a.getenv("MEMORYCARD_PROJECT"))
}

func (a *app) dbPath() string {
	if p := a.getenv("MEMORYCARD_DB"); p != "" {
		return p
	}
	return filepath.Join(a.getenv("HOME"), ".memorycard", "memory.db")
}

// open returns the service, the embedder (nil when off) and a closer.
func (a *app) open() (*memory.Service, embed.Embedder, func(), error) {
	path := a.dbPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, nil, err
	}
	db, err := store.Open(path)
	if err != nil {
		return nil, nil, nil, err
	}
	svc := memory.New(db)
	emb := embed.FromEnv(a.getenv)
	if emb != nil {
		svc.SetEmbedder(emb)
	}
	svc.SetHybrid(a.getenv("MEMORYCARD_SEARCH") != "dense")
	return svc, emb, func() { db.Close() }, nil
}

func (a *app) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.err)
	return fs
}

func (a *app) save(ctx context.Context, svc *memory.Service, args []string) int {
	fs := a.flags("save")
	in := memory.SaveInput{Project: a.project()}
	fs.StringVar(&in.Title, "title", "", "title")
	fs.StringVar(&in.Body, "body", "", "body, or - for stdin")
	fs.StringVar(&in.Type, "type", "", "type")
	fs.StringVar(&in.TopicKey, "topic", "", "topic key")
	fs.StringVar(&in.Scope, "scope", "", "scope")
	fs.StringVar(&in.Project, "project", in.Project, "project")
	fs.Int64Var(&in.Supersedes, "supersedes", 0, "id this replaces")
	if fs.Parse(args) != nil {
		return 2
	}
	if in.Body == "-" {
		b, err := io.ReadAll(a.in)
		if err != nil {
			return a.fail(err)
		}
		in.Body = string(b)
	}
	r, err := svc.Save(ctx, in)
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprint(a.out, render.Save(r))
	return 0
}

func (a *app) search(ctx context.Context, svc *memory.Service, args []string) int {
	fs := a.flags("search")
	in := memory.SearchInput{Project: a.project()}
	var titles bool
	fs.StringVar(&in.Type, "type", "", "type filter")
	fs.IntVar(&in.K, "k", 0, "results")
	fs.BoolVar(&titles, "titles", false, "titles only, no excerpt")
	fs.BoolVar(&in.IncludeOld, "all", false, "include superseded and archived")
	fs.StringVar(&in.Project, "project", in.Project, "project")
	if fs.Parse(args) != nil {
		return 2
	}
	in.Query = strings.Join(fs.Args(), " ")
	if titles {
		in.Detail = memory.DetailTitle
	}
	r, err := svc.Search(ctx, in)
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprint(a.out, render.Search(r))
	return 0
}

func (a *app) get(ctx context.Context, svc *memory.Service, args []string) int {
	// Scanned by hand rather than with the flag package so --history may
	// follow the id and anything else, "-1" included, still reads as the id.
	history := false
	var rest []string
	for _, arg := range args {
		if arg == "--history" || arg == "-history" {
			history = true
			continue
		}
		rest = append(rest, arg)
	}
	if len(rest) != 1 {
		fmt.Fprintln(a.err, "usage: memorycard get [--history] <id>")
		return 2
	}
	id, err := strconv.ParseInt(rest[0], 10, 64)
	if err != nil {
		fmt.Fprintf(a.err, "id must be a number, got %q\n", rest[0])
		return 2
	}
	m, err := svc.Get(ctx, id)
	if errors.Is(err, memory.ErrNotFound) {
		return a.fail(fmt.Errorf("memory #%d: %w", id, err))
	}
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprint(a.out, render.Memory(m, history, "--history"))
	return 0
}

func (a *app) context(ctx context.Context, svc *memory.Service, args []string) int {
	fs := a.flags("context")
	proj := fs.String("project", a.project(), "project")
	query := fs.String("query", "", "task or question: append its top search hits")
	if fs.Parse(args) != nil {
		return 2
	}
	if *query == "" {
		*query = strings.Join(fs.Args(), " ")
	}
	text, err := mcpserver.DigestText(ctx, svc, *proj, *query)
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprint(a.out, text)
	return 0
}

func (a *app) stats(ctx context.Context, svc *memory.Service, emb embed.Embedder) int {
	st, err := svc.Stats(ctx)
	if err != nil {
		return a.fail(err)
	}
	model := "off"
	if emb != nil {
		model = emb.Model()
	}
	fmt.Fprintf(a.out, "db: %s\nmemories: %d\nactive: %d\nsuperseded: %d\narchived: %d\nwithout embedding: %d\nembedder: %s\n",
		a.dbPath(), st.Total, st.Active, st.Superseded, st.Archived, st.WithoutEmbedding, model)
	return 0
}

func (a *app) embed(ctx context.Context, svc *memory.Service, emb embed.Embedder, args []string) int {
	if emb == nil {
		return a.fail(errors.New("the embedder is off (MEMORYCARD_EMBED=off)"))
	}
	fs := a.flags("embed")
	limit := fs.Int("limit", 8, "memories per embedder request (large notes time out in bigger batches)")
	if fs.Parse(args) != nil {
		return 2
	}
	total := 0
	for {
		n, err := svc.EmbedPending(ctx, *limit)
		total += n
		if err != nil {
			fmt.Fprintf(a.out, "embedded %d\n", total)
			return a.fail(err)
		}
		if n == 0 {
			break
		}
		fmt.Fprintf(a.err, "  embedded %d so far\n", total)
	}
	fmt.Fprintf(a.out, "embedded %d\n", total)
	return 0
}

func (a *app) importCmd(ctx context.Context, svc *memory.Service, args []string) int {
	if len(args) != 2 || args[0] != "engram" {
		fmt.Fprintln(a.err, "usage: memorycard import engram <path-to-engram.db>")
		return 2
	}
	res, err := importer.FromEngram(ctx, svc, args[1], func(done, total int) {
		fmt.Fprintf(a.err, "  %d/%d\n", done, total)
	})
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprintf(a.out, "imported: %d\nalready imported: %d\nmerged duplicates: %d\nempty after redaction: %d\n"+
		"chunked: %d records into %d parts\nsuperseded links: %d (skipped %d)\n"+
		"skipped deleted: %d\narchived deleted: %d\nrelations not imported: %d\nunparsable timestamps: %d\n",
		res.Imported, res.AlreadyImported, res.Duplicates, res.Empty, res.Chunked, res.Parts,
		res.Linked, res.LinksSkipped, res.SkippedDeleted, res.ArchivedDeleted, res.UnusedRelations, res.BadTimestamps)
	st, err := svc.Stats(ctx)
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprintf(a.out, "without embedding: %d (run `memorycard embed` to index them)\n", st.WithoutEmbedding)
	return 0
}
