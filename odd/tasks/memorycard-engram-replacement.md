# memorycard replaces Engram

Objective: move every agent from Engram to memorycard in stages: dual write, validation, migration of the integrations, then Engram read-only.
Branch: `main` for docs and agent-instruction changes; a feature branch for any code change. Delivery: ask-on-risk. Test runner: `go test ./...`.

## Specs

- **S1 — Order.** The owner set the order: "primero la escritura doble luego generar la validacion y ver que todo ok entree guardado y esciritura pasamos a la migracion". Each stage starts only when the previous one is done.
- **S2 — Dual write, agent-driven.** The owner chose "Agente guarda en ambos": after every Engram write, the agent writes the same memory to memorycard through its MCP `mem_save`. This applies to the four agents that already load the field-test rules: Claude Code, Codex, Pi and OpenCode. Engram stays the source of truth during the trial. A memorycard failure never blocks the Engram save or the reply; it is reported to the user in one line.
- **S3 — Validation.** "ver que todo ok entree guardado y esciritura". Compare what was written to Engram with what reached memorycard over the dual-write window: every Engram memory created or updated since the start of dual write has a memorycard counterpart in the same project, with the same topic_key and the same content. Report the mismatches. Exact criteria are set in T3 before the first run.
- **S5 — Large bodies.** The owner answered "vamos" to: "que el `mem_save` de memorycard parta solo los cuerpos grandes en varias memorias, igual que ya hace el importador". A save whose body exceeds the 16 KB hard cap is stored as parts, the way the importer splits: parts of about 12 KB, titled `<title> (part i/N)`; part 1 keeps the topic_key and part i>1 gets `<key>/part-i`. Saving the same topic_key again updates the parts in place. Parts left over when the document shrinks are archived; they come back when it grows. Supersedes applies to part 1. `mem_update` by id keeps the cap.
- **S4 — Migration.** Done after S3 passes. Move each integration tied to Engram ids or tools: the longterm-mem vault `promote`, the `thinking-bank` skill, the ODD Engram mirrors, the session hooks, passive capture of Key Learnings, and `mem_review`. Then decide what happens to relations (`mem_judge`, `mem_compare`). Engram goes read-only last. The tasks are written when S3 passes.

## Tasks

| ID | Specs | Route | Status | Evidence |
|---|---|---|---|---|
| T1 | S2 | inline | done | field-test section replaced in `~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md`, `~/.pi/agent/AGENTS.md`, `~/.config/opencode/AGENTS.md` (outside the repo) |
| T2 | S2 | inline | done | Engram #4517 mirrored as memorycard #3446: project `memorycard`, scope `project`, same topic_key, one live holder |
| T3 | S3 | inline | done | `~/memorycard-lab/dualwrite-check.py`; first run: engram 1, matched 1, problems 0 |
| T3b | S3 | inline | done | backfill of the pre-cutover gap (L7): 83 imported, 2 merged, 1 update applied, 85 embedded |
| T6 | S5 | inline | done, merged and installed (L11) | branch `feat/save-splits-large-bodies`: `93cd550` + correction `8b63386`; RED `body exceeds the hard size cap` then GREEN; full suite ok; differential vs `main`: only a timestamp differs; the independent verifier's recheck shows all 3 findings fixed (L10) |
| T4 | S3 | inline | pending | validation run after the dual-write window, with verdict |
| T5 | S4 | — | pending | migration tasks, written when T4 passes |

## Log

