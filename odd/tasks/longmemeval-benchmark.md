# LongMemEval benchmark

Objective: a public, reproducible benchmark of memorycard on LongMemEval_S, starting with retrieval (recall@k) and later end-to-end QA.
Branch: `feat/longmemeval-bench`. Delivery: ask-on-risk. Test runner: `go test ./...`.

## Specs

- S1. Solve the embedding bottleneck first. The owner said: "lo primero es difinir como vamos a hacr una prueba eficiente cacheando pro contenido o con otro patron para evitar el cuello de botella en el embeddings eso vale la pena que sea el primer punto". Approach accepted with "si dale": a content-addressed cache (key `sha256(model, kind, text)`) as a decorator over `embed.Embedder`, used only by the benchmark. Production code paths are unchanged. A rerun or a resumed run embeds nothing it already embedded.
- S2. Retrieval runner over LongMemEval_S (`xiaowu0162/longmemeval-cleaned`, `longmemeval_s_cleaned.json`). Each question gets a fresh memorycard database built from its own haystack. Granularity is `session` (one memory per session, the current product behavior) or `round` (one memory per user and assistant round). Report recall_any@k and recall_all@k for k = 1, 3, 5 and 10, overall and per question type. Abstention questions (`_abs`) are excluded from retrieval metrics and counted.
- S3. Run a sample of 50 questions with both granularities, then the full set overnight with the winning granularity. The owner chose the sample run with "si dale".
- S5. Hybrid search. The owner said: "si eso te iba a decir el hibrido nos resulta mas beneficioso en resultados pero en recursos que tanto nos aumenta el uso", then "bien entonces vamos para adelante el costo de diseño lo podemos asumir". Dense and BM25 rankings are fused by reciprocal rank fusion (k = 60, a pool of 50 from each). Confidence stays judged on the dense cosine. The mode is opt-in (`Service.SetHybrid`; `-hybrid` in `mc-bench` and `mc-accept`) and the default search is unchanged. It is adopted as the default only if it improves LongMemEval without lowering the 190 private held-out questions.
- S6. Beat MemPalace on the same metric. The owner said: "siii esta es la gente que tenemos que superar", then "bien tenemos que ir para alla para declarar victoria". Their headline is 96.6% session recall_any@5 on all 500 questions (raw mode); their honest hybrid figure is hybrid_v4 98.4% R@5 on a 450-question held-out split. Victory is declared only from a full 500 run, never from the 50-question sample.
- S4. End-to-end QA: the reader and judge model is `gpt-6.1-sol` through Codex. The owner said "y de modelo usemos gpt sol con el proveedor codex" and then "si nosotros vamos a usar gpt-6.1-sol".

## Tasks

| ID | Specs | Route | Status | Evidence |
|---|---|---|---|---|
| T1 | S1 | inline | done | `internal/bench/cache.go`; RED `undefined: OpenCache`, then GREEN for 2 tests (each text embedded once across calls and reopens; a different kind or model misses) |
| T2 | S2 | inline | done | `internal/bench/longmemeval.go`, `cmd/mc-bench`; RED `undefined: Load`, then GREEN for 3 tests (round granularity finds an answer beyond the embed window that session granularity misses; abstention skipped; lexical run; deterministic sample). Smoke run of 3 real questions, lexical: ok |
| T3 | S3 | inline | open | |
| T4 | S3, S6 | inline | running | full 500, hybrid session, queued after T3 and T7 runs: `runs/full-hybrid-session.jsonl` |
| T5 | S4 | inline | open | |
| T6 | S5 | inline | done | `fuse` in `internal/memory/search.go`. RED `s.SetHybrid undefined`, then GREEN for 3 memory tests (words beyond the embed window found; dense ranking and cosine confidence kept; lexical fallback) and 1 bench test. Differential of `memorycard search` (5 queries × 2 flag sets) between main and the branch on a copy of the live store: 10/10 identical |
| T7 | S5 | inline | open | measure hybrid on the 50-question sample and on the 190 private questions; decide the default |

## Log

- L1 (2026-10-29, owner, verbatim): "hablemos del benchmark" … "lo primero es difinir como vamos a hacr una prueba eficiente cacheando pro contenido o con otro patron para evitar el cuello de botella en el embeddings eso vale la pena que sea el primer punto" … "si dale".
- L2 (2026-10-29): Sizing measured before building. On this machine (CPU only, 20 cores, no GPU), bge-m3 embeds ~1.0 text/s at 1500 chars and ~4.5 text/s at ~330 chars; batching barely helps. The full set has 23,867 haystack sessions and 18,410 unique embed windows, so reuse across haystacks is only 1.3x and a cache saves ~23% of the first run. Its main value is free reruns and resumability. 40% of answer turns (355 of 896) start beyond char 1500, which memorycard never embeds (`EmbedBodyRunes`), so session-level retrieval is expected to miss them. Rounds number 96,064 unique (~5.9 h); sessions take ~5.1 h; the 50-question sample takes ~0.6 to 0.7 h per granularity. Data: `~/memorycard-lab/longmemeval/data/` (sha256 `d6f21ea9d60a0d56f34a05b609c79c88a451d2ae03597821ea3d5a9678c3a442`, MIT).
- L3 (2026-10-29): Session resumed after a cut. `dense/round` resumed from question 22 via `-out`; the private acceptance (`mc-accept`) runs on a copy of `~/.memorycard/memory.db`, whose `import_map` maps the gold Engram ids. Its earlier p50 of 35 s was CPU contention with the bench, so it is rerun alone. MemPalace scored on our 46 sample questions from their committed per-question file (`results_mempal_raw_session_20260414_1629.jsonl`): raw any@1/3/5/10 = 80.4/89.1/91.3/95.7, memorycard dense 80.4/84.8/93.5/95.7, hybrid 84.8/97.8/97.8/97.8. A 3-question gap is not significant at n=46.
