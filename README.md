<p align="center">
  <img src="assets/logo.svg" width="180" alt="memorycard logo: a retro cartridge stamped with an ouroboros">
</p>

# memorycard

Local-first persistent memory for coding agents. One Go binary, one SQLite file,
six MCP tools, dense retrieval with a lexical fallback.

[![ci](https://github.com/labdrian-ai/memorycard/actions/workflows/ci.yml/badge.svg)](https://github.com/labdrian-ai/memorycard/actions/workflows/ci.yml)

Coding agents forget everything when a session ends. memorycard gives them a
memory they can save to and search by meaning, across sessions and across
agents, without a service or a cloud account. It speaks MCP, so it works with
Claude Code, Codex, OpenCode, Pi and any other MCP client.

Status: v0.1, episodic memory (decisions, fixes, discoveries, session summaries).

## Why

- **Cheap in tokens.** The six tool definitions cost about 720 tokens (budget
  1,500). A default search returns about 330 tokens for five results.
- **Finds what you mean.** Retrieval is dense (`bge-m3` through a local
  [ollama](https://ollama.com)), not keyword matching. On 190 held-out natural
  questions over a real 3,400-memory store: hit@5 85 %, hit@1 65 %, median
  latency about 200 ms.
- **Never silently degraded.** If the embedder is down, search falls back to
  BM25 and says `mode: lexical`. Notes over 16 KB are saved as numbered parts
  of about 12 KB, never truncated; saving the same `topic_key` again updates
  the parts in place.
- **Honest about confidence.** `confidence: low` flags results for questions
  the store knows nothing about. It cannot tell whether related memories hold
  the exact fact asked for (no retrieval score separates those cases), so the
  agent reading the results decides that.
- **No services.** Pure-Go SQLite (no cgo), no daemon, no network except the
  local embedder.

## Install

Download the archive for your platform from
[Releases](https://github.com/labdrian-ai/memorycard/releases) and put
`memorycard` on your `PATH`, or build it with Go:

```sh
go install github.com/labdrian-ai/memorycard/cmd/memorycard@latest
```

Then pull the embedding model into a local [ollama](https://ollama.com).
Without it memorycard still works, with keyword search only.

```sh
ollama pull bge-m3
```

## Try it

```sh
memorycard save --title "Auth model" --body "Signed tokens, 15 min expiry"
memorycard search "how do sessions expire"
```

## Connect your agent

memorycard serves its tools over stdio with `memorycard mcp`.

Claude Code:

```sh
claude mcp add --scope user memorycard -- memorycard mcp
```

Codex (`~/.codex/config.toml`):

```toml
[mcp_servers.memorycard]
command = "memorycard"
args = ["mcp"]
```

OpenCode (`opencode.json`):

```json
{
  "mcp": {
    "memorycard": { "type": "local", "command": ["memorycard", "mcp"] }
  }
}
```

Pi (`~/.pi/agent/mcp.json`):

```json
{
  "mcpServers": {
    "memorycard": { "command": "memorycard", "args": ["mcp"] }
  }
}
```

MCP tools: `mem_save`, `mem_search`, `mem_get`, `mem_update`, `mem_context`,
`mem_session`. Diagnostics (`stats`, `embed`, `import`, `version`) live in the
CLI.

Agents use memory best when told when to save and search. A line like this in
your agent instructions (`CLAUDE.md`, `AGENTS.md`) is enough to start:

> Search memorycard before starting work that may have been done before. After
> a decision, a bug fix or a non-obvious discovery, save it with `mem_save`.

## Configuration

| Variable | Meaning |
|---|---|
| `MEMORYCARD_DB` | database path |
| `MEMORYCARD_PROJECT` | project name; if unset it is resolved as: a `.memorycard` file (first line) in the directory or above, the git `origin` remote name, the git root name, the directory name. Every agent started inside the same repository, worktrees included, shares one project |
| `MEMORYCARD_EMBED=off` | disable the embedder (lexical only) |
| `MEMORYCARD_OLLAMA_URL` | ollama endpoint |
| `MEMORYCARD_EMBED_MODEL` | embedding model (default `bge-m3`) |

## Importing existing memories

An existing Engram database can be copied in:

```sh
sqlite3 ~/.engram/engram.db ".backup engram-copy.db"
memorycard import engram engram-copy.db
memorycard embed
```

The import is read-only on the source, resumable, merges exact duplicates,
splits oversized notes into parts instead of truncating, and maps judged
`supersedes` relations.

## Benchmark

Retrieval on [LongMemEval_S](https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned)
(`longmemeval_s_cleaned.json`, 500 questions, each with its own haystack of
about 50 sessions). Each question gets a fresh database with one memory per
session; the 30 abstention questions are excluded. recall_any@k is the share of
questions whose answer session is in the top k.

| System | any@1 | any@5 | any@10 |
|---|---|---|---|
| memorycard, hybrid search | 86.4 % | 97.9 % | 99.1 % |
| MemPalace raw, same 470 questions | 81.7 % | 96.6 % | 98.3 % |

The MemPalace row is scored from the per-question results its authors
published. Hybrid search fuses dense and BM25 rankings; it is not the default
yet because it lowers a private set of mostly Spanish questions (see
`odd/tasks/longmemeval-benchmark.md`). Per-question results:
[`bench/results/longmemeval-s-hybrid-session.jsonl`](bench/results/longmemeval-s-hybrid-session.jsonl).
CPU only, `bge-m3` through a local ollama, search p50 180 ms.

Reproduce (embeddings are cached by content, so reruns and resumed runs are
cheap):

```sh
go run ./cmd/mc-bench -data longmemeval_s_cleaned.json -granularity session \
  -hybrid -cache cache.db -out run.jsonl
```

## Development

```sh
go test ./...
go run ./cmd/mc-accept -db memory.db -gold <dir> -k 5   # acceptance runner
```

Design notes and the task log live in `odd/tasks/`.

## License

MIT. See [LICENSE](LICENSE). memorycard is an independent implementation; it
shares no code with other memory tools.
