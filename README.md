<p align="center">
  <img src="assets/logo.svg" width="180" alt="memorycard logo: a retro cartridge stamped with an ouroboros">
</p>

# memorycard

Local-first persistent memory for coding agents. One Go binary, one SQLite file,
six MCP tools, dense retrieval boosted by keywords, with a lexical fallback.

[![ci](https://github.com/labdrian-ai/memorycard/actions/workflows/ci.yml/badge.svg)](https://github.com/labdrian-ai/memorycard/actions/workflows/ci.yml)
[![M8ven Score](https://m8ven.ai/badge/mcp/labdrian-ai/memorycard)](https://m8ven.ai/mcp/labdrian-ai/memorycard?s=readme)

Coding agents forget everything when a session ends. memorycard gives them a
memory they can save to and search by meaning, across sessions and across
agents, without a service or a cloud account. It speaks MCP, so it works with
Claude Code, Codex, OpenCode, Pi and any other MCP client.

Status: v0.3.0, episodic memory (decisions, fixes, discoveries, session summaries).

## Why

- **Cheap in tokens.** The six tool definitions cost about 970 tokens (budget
  1,500; estimated as bytes / 3.6 by the server test). A default search returns about 330 tokens for five results.
- **Finds what you mean.** Retrieval is dense (`bge-m3` through a local
  [ollama](https://ollama.com)), boosted by the query's keywords. On
  190 held-out natural questions over a real 3,400-memory store: hit@5 88 %,
  hit@1 71 %, median latency about 230 ms.
- **Never silently degraded.** If the embedder is down, search falls back to
  BM25 and says `mode: lexical`. Notes over 64 KB are saved as numbered parts
  of at most 48 KB, never truncated; saving the same `topic_key` again updates
  the parts in place. A save that lands on an archived memory (same
  `topic_key`, or the same title and body) restores it and says so.
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

A running `memorycard mcp` keeps its code until the client reconnects. When a
newer binary is installed over it, the next tool result says so once, with the
two versions and `/mcp` to reconnect. A build from a checkout also lists the
commits the new binary brings:

```sh
go build -ldflags "-X github.com/labdrian-ai/memorycard/internal/mcpserver.Version=0.4.0-dev+$(git rev-parse --short HEAD) \
  -X github.com/labdrian-ai/memorycard/internal/mcpserver.SourceDir=$PWD" -o ~/.local/bin/memorycard ./cmd/memorycard
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

For hooks: `memorycard project` prints the project the other commands resolve, and
`memorycard capture` reads text on stdin and saves each numbered item of its
`## Key Learnings` block as one memory (type `learning`, scope `project`,
topic `learning/<16 hex digits of its content hash>`, so each learning has its
own curated key and promotion to a vault can take it). The same input twice
adds nothing, nor does a learning already saved without a topic, and capturing
a learning again never overwrites an edit made to it. Input with no block or
no items saves nothing and exits 0. `capture --transcript <file>` takes the subagent's transcript: when
it names paths in another git repository more often than in the working
directory's, the memories go to that repository's project. A pinned
`MEMORYCARD_PROJECT` still wins, and a missing transcript is ignored.

## What the commands return

### Search (`memorycard search`, `mem_search`)

- Session summaries recap whole sessions and match almost anything, so in a
  default search their dense score counts at 0.9: a summary ranks below a note
  that is about as close to the query, but still comes back when it alone
  holds the answer. Lexical-only search (no embedder) leaves them out. Use
  `--type session_summary` (MCP `type: "session_summary"`) to search only
  them. An exact topic-key query (one with a `/`) still finds any type.
- Date range: `--since YYYY-MM-DD` and `--until YYYY-MM-DD` (MCP `since`,
  `until`, both optional) keep memories created in that range, UTC days, both
  ends inclusive: `memorycard search --since 2026-11-05 --until 2026-11-05
  --type session_summary "x"`. Giving either also lets lexical search return
  session summaries, which a default search leaves out. An invalid date exits 2
  with `invalid --since date` (MCP: a tool error) and returns nothing.
- A document saved in parts (`<title> (part i/N)`) comes back once, as its
  best-matching part, with the other parts' ids: `[other parts: #11, #12]`. So
  `k` results are `k` distinct documents.
- Confidence is judged on how similar the best match is, even when the query is
  an exact topic key. A topic-key match is still listed first, but it no longer
  marks the rest of the list "ok".
- Lexical search (embedder off): a query that reads as Spanish also searches the
  English words a note would use, so "¿por qué falló el despliegue?" also
  matches "failed" and "deployment". A query counts as Spanish when it has more
  Spanish than English function words (el, la, de, que, por...) or, with none,
  when it uses Spanish spelling (an accent, ñ, ¿ or ¡). Hybrid search (the
  default) does not translate: its dense ranking already matches across
  languages.

### Get (`memorycard get`, `mem_get`)

`memorycard get [--history] <id>` prints the current version only, ending with
a line such as `(5 earlier versions not shown; --history shows them)` when
there are earlier ones. `--history` (before or after the id) adds them. MCP
`mem_get` takes an optional `history` boolean.

### Save and capture

When a save writes text and an embedder is on, the result lists up to 3 active
memories of the same project and type whose meaning is close (cosine ≥ 0.75),
closest first:

```text
created #3772
similar active memories (if one is outdated, save again with supersedes=<id>):
  #3586 (0.87): Hybrid search hurts the private gold set; stays opt-in
```

To retire an outdated one, save the new memory again with `supersedes=<id>`
(CLI: `--supersedes <id>`); the old one leaves search, the digest and these
hints. Session summaries get no hints. The `possible duplicate of #N` line
(cosine ≥ 0.97) stays as before.

`capture` skips items shorter than 60 characters and items with a probe marker
(a word ending in "probe" followed by digits, such as `codexprobe1730`). The
title is the item's first sentence, cut at 100 characters on a word boundary.
All saved items, every chunk included, are embedded together, in requests of
at most 16 texts; if one fails they stay pending for `memorycard embed`. Each item then gets the same hints a
single save gives, compared only with memories saved before it. A last line
reports the counts: `captured: 3 saved, 2 skipped (1 too short, 1 duplicate)`.

`save --defer-embed` stores the memory without calling the embedder (no vector,
no similar-memory hints); `memorycard stats` counts it under `without
embedding` until `memorycard embed` indexes it.

### Session ingest (`memorycard session ingest`)

`memorycard session ingest --from claude-postcompact` reads a Claude Code
PostCompact hook event on stdin (`session_id`, `cwd`, `transcript_path`,
`compact_summary`, optional `agent_id`) and keeps one `session_summary` memory
per session, with topic_key `session/<session_id>`: each compaction updates it.
It prints nothing and always exits 0, so a hook never fails the session; when it
saves nothing (malformed JSON, empty summary, no session id, an unopenable
store) the reason goes to stderr. An event with `agent_id` (a subagent) is
ignored. Only a bad `--from` or a missing verb exits 2.

- Body: the sections Primary Request, Errors and fixes, Problem Solving, Pending
  Tasks and Current Work, plus the user messages as a list (each item cut to 160
  characters, the list to 1500 bytes). Key Technical Concepts, Files and Code and
  Optional Next Step are dropped. Headings may read `1. X`, `1. **X**`, `## 1. X`
  or lowercase. A summary without those sections is kept as its first 4096 bytes.
  The body is capped at 16 KB so it stays one memory.
- Title: `Session YYYY-MM-DD · <project> · <first line of the primary request>`
  (80 characters). The first body line is `Date: <ISO start> · cwd: <cwd> ·
  transcript: <path>`; the date is the timestamp of the transcript's first line,
  or the ingest time (UTC) when it cannot be read.
- Project: resolved from `cwd`; inside a git worktree it is the main checkout's
  project. `MEMORYCARD_PROJECT` still wins.
- Secrets are replaced by `[REDACTED]` on this command only: values of
  KEY/PASSWORD/SECRET/TOKEN assignments, `Bearer` tokens, `ghp_`/`gho_`/
  `github_pat_`, `sk-` and AWS `AKIA` keys, JWTs, PEM blocks and `user:pass@`
  in URLs.
- The memory is saved without embedding, so the hook returns fast; run
  `memorycard embed` to index it.

### Session digest (`memorycard context`, `mem_context`)

`memorycard context [--project P] [--query Q | <query>]` prints a project's
digest in up to 2048 bytes: pinned memories, then at most 3 stale decisions to
review (`... N more stale (use mem_search)` when there are more), then the most
recent memories.

A document stored in parts is one line, `#<part 1 id> ... Title (N parts)`.
Session summaries take one line, and so do the automated mirrors (documents
under `sdd/`, `odd/` or `project/` topic keys whose title starts with their
topic key, and architecture notes under `sdd/` or `odd/`; a hand-titled
architecture note under `project/`, such as an ADR, keeps its own line): the
newest one with `(+N more session
summaries)` or `(+N more mirrors)`. `--project` picks the project. A query
(`--query`, or positional words) appends `top hits for "<query>":` with the top
5 search hits. `mem_context` takes the same optional `query`.

### Long memories are embedded in chunks

A body longer than the embed window (1500 characters) is embedded as
overlapping chunks: windows of 1500 characters with 200 of overlap, each
prefixed with the title. Dense search scores the memory by its first window,
lifted by 15% of the gap up to its best chunk: a passage deep inside a long
note can raise it above other long notes, but not past a note that matches
better as a whole. Saving or editing a memory replaces all of its chunks.

Memories embedded before chunking keep their single vector and stay searchable;
`memorycard embed` adds their missing chunks (about one chunk per second on a
CPU-only ollama). The database migrates to schema v4, which adds a
`vector_chunks` table. To return to v3:
`sqlite3 memory.db "drop table vector_chunks; pragma user_version = 3"`.

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

The server resolves its default project once, from the directory it was
started in. `mem_save` and `mem_context` also take `cwd` (or its alias
`directory`), an absolute path that resolves the project on that call, the
same way as `MEMORYCARD_PROJECT` below; an explicit `project` still wins.

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
| `MEMORYCARD_SEARCH=dense` | rank by meaning only. The default, `hybrid`, boosts meaning with the query's keywords and a light BM25 fusion (the mode measured in [Benchmark](#benchmark)) |

## Importing existing memories

An existing Engram database can be copied in:

```sh
sqlite3 ~/.engram/engram.db ".backup engram-copy.db"
memorycard import engram engram-copy.db
memorycard embed
```

The import is read-only on the source, resumable, merges exact duplicates,
splits oversized notes into parts instead of truncating, and maps judged
`supersedes` relations. Rerun it to reconcile deletions: a memory that copies
an observation Engram has since deleted (imported, or with its `topic_key` or
its exact content) is archived, unless it was written after the deletion or a
live observation still holds that key or content. `archived deleted` counts
them.

## Benchmark

Retrieval on [LongMemEval_S](https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned)
(`longmemeval_s_cleaned.json`, 500 questions, each with its own haystack of
about 50 sessions). Each question gets a fresh database with one memory per
session; the 30 abstention questions are excluded. recall_any@k is the share of
questions whose answer session is in the top k.

| System | any@1 | any@5 | any@10 |
|---|---|---|---|
| memorycard, default search | 86.8 % | 98.1 % | 98.9 % |
| memorycard, `MEMORYCARD_SEARCH=dense` | 81.3 % | 93.6 % | 97.0 % |
| MemPalace raw, same 470 questions | 81.7 % | 96.6 % | 98.3 % |

The MemPalace row is scored from the per-question results its authors
published. Their best mode, hybrid_v4, was tuned on 50 questions and reports
90.1 % any@1 and 98.3 % any@5 on the other 450 (424 without abstentions);
memorycard scores 86.3 % and 97.9 % on those same 424.

Default search ranks by meaning (dense cosine), multiplies each score by
1 + 0.3 × the share of query words found in the title and the best
1500-character window of the body, then fuses the top 10 BM25 results at half
weight, never moving the dense winner. Choices were made on a private set of
190 mostly Spanish questions, where it lifts hit@1 on all three splits (see
`odd/tasks/longmemeval-benchmark.md`). Per-question results:
[`bench/results/longmemeval-s-hybrid-session.jsonl`](bench/results/longmemeval-s-hybrid-session.jsonl).
CPU only, `bge-m3` through a local ollama, search p50 about 230 ms on a store
of 3,600 memories, query embedding included.

Reproduce (embeddings are cached by content, so reruns and resumed runs are
cheap):

```sh
go run ./cmd/mc-bench -data longmemeval_s_cleaned.json -granularity session \
  -hybrid -cache cache.db -out run.jsonl
```

End-to-end QA gives the top k sessions of the default search to a reader
model and scores its answer with a judge model, both through the
[Codex CLI](https://github.com/openai/codex), using the official LongMemEval
reader and judge prompts (abstention questions included). With `gpt-6.1-sol`
as reader and judge, on all 500 questions:

| Question type | n | top 5 | top 10 | top 20 | top 20, careful reader |
|---|---|---|---|---|---|
| single-session-user | 70 | 98.6 % | 100.0 % | 98.6 % | 98.6 % |
| single-session-assistant | 56 | 100.0 % | 100.0 % | 100.0 % | 100.0 % |
| single-session-preference | 30 | 93.3 % | 93.3 % | 96.7 % | 93.3 % |
| knowledge-update | 78 | 91.0 % | 94.9 % | 93.6 % | 97.4 % |
| temporal-reasoning | 133 | 85.0 % | 90.2 % | 94.0 % | 94.7 % |
| multi-session | 133 | 73.7 % | 81.2 % | 90.2 % | 88.7 % |
| abstention (subset of the above) | 30 | 80.0 % | 86.7 % | 80.0 % | 90.0 % |
| **all** | 500 | **87.0 %** | **91.2 %** | **94.4 %** | **94.6 %** |

Reading 10 sessions instead of 5 fixes 21 questions and breaks none. Reading
20 instead of 10 fixes 24 more and breaks 8: with more sessions the reader
sometimes picks an outdated value or answers a question it should decline.
Of the 22 answerable questions still wrong at top 20, 17 read all of their
answer sessions. The judge runs without temperature control, so a rerun can
move a question or two.

The last column is not the official reader: `-reader careful` adds four
rules to its prompt (take the most recent value of a fact that changed, list
items before counting them, write down the dates in time arithmetic, decline
when the exact thing asked about is never mentioned). The rules were written
and then narrowed by looking at the misses of these same 500 questions, so
treat it as tuned. Its total is one question above the official reader,
within the judge's noise; what changes is the mix: it declines unanswerable
questions far more often and picks the updated value, at the cost of a few
multi-session and preference questions. Per-question results:
[`bench/results/longmemeval-s-qa-gpt-6.1-sol.jsonl`](bench/results/longmemeval-s-qa-gpt-6.1-sol.jsonl)
(top 5),
[`bench/results/longmemeval-s-qa-gpt-6.1-sol-k10.jsonl`](bench/results/longmemeval-s-qa-gpt-6.1-sol-k10.jsonl)
(top 10),
[`bench/results/longmemeval-s-qa-gpt-6.1-sol-k20.jsonl`](bench/results/longmemeval-s-qa-gpt-6.1-sol-k20.jsonl)
(top 20) and
[`bench/results/longmemeval-s-qa-gpt-6.1-sol-k20-careful.jsonl`](bench/results/longmemeval-s-qa-gpt-6.1-sol-k20-careful.jsonl)
(top 20, careful reader).

```sh
go run ./cmd/mc-qa -data longmemeval_s_cleaned.json -cache cache.db \
  -model gpt-6.1-sol -k 20 -out qa.jsonl   # add -reader careful for the last column
```

`-split dev` or `-split test` runs a fixed half of the questions, balanced
by type. Further reader changes are chosen from `dev` misses only and
measured on `test`.

`-dataset locomo` runs [LoCoMo](https://github.com/snap-research/locomo)
(`locomo10.json`, CC BY-NC 4.0, not redistributed here) through the same
pipeline as a held-out check: one memory per session, every question of a
conversation over that conversation's sessions, answerable questions
(categories 1-4) judged with
[Mem0's LoCoMo judge prompt](https://github.com/mem0ai/mem0/blob/aae5989e78a6188b3b047c104d960c9ad0927e75/evaluation/metrics/llm_judge.py)
and adversarial ones (category 5) as abstention. On a 400-question sample
(seed 20261029), top 10 sessions, `gpt-6.1-sol` as reader and judge:

| Reader | all | answerable (1-4) | adversarial (5) |
|---|---|---|---|
| official | 82.8 % | 90.2 % | 58.9 % |
| careful | 88.0 % | 88.9 % | 85.3 % |

Nothing was tuned on LoCoMo. The careful reader gains 25 adversarial
questions and loses none, at the cost of 5 answerable ones: the same trade
as on the LongMemEval `test` half. Per-question results are not published
because they quote the dataset.

### Private acceptance run (`mc-accept`)

`mc-accept -db copy.db -gold dir[,dir]` measures retrieval against private
gold sets. By default it uses the lab-index view: superseded and archived
memories compete, every project is searched, dense ranking. To measure what an
agent sees:

```sh
mc-accept -db copy.db -gold dir -include-old=false -project @target -hybrid
```

This searches only active memories, inside the project of each question's
answer, with the default hybrid ranking. `-project NAME` fixes one project.

Gold format: `queries.tsv` (id, query, lang[, split]), optional `relevant.tsv`
(id TAB accepted keys) and optional `outdated.tsv` (id TAB keys of outdated
memories that contradict the answer). A key is an Engram id from the import
map, or `mc:<id>` for a memorycard memory. Outdated memories are never
accepted; the run reports how often the current answer ranks above them.

`-digest p1,p2` reports, per project, the share of `memorycard context` lines
that are session summaries, further parts of a document already listed, or
titles cut with "...". It needs neither a gold set nor the embedder.

## Development

```sh
go test ./...
go run ./cmd/mc-accept -db memory.db -gold <dir> -k 5   # acceptance runner
```

Design notes and the task log live in `odd/tasks/`.

## License

MIT. See [LICENSE](LICENSE). memorycard is an independent implementation; it
shares no code with other memory tools.