- **L1 (2026-10-27).** Owner, verbatim: "y che vamos en ese orden primero la escritura dobleluego generar la validacion y ver que todo ok entree guardado y esciritura pasamos a la migracion".
- **L2 (2026-10-27).** Owner chose "Agente guarda en ambos (Recommended)" over an automatic mirror. Reason given in the question: the agent path exercises memorycard's real write path (topic_key upsert, dedup, supersedes), which has never run in daily use. The importer would not test it, and it also skips records it already imported (`internal/memory/import.go:99`), so Engram updates under an existing topic_key would be lost.
- **L3 (2026-10-27).** The memorycard MCP `mem_save` takes an optional `project` (`internal/mcpserver/server.go`); without it the server uses the project resolved from its own working directory. The rules therefore pass Engram's exact project, so a server started elsewhere cannot file the memory under another project. Session summaries are mirrored as a plain `mem_save` with type `session_summary`.
- **L4 (2026-10-27).** Correction to L3: the first rule keyed summaries `session/<id>`, but agents write to Engram without a real session id (Engram files them all under `manual-save-memorycard`), so every summary would have overwritten the previous one. The rule now saves each summary as its own memory, titled `Session summary: <project>`, with no topic_key, as Engram does. Fixed before any agent used it.
- **L5 (2026-10-27).** T3 criteria, as implemented in `~/memorycard-lab/dualwrite-check.py` (read-only on both stores). The window starts at the cutover, `2026-10-27 14:03:41` UTC (Engram #4517). Every live Engram observation created or updated since then needs a counterpart in the same project and scope. With a topic_key, the counterpart is the live memorycard holder of that key. Without one, it is a memorycard memory with the same content. Content matches after case and whitespace folding. Reported kinds: `missing`, `differs`, `redacted`, `only-memorycard`. Exit 1 on any problem.
- **L6 (2026-10-27).** Finding: the importer ran before 2026-10-24, and Engram memories written between then and the cutover never reached memorycard. Running the check since 2026-10-24 for project `memorycard` alone reports 31 `missing`. That gap predates dual write, so the check excludes it, but memorycard cannot replace Engram with it open. A backfill is a write to the live store and needs the owner's go-ahead. Caveat: the importer skips records it already imported, so a backfill catches new records but not updates to imported ones.
- **L7 (2026-10-27).** Owner approved the backfill ("si dale"). Rehearsed first on a copy; isolation through `MEMORYCARD_DB` was confirmed with a marker row that the live store did not have. Live run:
  - Backup: `~/.memorycard/backups/memory-pre-backfill-2026-10-27.db` (3447 rows, integrity ok).
  - `memorycard import engram`: 83 imported, 2 merged (the two dual-written rows), 1 record split into 3 parts.
  - One post-import Engram update (#4430) was applied as a topic_key save.
  - 85 memories embedded.
  - Result: 3532 rows, integrity ok, 0 live topic_key collisions, 0 without embedding.
  The checker now also matches records the importer split into parts.
- **L8 (2026-10-27).** Check since the import (`--since "2026-10-24 18:27:25"`) after the backfill: engram 89, matched 85, problems 4. Two causes:
  - **Blocker for S2: memorycard rejects bodies over 16 KB** (`HardCapBytes`, `internal/memory/save.go:24`). Engram accepts 50 KB, and ODD feature-doc mirrors pass 16 KB. #4353 (17107 bytes) and #4399 (19242) cannot be mirrored. #4477 was imported in parts, but its later updates cannot go through `mem_save` either. Needs an owner decision.
  - **Agent sessions started before the rule change still run the old rule.** An open intranet-kalani session wrote #4519 and updated #4477 after the cutover without mirroring. This goes away when those sessions restart.
- **L10 (2026-10-27).** T6: the independent verifier checked commit `93cd550` against `main`. Every S5 clause passed except leftover archiving, which had two blockers:
  - non-ASCII topic keys never matched, because a byte length was passed to SQLite's character-counting `substr`;
  - every keyed save archived any `<key>/part-<digits>` row, including rows that were never parts (`release/part-2`, `doc/part-04`).

  Advisory 1: reviving an archived part could reach a row under another key or scope through a duplicate match. Correction `8b63386` fixes all three. The previous part count now comes from part 1's title `(part 1/N)`, only the exact keys `<key>/part-i` up to N are archived, and a revival checks the topic key. Each finding has a RED test. Advisories not fixed: a split save can overwrite an unrelated memory whose own key is `<key>/part-i`; `overallAction` can mislabel mixed results. A recheck confirmed all three findings fixed. Remaining advisory: when a generated part key has no row yet, the duplicate lookup (project and hash only) can resolve that part to an identical row in another scope or under another key. No stored data changes.
- **L11 (2026-10-27).** The owner approved ("si hacelo porfa"); `main` was fast-forwarded to `bc8cc07`.
  - Install: the suite passed, the old binary was backed up to `~/.memorycard/backups/memorycard-pre-split-2026-10-27.bin`, and the new one went to `~/.local/bin/memorycard` through an atomic rename. Schema unchanged (v3).
  - Live proof: the three oversized Engram records were mirrored through the save path with `~/memorycard-lab/mirror-engram.sh`. #4353 and #4399 became 2 parts each, updating the imported row in place. #4477 became 3 parts, updating the importer's parts.
  - Check since the import: engram 92, matched 89. The 3 still `missing` (#4519, #4521, #4522) were written by an intranet-kalani session started before the rule change. That session must restart.
  - MCP servers that were already running keep the old binary until their agent restarts.
- **L9 (2026-10-27), advisory.** An update whose re-embedding fails keeps the old vector. `EmbedPending` only picks up rows with no vector or a different model, so that vector stays stale until the next successful update. Not fixed.
