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
	"github.com/labdrian-ai/memorycard/internal/mcpserver"
	"github.com/labdrian-ai/memorycard/internal/memory"
	"github.com/labdrian-ai/memorycard/internal/render"
	"github.com/labdrian-ai/memorycard/internal/store"
)

const usage = `usage: memorycard <command> [flags]

commands:
  mcp                      serve the MCP tools over stdio
  save   --title T --body B [--type X] [--topic K] [--scope S] [--supersedes ID]
                           save a memory (--body - reads stdin)
  search [--type X] [--k N] [--snippet] [--all] <query>
  get    <id>              print one memory with its history
  context                  print the project digest
  stats                    counts and embedding backlog
  embed  [--limit N]       embed memories that have no vector yet
  help

environment:
  MEMORYCARD_DB            database path (default ~/.memorycard/memory.db)
  MEMORYCARD_PROJECT       default project (default: current directory name)
  MEMORYCARD_EMBED         "off" disables the embedder (text search only)
  MEMORYCARD_OLLAMA_URL    ollama address (default http://localhost:11434)
  MEMORYCARD_EMBED_MODEL   embedding model (default bge-m3)
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
	case "mcp", "save", "search", "get", "context", "stats", "embed":
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
	case "search":
		return a.search(ctx, svc, rest)
	case "get":
		return a.get(ctx, svc, rest)
	case "context":
		return a.context(ctx, svc)
	case "stats":
		return a.stats(ctx, svc, emb)
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
	if p := a.getenv("MEMORYCARD_PROJECT"); p != "" {
		return p
	}
	if wd, err := os.Getwd(); err == nil {
		return filepath.Base(wd)
	}
	return ""
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
	var emb embed.Embedder
	if a.getenv("MEMORYCARD_EMBED") != "off" {
		model := a.getenv("MEMORYCARD_EMBED_MODEL")
		if model == "" {
			model = "bge-m3"
		}
		emb = embed.NewOllama(a.getenv("MEMORYCARD_OLLAMA_URL"), model)
		svc.SetEmbedder(emb)
	}
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
	var snippet bool
	fs.StringVar(&in.Type, "type", "", "type filter")
	fs.IntVar(&in.K, "k", 0, "results")
	fs.BoolVar(&snippet, "snippet", false, "include a snippet")
	fs.BoolVar(&in.IncludeOld, "all", false, "include superseded and archived")
	fs.StringVar(&in.Project, "project", in.Project, "project")
	if fs.Parse(args) != nil {
		return 2
	}
	in.Query = strings.Join(fs.Args(), " ")
	if snippet {
		in.Detail = memory.DetailSnippet
	}
	r, err := svc.Search(ctx, in)
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprint(a.out, render.Search(r))
	return 0
}

func (a *app) get(ctx context.Context, svc *memory.Service, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(a.err, "usage: memorycard get <id>")
		return 2
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintf(a.err, "id must be a number, got %q\n", args[0])
		return 2
	}
	m, err := svc.Get(ctx, id)
	if errors.Is(err, memory.ErrNotFound) {
		return a.fail(fmt.Errorf("memory #%d: %w", id, err))
	}
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprint(a.out, render.Memory(m))
	return 0
}

func (a *app) context(ctx context.Context, svc *memory.Service) int {
	d, err := svc.Context(ctx, a.project(), memory.ContextBudgetBytes)
	if err != nil {
		return a.fail(err)
	}
	if len(d.Entries) == 0 {
		fmt.Fprintln(a.out, "no memories yet")
		return 0
	}
	fmt.Fprint(a.out, d.Text())
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
	limit := fs.Int("limit", 100, "memories per run")
	if fs.Parse(args) != nil {
		return 2
	}
	n, err := svc.EmbedPending(ctx, *limit)
	fmt.Fprintf(a.out, "embedded %d\n", n)
	return a.fail(err)
}
