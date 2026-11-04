# Feature: memory accuracy and efficiency

Objective: apply every fix the 2026-11-04 audit found, in parallel. Branch `feat/accuracy-efficiency` from `main` d8692a3. Delivery: ask-on-risk. Runner: `go test ./... -count=1`; benchmark `cmd/mc-accept` on a copy of the live store, never the live store.

## Specs

The owner asked: "en ultracode todos los arreglos en paralelo", on the ten fixes below (audit of 2026-11-04, L1).

- S1 — Benchmark sees what users see: the private gold run (`cmd/mc-accept`, `internal/accept`) can run active-only and project-scoped, has a "current vs outdated" question set (seed case: "¿búsqueda densa o híbrida por defecto?" must answer #3631, not #3586/#3384), and a digest check (share of digest lines that are session summaries, chunk parts or truncated titles). Baseline numbers recorded before any ranking change.
- S2 — `mem_get` / `memorycard get` return the current version without the full history by default; history stays available on request (MCP optional field, CLI flag). Measured: #3447 returns 54,437 bytes today, ~10 KB of it current.
- S3 — Search: `session_summary` does not compete in default search (still returned when the type filter asks for it); chunk parts of one document collapse to one hit (best part, sibling ids shown); recency breaks near-ties; a widened search embeds the query once; a `/` in a query no longer forces confidence "ok" when only the topic-key lookup matched.
- S4 — Digest (`memorycard context`, MCP `mem_context`): one line per document, not per part; session summaries and automated mirrors capped at one line; stale tier capped at 3 lines; `--project` honoured by the CLI; optional task/query argument adds the top search hits.
- S5 — Contradictions: on save, the result names the closest active memories of the same project and type above ~0.75 cosine so the agent can supersede; stale flagging no longer limited to age of `decision`/`policy` where a newer memory supersedes. Data: #3384 and #3586 superseded by #3631.
- S6 — Data cleanup of the live store, after a backup: memorycard decisions filed under project `labdrian` moved to `memorycard` (#3408, #3435, #3384, #3378, #3359, #3350, #3409, #3535, #3443 — each checked first); #3740 and #3742 moved to `labdrian-sdd-overlay`; probes, placeholders and context-free passive fragments archived (#3751, #1017, #1357, #716, #735, #1361-#1408 passive, …); phase rows of SDD changes that have an archive-report archived, except any memory a gold set targets. Every change reversible (archive, not delete).
- S7 — Capture: drops items under ~60 chars and probe markers; title from the first sentence, not an 80-rune cut; all items embedded in one batch; the four agents' capture hooks run without blocking the session.
- S8 — Embeddings cover whole bodies: a memory longer than the embed window gets several vectors (chunks), dense search scores a memory by its best chunk. Tested on a copy; adopted only if the benchmark does not regress.
- S9 — Spanish queries: word matching helps Spanish questions over English notes (stemming and/or query translation). Adopted only if the gold `es` slice improves and nothing else regresses.
- S10 — Agent setup: the Engram protocol block removed from `~/AGENTS.md`; the role agents (`~/.claude/agents/*.md`, built from labdrian-sdd-overlay) get memorycard tools instead of Engram ones; overlay projection hook matchers name memorycard tools; dead Engram config (Pi `mcp-cache.json`) cleared.

## Tasks

- [ ] T1 — S1 — delegated (writer reason: parallel units, disjoint surfaces) — benchmark — commit: -
- [ ] T2 — S2 — delegated (parallel) — get without history — commit: -
- [ ] T3 — S3 — delegated (parallel) — search ranking and noise — commit: -
- [ ] T4 — S4 — delegated (parallel) — digest — commit: -
- [ ] T5 — S5, S7 (code) — delegated (parallel) — write path: neighbour hint, capture filter, batch embed — commit: -
- [ ] T6 — S8 — delegated (parallel) — chunked embeddings — Risk item 1 (vector storage format) — commit: -
- [ ] T7 — S9 — delegated (parallel) — Spanish matching — Risk item 1 (FTS index rebuild) — commit: -
- [ ] T8 — S10 (overlay) — delegated (parallel, other repo) — role agents and projection matchers — Risk item 5 — commit: -
- [ ] T9 — S5 (data), S6 — delegated (parallel) — live store cleanup — Risk item 1 — commit: n/a (data)
- [ ] T10 — S7 (hooks), S10 (home) — inline — `~/AGENTS.md`, capture hooks async, Pi cache — Risk item 5 — commit: n/a (config)

## Log

- **L1** (2026-11-04, owner): "en ultracode todos los arreglos en paralelo" — on the ten-item audit summary: (1) `~/AGENTS.md` Engram protocol; (2) role agents only have Engram tools; (3) `mem_get` returns full history; (4) noise in search (566 summaries, 423 parts); (5) contradictory active decisions (#3586 vs #3631); (6) misfiled data, 824 archived-SDD phase rows, probes; (7) capture blocks the agent; (8) 65% of bodies embedded only for the first 1500 chars; (9) Spanish matching has no stemmer; (10) the benchmark cannot see 4, 5, 6.
