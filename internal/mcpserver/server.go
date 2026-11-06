// Package mcpserver exposes the memory service as six MCP tools. Their
// definitions are part of every agent's context, so descriptions stay short
// and results are plain text rather than structured output with a schema.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/labdrian-ai/memorycard/internal/memory"
	"github.com/labdrian-ai/memorycard/internal/project"
	"github.com/labdrian-ai/memorycard/internal/render"
)

type handlers struct {
	svc     *memory.Service
	project string // default project when a call does not name one
}

// New builds an MCP server over svc. defaultProject is used when a tool call
// omits project.
// A tool result also says, once per version, that a newer binary has been
// installed since the server started.
func New(svc *memory.Service, defaultProject string) *mcp.Server {
	return newServer(svc, defaultProject, defaultUpdateNotice())
}

func newServer(svc *memory.Service, defaultProject string, notice *updateNotice) *mcp.Server {
	h := &handlers{svc: svc, project: defaultProject}
	srv := mcp.NewServer(&mcp.Implementation{Name: "memorycard", Version: Version}, nil)
	if notice != nil {
		srv.AddReceivingMiddleware(notice.middleware)
	}
	// Clients decide whether to ask for approval from these hints: reads are
	// declared read-only, writes are declared non-destructive (nothing is
	// ever hard-deleted; old versions are kept and archiving is reversible).
	read := &mcp.ToolAnnotations{ReadOnlyHint: true}
	no := false
	write := &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no}
	mcp.AddTool(srv, &mcp.Tool{Name: "mem_save", Annotations: write,
		Description: "Save a memory. Same topic_key updates it in place, restoring it if archived; supersedes marks an older id replaced. A body over 64 KB is stored as numbered parts. The result names similar active memories to supersede if outdated."}, h.save)
	mcp.AddTool(srv, &mcp.Tool{Name: "mem_search", Annotations: read,
		Description: "Find memories by meaning. Returns titles with a short excerpt; detail=title drops the excerpt. Results are related memories: check that they hold the fact asked for."}, h.search)
	mcp.AddTool(srv, &mcp.Tool{Name: "mem_get", Annotations: read,
		Description: "Read one memory in full. Earlier versions only with history=true."}, h.get)
	mcp.AddTool(srv, &mcp.Tool{Name: "mem_update", Annotations: write,
		Description: "Edit a memory by id, or pin, archive or mark it reviewed."}, h.update)
	mcp.AddTool(srv, &mcp.Tool{Name: "mem_context", Annotations: read,
		Description: "Short digest of a project: pinned, stale, then recent memories, one line per document. query adds the top hits for a task."}, h.context)
	mcp.AddTool(srv, &mcp.Tool{Name: "mem_session", Annotations: write,
		Description: "Session bookkeeping: action start, end or summary."}, h.session)
	return srv
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func (h *handlers) proj(p string) string {
	if p != "" {
		return p
	}
	return h.project
}

// projIn is proj for a call that may name the caller's working directory:
// an explicit project wins, then the project resolved from cwd (or its
// alias directory) on this call, then the server default.
func (h *handlers) projIn(p, cwd, directory string) (string, error) {
	if p != "" {
		return p, nil
	}
	if cwd == "" {
		cwd = directory
	} else if directory != "" && filepath.Clean(directory) != filepath.Clean(cwd) {
		return "", fmt.Errorf("cwd %q and directory %q differ: pass one", cwd, directory)
	}
	if cwd == "" {
		return h.project, nil
	}
	if !filepath.IsAbs(cwd) {
		return "", fmt.Errorf("cwd %q must be an absolute path", cwd)
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("cwd %q is not an existing directory", cwd)
	}
	return project.Resolve(cwd, ""), nil
}

type saveArgs struct {
	Title      string `json:"title" jsonschema:"short title"`
	Body       string `json:"body" jsonschema:"what, why, where, learned"`
	Type       string `json:"type,omitempty" jsonschema:"decision, bugfix, discovery, pattern..."`
	TopicKey   string `json:"topic_key,omitempty" jsonschema:"stable key such as architecture/auth"`
	Scope      string `json:"scope,omitempty" jsonschema:"project (default), personal or global"`
	Supersedes int64  `json:"supersedes,omitempty" jsonschema:"id of the memory this replaces"`
	Project    string `json:"project,omitempty"`
	Cwd        string `json:"cwd,omitempty" jsonschema:"absolute dir to resolve project from"`
	Directory  string `json:"directory,omitempty" jsonschema:"alias of cwd"`
}

func (h *handlers) save(ctx context.Context, _ *mcp.CallToolRequest, a saveArgs) (*mcp.CallToolResult, any, error) {
	p, err := h.projIn(a.Project, a.Cwd, a.Directory)
	if err != nil {
		return nil, nil, err
	}
	r, err := h.svc.Save(ctx, memory.SaveInput{
		Project: p, Scope: a.Scope, Type: a.Type, Title: a.Title, Body: a.Body,
		TopicKey: a.TopicKey, Supersedes: a.Supersedes,
	})
	if err != nil {
		return nil, nil, err
	}
	return text(render.Save(r)), nil, nil
}

