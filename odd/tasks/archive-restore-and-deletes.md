# Archive restore and mirrored deletes

Objective: close the gaps the owner found comparing Engram 3.6.1 with memorycard v0.2.0: saves that silently land on archived memories, Engram deletions that never reach memorycard, a dual-write check blind to both, and a project resolved only once per MCP server.
Branch: `feat/archive-restore-and-deletes` (one branch, as the owner asked). Delivery: single-pr. Test runner: `go test ./...`.

## Specs

- S1. Owner: "Save sobre memoria archivada: hoy, un mem_save con contenido idéntico a una archivada devuelve "duplicate" con el id archivado, y un upsert por topic_key sobre una archivada devuelve "updated" pero sigue archivada e invisible. Es una pérdida silenciosa. Que reactive la memoria si el save es más nuevo (como hace Engram 3.6.1: restaura solo si updated_at > deleted_at, con precisión subsegundo), o que devuelva un error claro. Proponé cuál y justificalo antes de implementar si dudás." Chosen (assumption, justified in L2): reactivate when the save is newer than the archiving, with RFC 3339 nanosecond timestamps; otherwise fail with a clear error naming the id. Archiving starts recording its time in `updated_at`.
- S2. Owner: "Espejar borrados: (a) proponé el texto para sumar a la regla de escritura doble (cuando se hace mem_delete en Engram, hacer mem_update archive=true en memorycard), pero NO toques todavía los archivos de reglas globales, mostrame el texto; (b) que el importer o un paso de reconcile archive las memorias cuya fuente en Engram tiene deleted_at (hoy el importer solo lee deleted_at is null y saltea lo ya importado)."
- S3. Owner: "~/memorycard-lab/dualwrite-check.py que mire el estado: vivo en Engram y archivado en memorycard = problema; borrado en Engram y activo en memorycard = problema; excluir las archivadas del chequeo de huérfanas. Hacé backup del script antes de cambiarlo, y corrélo después (solo lectura) contra los stores reales para ver si aparecen diferencias por borrados que hoy no se ven." The script lives outside the repo.
- S4. Owner: "Argumento opcional cwd/directory en mem_save y mem_context, resuelto en cada llamada (como #2218 de Engram), en vez de usar solo el project() calculado al arrancar el MCP."
- S5. Owner's rules: "no toques el store real ~/.memorycard/memory.db salvo con el dualwrite-check de solo lectura; los tests usan stores temporales. No instales el binario nuevo, no reinicies sesiones ni toques otros agentes, no actualices Engram, y no pushees ni mergees sin preguntarme. Al final reportame: los cambios por punto, los tests (antes fallan / después pasan), el resultado del dualwrite-check nuevo y el texto propuesto para la regla."

## Tasks

| ID | Specs | Route | Status | Evidence |
|---|---|---|---|---|
| T1 | S1 | inline; high risk item 1 (changes stored status of existing rows), own commit | pending | |
| T2 | S2 | inline; high risk item 1 (importer archives existing rows), own commit; rule text in the report only | pending | |
| T3 | S3 | inline, outside the repo; backup first | pending | |
| T4 | S4 | inline | pending | |

## Log

- L1 (2026-11-03): Owner's request, verbatim in S1-S5 (pasted as one message).
- L2 (2026-11-03): Exploration. `write` in `internal/memory/save.go` looks up the topic key among rows that are not superseded, so archived rows are upserted and stay archived; an identical save without a live match finds the archived row by content hash and reports duplicate. `Update` archives without touching `updated_at`, so the store does not know when a memory was archived. Engram 3.6.0 (installed) stores `deleted_at` in whole seconds ("2006-01-02 15:04:05"); 1,198 observations are deleted, the latest on 2026-10-01. Decision for S1: reactivate. A memorycard save is stamped with the current time, so it is newer than any archiving done before it and the guard only bites on clock skew; then a clear error is safer than writing into an invisible row. An error on every save over an archived row would push agents to work around it and lose the write anyway.
