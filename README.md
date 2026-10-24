# memorycard

Local-first persistent memory for coding agents. One Go binary, one SQLite file,
six MCP tools, dense retrieval with a lexical fallback.

Status: v0, episodic tier only. Not yet published.

## Why

- **Cheap in tokens.** The six tool definitions cost about 720 tokens (budget
  1,500). A default search returns about 330 tokens for five results.
- **Finds what you mean.** Retrieval is dense (`bge-m3` through a local
  [ollama](https://ollama.com)), not keyword matching. On 190 held-out natural
  questions over a real 3,400-memory store: hit@5 85 %, hit@1 65 %, median
  latency about 200 ms.
- **Never silently degraded.** If the embedder is down, search falls back to
  BM25 and says `mode: lexical`. Oversized notes are rejected with an explicit
  error, never truncated.
- **No services.** Pure-Go SQLite (no cgo), no daemon, no network except the
  local embedder.

## Install and run

```sh
go build -o memorycard ./cmd/memorycard
ollama pull bge-m3

memorycard save --title "Auth model" --body "Signed tokens, 15 min expiry"
memorycard search "how do sessions expire"
memorycard mcp          # MCP server over stdio
```

MCP tools: `mem_save`, `mem_search`, `mem_get`, `mem_update`, `mem_context`,
`mem_session`. Diagnostics (`stats`, `embed`, `import`) live in the CLI.

## Configuration

| Variable | Meaning |
|---|---|
| `MEMORYCARD_DB` | database path |
| `MEMORYCARD_PROJECT` | project name; if unset it is resolved as: a `.memorycard` file (first line) in the directory or above, the git `origin` remote name, the git root name, the directory name. Every agent started inside the same repository, worktrees included, shares one project |
| `MEMORYCARD_EMBED=off` | disable the embedder (lexical only) |
| `MEMORYCARD_OLLAMA_URL` | ollama endpoint |
| `MEMORYCARD_EMBED_MODEL` | embedding model (default `bge-m3`) |

## Importing from Engram

```sh
sqlite3 ~/.engram/engram.db ".backup engram-copy.db"
memorycard import engram engram-copy.db
memorycard embed
```

The import is read-only on the source, resumable, merges exact duplicates,
splits oversized notes into parts instead of truncating, and maps judged
`supersedes` relations.

## Development

```sh
go test ./...
go run ./cmd/mc-accept -db memory.db -gold <dir> -k 5   # acceptance runner
```

Design notes and the task log live in `odd/tasks/`.

## License

MIT. See [LICENSE](LICENSE). memorycard is an independent implementation; it
shares no code with other memory tools.