type searchArgs struct {
	Query      string `json:"query" jsonschema:"natural-language question or topic_key"`
	Type       string `json:"type,omitempty"`
	K          int    `json:"k,omitempty" jsonschema:"results, default 5"`
	Detail     string `json:"detail,omitempty" jsonschema:"snippet (default) or title"`
	IncludeOld bool   `json:"include_old,omitempty" jsonschema:"include superseded and archived"`
	Project    string `json:"project,omitempty"`
}

func (h *handlers) search(ctx context.Context, _ *mcp.CallToolRequest, a searchArgs) (*mcp.CallToolResult, any, error) {
	r, err := h.svc.Search(ctx, memory.SearchInput{
		Query: a.Query, Project: h.proj(a.Project), Type: a.Type, K: a.K, Detail: a.Detail, IncludeOld: a.IncludeOld,
	})
	if err != nil {
		return nil, nil, err
	}
	return text(render.Search(r)), nil, nil
}

type getArgs struct {
	ID      int64 `json:"id"`
	History bool  `json:"history,omitempty" jsonschema:"include earlier versions"`
}

func (h *handlers) get(ctx context.Context, _ *mcp.CallToolRequest, a getArgs) (*mcp.CallToolResult, any, error) {
	m, err := h.svc.Get(ctx, a.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("memory #%d: %w", a.ID, err)
	}
	return text(render.Memory(m, a.History, "history=true")), nil, nil
}

type updateArgs struct {
	ID       int64   `json:"id"`
	Title    *string `json:"title,omitempty"`
	Body     *string `json:"body,omitempty"`
	Pinned   *bool   `json:"pinned,omitempty"`
	Archive  *bool   `json:"archive,omitempty" jsonschema:"true archives, false restores"`
	Reviewed bool    `json:"reviewed,omitempty" jsonschema:"confirm a stale memory is still valid"`
}

func (h *handlers) update(ctx context.Context, _ *mcp.CallToolRequest, a updateArgs) (*mcp.CallToolResult, any, error) {
	r, err := h.svc.Update(ctx, a.ID, memory.UpdateInput{
		Title: a.Title, Body: a.Body, Pinned: a.Pinned, Archive: a.Archive, Reviewed: a.Reviewed,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("memory #%d: %w", a.ID, err)
	}
	return text(render.Save(r)), nil, nil
}

type contextArgs struct {
	Project   string `json:"project,omitempty"`
	Cwd       string `json:"cwd,omitempty" jsonschema:"absolute dir to resolve project from"`
	Directory string `json:"directory,omitempty" jsonschema:"alias of cwd"`
	Query     string `json:"query,omitempty" jsonschema:"current task: appends its top 5 search hits"`
}

func (h *handlers) context(ctx context.Context, _ *mcp.CallToolRequest, a contextArgs) (*mcp.CallToolResult, any, error) {
	p, err := h.projIn(a.Project, a.Cwd, a.Directory)
	if err != nil {
		return nil, nil, err
	}
	if p == "" {
		return nil, nil, memory.ErrNoProject
	}
	s, err := DigestText(ctx, h.svc, p, a.Query)
	if err != nil {
		return nil, nil, err
	}
	return text(s), nil, nil
}

// DigestText renders a project's digest, then, when query is not blank, the
// top memory.DigestHitsK search hits for it. The CLI context command and
// mem_context share it, so both print the same text.
func DigestText(ctx context.Context, svc *memory.Service, project, query string) (string, error) {
	d, err := svc.Context(ctx, project, memory.ContextBudgetBytes)
	if err != nil {
		return "", err
	}
	out := d.Text()
	if len(d.Entries) == 0 {
		out = "no memories yet\n"
	}
	if strings.TrimSpace(query) == "" {
		return out, nil
	}
	r, err := svc.ContextSearch(ctx, project, query)
	if err != nil {
		return "", err
	}
	return out + fmt.Sprintf("\ntop hits for %q:\n", strings.TrimSpace(query)) + render.Search(r), nil
}

type sessionArgs struct {
	Action  string `json:"action" jsonschema:"start, end or summary"`
	ID      string `json:"id" jsonschema:"session id"`
	Text    string `json:"text,omitempty" jsonschema:"summary text, for action summary"`
	Project string `json:"project,omitempty"`
}

func (h *handlers) session(ctx context.Context, _ *mcp.CallToolRequest, a sessionArgs) (*mcp.CallToolResult, any, error) {
	switch a.Action {
	case "start":
		if err := h.svc.SessionStart(ctx, a.ID, h.proj(a.Project)); err != nil {
			return nil, nil, err
		}
		return text("session " + a.ID + " started\n"), nil, nil
	case "end":
		if err := h.svc.SessionEnd(ctx, a.ID); err != nil {
			return nil, nil, fmt.Errorf("session %s: %w", a.ID, err)
		}
		return text("session " + a.ID + " ended\n"), nil, nil
	case "summary":
		r, err := h.svc.SessionSummary(ctx, a.ID, a.Text)
		if err != nil {
			return nil, nil, fmt.Errorf("session %s: %w", a.ID, err)
		}
		return text(render.Save(r)), nil, nil
	default:
		return nil, nil, errors.New("action must be start, end or summary")
	}
}
