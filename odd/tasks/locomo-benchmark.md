# LoCoMo benchmark

Objective: an independent held-out end-to-end QA benchmark (LoCoMo, `locomo10.json`) through the same pipeline as LongMemEval, so reader changes are not judged only on the questions they were tuned on.
Branch: `feat/locomo-bench` (stacked on `feat/longmemeval-split`). Delivery: ask-on-risk. Test runner: `go test ./...`.

## Specs

- S1. Add LoCoMo as a second benchmark. Offered either splitting the LongMemEval 500 or adding LoCoMo, the owner said "Porque no hacer las 2" (2026-11-01). LoCoMo is never used to tune prompts or rules; it only measures.
- S2. Data: `https://raw.githubusercontent.com/snap-research/locomo/main/data/locomo10.json`, sha256 `79fa87e90f04081343b8c8debecb80a9a6842b76a7aa537dc9fdf651ea698ff4`, 2,805,274 bytes, license CC BY-NC 4.0. It is downloaded at run time and never committed. 10 conversations, 272 sessions, 1,986 questions; category codes in the data: 1 multi-hop (282), 2 temporal (321), 3 open-domain (96), 4 single-hop (841), 5 adversarial (446, `adversarial_answer` instead of `answer`).
- S3. Mapping (assumption, chosen to reuse the LongMemEval harness unchanged): each LoCoMo question becomes a `bench.Question` whose haystack is every session of its conversation, one memory per session, turns rendered as `<speaker>: <text>` with `[image: <blip_caption>]` appended for image turns (the image `query` field is left out); session dates parsed from `session_N_date_time` into the LongMemEval date format; answer sessions from the session number in each `evidence` dia_id; the question date is the date of the conversation's last session. Category 5 questions are abstention questions. `mc-qa -dataset locomo` loads it; `-dataset longmemeval` stays the default.
- S4. Judge (assumption): categories 1-4 use Mem0's LoCoMo LLM-judge prompt verbatim (`mem0ai/mem0` `evaluation/metrics/llm_judge.py` at `aae5989`), counted correct when the verdict says CORRECT and not WRONG; category 5 uses the LongMemEval abstention judge. The reader is the same `-reader official|careful` as LongMemEval, with `gpt-6.1-sol` through Codex. Headline: categories 1-4 (1,540 questions), reported by numeric code; category 5 reported apart.

## Tasks

| ID | Specs | Route | Status | Evidence |
|---|---|---|---|---|
| T1 | S2, S3 | inline | done | `LoadLoCoMo` in `internal/bench/locomo.go`. RED `undefined: LoadLoCoMo`, then GREEN for 2 tests (sessions in numeric order, a lone date key ignored, LongMemEval date format, last session as question date, evidence to sessions, speakers and image captions without the image query, numeric answer, category 5 as abstention; unreadable date named). Real file: 1,986 questions (multi-hop 282, temporal 321, open-domain 96, single-hop 841, adversarial 446), 272 sessions, 4 with no evidence, 0 evidence to an unknown session |
| T2 | S3, S4 | inline | done | `judgeLoCoMo` and `Correct` in `internal/bench/qa.go`, `-dataset` in `cmd/mc-qa`, README. Mem0 prompt extracted with Python `ast` from llm_judge.py at aae5989 and asserted byte-for-byte fragments. RED `undefined: Correct`, then GREEN (JSON label, fallback last CORRECT/WRONG, INCORRECT is wrong, adversarial uses the abstention judge's yes, LongMemEval still only yes; RunQA end to end on a LoCoMo question). `go test ./...` ok. Differential: LongMemEval unsplit report identical to `runs/qa-full-k20.txt`; `-dataset foo` exits 2 |
| T3 | S1, S4 | inline | pending | runs; cost confirmed with the owner first |

## Log

- L1 (2026-11-01): After "Sigamos", offered the dev/test split of LongMemEval or LoCoMo; the owner said "Porque no hacer las 2".
- L2 (2026-11-01): Research (sources: snap-research/locomo, arXiv 2402.17753, mem0 evaluation at aae5989, arXiv 2504.19413, the Penfield Labs audit): the data's category codes differ from the paper's listed order; Mem0 and others drop category 5 and judge with gpt-4o-mini at temperature 0 using a lenient "touches on the same topic" prompt; an audit reports 99 of the 1,540 category 1-4 gold answers wrong (about 6.4 %) and that judge accepting about 63 % of deliberately wrong on-topic answers, so a few points are within noise. With one memory per session a conversation has 19 to 32 sessions, so at k >= 19 the smallest conversations are read whole; k is reported with every score.
