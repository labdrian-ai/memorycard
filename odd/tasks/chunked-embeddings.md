# Chunked embeddings

Objective: embed the whole body of a memory, not only its first 1,500 characters, so evidence deep in a long memory ranks in dense search; stay one SQLite file, no extension, no cgo.
Branch: `feat/chunked-embeddings` (stacked on `feat/longmemeval-reader-doubt`). Delivery: ask-on-risk. Test runner: `go test ./...`.

## Specs

- S1. Owner's request. After the retrieval analysis (`odd/tasks/longmemeval-benchmark.md` L28, `odd/tasks/locomo-benchmark.md` L6) the owner asked "pero seguimos usandio sqlite esto sigue manteniendose simple y eficiente ?", was answered with the measured cost on the live store, and said "bueno hagamoslo". It must stay one SQLite file searched in Go: no vector index, no `sqlite-vec`, no new service.
- S2. Storage (assumption, chosen so no stored data is rewritten): schema v4 adds a table `chunk_vectors(memory_id, chunk, model, dim, vec)` keyed by `(memory_id, chunk)`, cascading on memory delete. The existing `vectors` row stays chunk 0 and keeps its exact text (title plus the first 1,500 runes). Chunk i >= 1 embeds the title plus runes [1500·i, 1500·(i+1)) of the body. A memory has at most `MaxChunks` chunks counting chunk 0; the cap is chosen by measurement (S5).
- S3. Indexing: saving or updating a memory embeds all its chunks and replaces its old extra chunks. `memorycard embed` also embeds missing extra chunks of existing memories, so an existing store is upgraded in place by running it. `stats` "without embedding" counts memories missing any of their chunks. The duplicate hint keeps comparing chunk 0 only.
- S4. Search: a memory's dense score is the best cosine over its chunks; ranking, keyword boost, BM25 fusion and widening are otherwise unchanged. Whether confidence is judged on that best cosine or on chunk 0 alone is chosen by measurement (S5).
- S5. Measurement, without Codex calls: LongMemEval retrieval with `mc-bench` (any@1/5/10 against `bench/results/longmemeval-s-hybrid-session.jsonl`), the private 190-question acceptance set with `mc-accept` (hit@1, hit@5 and the low-confidence negatives), and LoCoMo retrieval recall at 10. Choices are made on LongMemEval and the private set; LoCoMo only measures. An improvement counts only from full runs.
- S6. Rollout: a v4 database is refused by older builds ("database schema v4 is newer than this build (v3)"), so the live store `~/.memorycard/memory.db` migrates only when the owner installs the new build for every agent, after a backup. Nothing in this feature writes to the live store.

## Tasks

| ID | Specs | Route | Status | Evidence |
|---|---|---|---|---|
| T1 | S2, S3, S4 | inline; high risk item 1 (persisted format), own commit, independent verifier | done, verifier pending | Schema v4 `chunk_vectors`; `chunkTexts`, `putVectors`, `missingChunks`, `SetMaxChunks` in `internal/memory/vectors.go`; best-chunk score in `dense` (`internal/memory/search.go`); `Stats`. RED: `s.SetMaxChunks undefined` and `no such table: chunk_vectors`; GREEN for 6 new tests (upgrade keeps vectors, unique chunk, cascade; every chunk embedded; search finds evidence beyond chunk 0 with ok confidence; update drops old chunks; pending fills a v3-era store; cap). Changed by design: the bench test that pinned "session granularity misses answers beyond the embed window" now expects rank 1; old-schema fixtures drop `chunk_vectors`. `go vet ./...`, `go test ./...` ok. Differential on two backup copies of the live store, main build vs this build: 5 searches identical (no extra chunk embedded yet), stats identical except "without embedding" 0 -> 2406; the main build refuses the migrated copy: "database schema v4 is newer than this build (v3)" |
| T2 | S5 | inline | pending | `mc-bench -dataset locomo` for retrieval recall; cap and confidence knobs for the bench |
| T3 | S4, S5 | inline | pending | measurements, choice of cap and confidence source |
| T4 | S6 | inline | pending | README, help text; rollout steps for the owner |

## Log

- L1 (2026-11-04): Owner's words, in order: "miremos los errores", then "pero seguimos usandio sqlite esto sigue manteniendose simple y eficiente ?", then "bueno hagamoslo". Measured on a backup copy of the live store (deleted afterwards): 3,651 active memories, 2,388 with a body over 1,500 characters, 10,774 chunks of 1,500 characters (about 3 per memory), 3,679 vectors of 1,024 floats taking 15 MB, longest body 16,277 characters. Forecast: about 44 MB of vectors, a few ms more per search over a p50 of about 230 ms dominated by embedding the query, and a one-time re-embed of about 7,100 extra chunks on CPU.
