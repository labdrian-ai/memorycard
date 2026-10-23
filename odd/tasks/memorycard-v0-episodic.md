# memorycard v0 — episodic core (Go)
Objective: a local-first episodic memory engine for coding agents that retrieves better and costs fewer tokens than Engram. Clean-room: no Engram code copied.
Branch: feat/v0-episodic | Delivery: ask-on-risk, local commits only (no remote yet) | Test runner: `go test ./...` | Forecast: ~2500 authored lines

## Specs
Source of truth for behavior: `/home/labdrian/memorycard-lab/spec/memorycard-v0.md` (design, derived from measurements) and `/home/labdrian/memorycard-lab/spec/engram-behavior.md` (behavior notes; read for context, never copy code).

- S1. "arrancamos con episodica" — scope is the episodic store only; long-term and procedural tiers are out of this feature.
- S2. "escribamos en go mejor" — Go, single binary, pure-Go SQLite with FTS5 (no cgo).
- S3. "memorycard hace ingenieria inversa de engram copia lo que le sirve y mejora lo en lo que no es optimo la idea es crear un mejor y mas eficiente producto" — reimplement from the behavior spec; improve on the weak points; no code copied from Engram.
- S4. Write path per spec section 4: redact private tags; reject empty title/body; soft target 4 KB (warn), hard cap 16 KB (explicit error, never silent truncation); exact duplicate (same hash, same project) writes nothing and returns the existing id; `topic_key` match updates only if body differs and snapshots the old version (max 5 kept); otherwise embed and insert; one non-blocking `possible duplicate of #id` hint above a similarity threshold, no pending rows; `supersedes=<id>` marks the old row superseded.
- S5. Read path per spec section 5: dense cosine over normalized bge-m3 vectors is primary; BM25-OR lexical fallback when the embedder is down, flagged `mode: lexical`; default k=5, title + 200-char snippet, average <= 350 tokens; `confidence: low` below a calibrated threshold; superseded/archived excluded unless `include_old`; a query containing `/` is tried first as an exact `topic_key` lookup.
- S6. Six MCP tools (`mem_save`, `mem_search`, `mem_get`, `mem_update`, `mem_context`, `mem_session`); all definitions together <= 1,500 tokens; diagnostics live in the CLI.
- S7. Importer from an Engram SQLite database, read-only: map title, body, type, project, scope, topic_key, pinned, timestamps; map `supersedes` relations to `superseded_by`; skip deleted rows; leave `pending` relation rows out; resumable; chunk legacy bodies over the hard cap.
- S8. Acceptance (spec section 10): held-out natural questions hit@5 >= 85 % and hit@1 >= 60 %; default search <= 350 tokens; warm query p50 <= 0.5 s; embedder down still returns lexical results and says so; importing the real store leaves 0 pending relation rows and 0 silent truncations.
- A1 (assumption). Repo lives at `~/memorycard`; module path `github.com/labdrian-ai/memorycard` (placeholder until the owner/name are final). License MIT proposed, not yet added.
- A2 (assumption). Thresholds for duplicate hint and low confidence start at cosine 0.97 and are calibrated in T9 on held-out data.

## Tasks
- [x] T1 (S2) module skeleton, pure-Go SQLite open + pragmas + migrations, FTS5 proven available — route: inline — commit 654aaba; RED: store package failed to build (undefined Open/DB/SchemaVersion); GREEN: 5 tests pass (schema, WAL+FK pragmas, reopen keeps data, FTS5 folds diacritics, FTS follows update/delete). Risk: none (new code, no existing behavior).
- [x] T2 (S4) store: save path, hashing, caps, redaction, topic_key upsert, versions, supersedes — inline — commit 1d8b95d; RED: package did not build; GREEN: 9 save tests; mutation check (version prune off by one) failed the test as expected. Risk: none (new code).
- [x] T3 (S4,S5) embedder interface, ollama client, vector storage, pending-embedding queue — inline — commit 8e9ad19; RED: embed and memory packages did not build; GREEN: 3 client tests + 7 vector/hint tests; integration test (build tag `integration`) against real bge-m3: es-en same-meaning cos 0.812 vs unrelated 0.346. Risk: none (new code; network call only to the local ollama).
- [x] T4 (S5) search: dense, lexical fallback, confidence, topic shortcut, exclusions — inline — commit pending in this entry's next commit; RED: Search undefined; GREEN: 12 search tests; mutation check (inverted ranking) failed the test as expected. Placeholders to calibrate in T9: ConfidenceCosine 0.55. Risk: none (new code).
- [ ] T5 (S6) get/update/context/session operations — inline
- [ ] T6 (S6) CLI + MCP server with the six tools, token-budget test of definitions — inline
- [ ] T7 (S7) Engram importer — inline
- [ ] T8 (S8) acceptance runner over the lab gold sets — inline
- [ ] T9 (S8,A2) calibrate thresholds, record results — inline

Route declaration: all inline. Tasks are sequentially dependent (store -> embedder -> search -> tools), so no parallel-writer trigger holds; no high-risk item expected except T7 reading existing user data (read-only) and T6 defining a public MCP contract (new, not changing one).

## Log
- L1 (2026-10-23, user, verbatim): "arrancamos con episodica"
- L2 (2026-10-23, user, verbatim): "escribamos en go mejor"
- L3 (2026-10-23, user, verbatim): "recuerda que memorycard hace ingenieria inversa de engram copia lo que le sirve y mejora lo en lo que no es optimo la idea es crear un mejor y mas eficiente producto"
- Context: lab evidence in `/home/labdrian/memorycard-lab` (private, git-ignored); bge-m3 dense hit@5 85 % vs Engram 0-3 % on 40 held-out adversarial questions.
- Next: create repo, commit this document on main, branch feat/v0-episodic, start T1 test-first.
