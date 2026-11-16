# memorycard list — read-only listing by topic prefix
Objective: a read-only `memorycard list --topic-prefix` verb so labdrian's procedural-candidate scan reads memorycard through its public CLI instead of its private SQLite schema.
Branch: feat/list-topic-prefix (from main 89178c7) | Delivery: one PR, owner's standing merge ok | Test runner: `go test ./...`

## Specs
Source of truth: the labdrian-sdd-overlay feature document `odd/tasks/labdrian-orchestrator-opencode.md` (branch `feat/labdrian-orchestrator-opencode`), task T25a, design in its Log L37, owner decision in L35.

- S1. The owner chose option B for how labdrian reads memorycard, verbatim: "B" (overlay L35) — a new `memorycard list --topic-prefix --json` verb in this repository, not a read of the SQLite file.
- S2. Contract (overlay L37): `memorycard list --topic-prefix P [--topic-prefix P2 ...] [--project NAME] [--json]`; literal prefix match, never `LIKE`; active memories only; writes nothing (no `use_count` or `last_used_at`); stored rows as they are, split parts included; JSON `{"project","memories":[{"id","topic_key","title","body"}]}` with `memories` always an array.

## Tasks
- [x] T1 — S1, S2 — delegated builder — the verb, its query, text and JSON output, usage text, README section, tests — commits: b3cb5ce, 733c8e0, d9943f9, 5d29f4d

## Log
- **L1** (2026-11-16) The overlay owner's request for this work, verbatim: "B" (answer to "¿Vamos con B o con A?" on how the labdrian foreman reads memorycard; overlay L35). Built by a delegated builder in a fresh worktree off main; manual runs used a scratch `MEMORYCARD_DB` with a marker memory read back first, never the live store.
- **L2** (2026-11-16, review answers; each commit reviewed alone.)
  - `b3cb5ce` sweep T2 `errcheck` on `defer rows.Close()` in `internal/memory/list.go`: live — the repository's convention (`internal/memory/ops.go:85`, `search.go:350`, `deleted.go:158`, `accept/accept.go:308`, `importer/engram.go:75`), and `List` returns `rows.Err()`.
  - `b3cb5ce` F1 (warning: text order lists `<key>/part-10` before `<key>/part-2`): fixed in `733c8e0`, RED `TestListOrdersTenOrMorePartsNumerically` (11 parts); id order was rejected because a re-save reuses part rows (`save.go:162-167`). F2 (extra positional arguments checked after the query): fixed in `733c8e0`, `fs.NArg()` is checked right after parsing; no observable RED, since the old code already exited 2 with the usage line and `Run` opens the database before any verb; pinned in the exit-2 table.
  - `733c8e0` F1 and F1 (2nd) (a sibling `<key>-x` now sorts after `<key>/part-N`, while the docs said "topic key then id"): fixed in `d9943f9`, the README, the `List` doc comment and the usage text state the order as document key (a part counts as its document), then part number, then id; pinned by `TestListKeepsADocumentsPartsBeforeASiblingKey`.
  - `d9943f9` F1 and F1 (2nd) (the commit adds an id tie-break, a behaviour change its message did not state): answered in `5d29f4d`, whose message states it, and pinned there by `TestListOrdersEqualDocumentKeyAndPartNumberByID`, which failed with `ids = [2 1 3], want [1 2 3]` when the tie-break was replaced by `return false`. F2 and F2 (2nd) (orphan line in the usage text, dangling doc-comment line): fixed in `5d29f4d`.
  - `5d29f4d` F1 (the test's reliance on `d/part-02` parsing as part 2 is not visible in the diff): answered, `partSuffix` is `^(.*)/part-(\d+)$` and the number goes through `strconv.Atoi` (`internal/memory/list.go:86,95`), so `02` is 2; the pin's failure under `return false` shows it depends on the tie-break. F2 (the message mentions a change the diff does not hold): answered, that is its purpose: it records that `d9943f9` changed behaviour, as `d9943f9` F1 asked.
  - Equal (document key, part) rows do occur: `d/part-2` beside `d/part-02`, and one topic key live in two scopes, because the live index covers project, scope and topic key (`internal/store/schema.go:98-99`) and `List` filters by project only. The labdrian adapter (overlay T25b) has to expect the same key twice.
