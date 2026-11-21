# M-424 pilot — measured results (2026-11-20)

Harness: `omp` 18.12.6, one headless process per task, isolated dir, no session reuse, one attempt,
no retries. All models ran on the workspace `opencode-go` provider. Tokens and cost are the
provider usage the harness reports per run (`usage.totalTokens`, `usage.cost.total`), summed across
the task's API calls.

## Measured (full 16-task set)

| Model | Runtime | Tasks | Go /30 | React /25 | Review /20 | Plan /25 | **Raw /100** | Tokens/task | Cost/task | Wall |
|---|---|---|---|---|---|---|---|---|---|---|
| opencode-go/beam-free | omp 18.12.6 | 16/16 | 30.00 | 25.00 | 20.00 | 21.88 | **96.88** | 88,843 | $0.000000 (free tier, usage reported) | 5.3 min |
| opencode-go/muse-spark-1.3-contributor | omp 18.12.6 | 16/16 | 30.00 | 25.00 | 20.00 | 21.88 | **96.88** | 72,829 | $0.002527 | 7.7 min |
| opencode-go/deepseek-v4.1-flash | omp 18.12.6 | 16/16 | 30.00 | 25.00 | 20.00 | 21.88 | **96.88** | 200,941 | $0.005542 | 17.3 min |

Per-task matrix (all three models are identical): G1–G4 pass, R1–R4 pass, V1–V4 scored 1, P1/P3/P4
scored 1, P2 scored 0.5 (each model omitted the disabled-tier "must not open" case from its trigger
rules — the one place the task set separated *behaviour* from the rubric).

## Not measured (and why)

| Model | Runtime | State |
|---|---|---|
| opencode-go/hy4-flash-free | omp | 13/16 tasks ran, no score: run stopped on the harness time budget (P2–P4 missing). 103,121 tokens/task — the most expensive free-tier run observed |
| opencode-go/glm-5.3-flash | omp | 6/16 tasks: G2 failed (legacy/stamped claim rule), R2 exceeded the 120s per-task cap with no usage reported. No score |
| claude-sonnet-5-5 | Claude Code runtime | **not measurable today**: the Claude account is out of monthly spend; `opencode-zen` (the other route) returns `401 API key expired` |
| gpt-6.1-terra, gpt-6-luna (Codex runtime) | codex-cli 0.166.0 | not run: the pilot kept one harness, and the Codex runtime is dispatched through a different CLI |
| claude-opus-5-5, Freebuff, Codebuddy models | — | not run in this pilot |

Calibrated 0–100 quality is therefore **not measured** (`a`,`b` need DeepSeek 4.1 Flash *and*
Sonnet 5.5 measured; one reference exists, the other is unreachable). Publish raw only.

## Caveats per row (blind / contamination / usage provenance)

| Row | Blind? | Contamination / provenance |
|---|---|---|
| beam-free 96.88 (16/16) | Non-blind: the same run authored the checks and executed the grading. G1–G4/R1–R4 grades are mechanical (held-out fixtures), so identity cannot move them; V/P grades are model-graded by `glm-5.3` (never a scored subject) and blind to the tag, but not human. | Usage source: harness-reported provider usage per run, committed in `results/*.jsonl` (`metered=false` → cost is a reported zero; zero metering is not proof of $0 billing). Raw chat/usage logs are not committed, so a repo-alone re-grade of V/P is not possible. |
| muse-spark 96.88 (16/16) | Same as beam-free. | Metered (`metered=true`); usage from the same harness source. |
| deepseek-v4.1-flash 96.88 (16/16) | Same as beam-free. | Metered (`metered=true`); usage from the same harness source. |
| hy4-flash-free 13/16 | Non-blind. | No score: stopped on the harness time budget (P2–P4 missing); no usage reported on stopped tasks. |
| glm-5.3-flash 6/16 | Non-blind. | No score: G2 failed, R2 hit the per-task cap with no usage. |
| G1 probes (M-424 record) | Non-blind. | M-437/M-438 discarded for exposure; M-428 contaminated; Freebuff and repeat rows non-blind; per-run identity/usage for Beam and Freebuff incomplete. Full caveats live on M-424. |

## Findings

1. **Ceiling effect — the task set does not rank the cheap/free tier.** Three different providers'
   models all land on 96.88 because 15 of 16 tasks are fully specified: the prompt states the
   contract (types, signatures, accessible names, headings), so "solves it" is the expected outcome
   for any competent model. A quality *ranking* needs tasks whose specification leaves room to fail.
2. **Cost is what actually separates them, by 2.8×.** Same quality ceiling, tokens/task 72.8k →
   200.9k (muse-spark → DeepSeek 4.1 Flash), cost/task $0 → $0.0055. On this evidence
   `opencode-go/muse-spark-1.3-contributor` is the best cheap-tier pick and `beam-free` is strictly
   cheaper at equal measured quality.
3. **The Owner's anchors do not reproduce.** DeepSeek 4.1 Flash was anchored at 68 and muse-spark at
   60 (an 8-point gap); here they are indistinguishable on quality and DeepSeek burns 2.8× the
   tokens. Either the anchor gap is smaller than recorded, or it lives in task types this set does
   not test. Do not copy the anchors into `model_tier.quality` — this is the first run that gives a
   reason to doubt them.
4. **The review/plan rubric is not the bottleneck.** Manual spot check of muse-spark's V3 answer:
   it walked the down-migration against parked rows, saw the `NOT VALID` skip, named the
   `convalidated` divergence, and gave a guard-first fix plus a seeded round-trip test. The
   independent grader marked 3/3 for a genuinely strong answer — leniency was not the cause of the
   flat scores.
5. **Go/React fixtures are contract-level, not repo-level.** They compile a stdlib-only `package
   claim` and render a component; they do not prove the change works inside `multica-x` (no repo
   checkout, no Postgres, no CI). Treat a pass as "the specified decision logic is correct", never
   as "the card is deliverable".

## Verification performed

* `node validate_checks.mjs` → G1,G2,G3,G4,R1,R2,R3,R4 all PASS on the reference solutions in `gold/`.
* Negative controls: G1 with the idx sort removed → FAIL (`want a,true got c,true`); R1 with the
  `onEdit` wiring removed → FAIL. The checks are not vacuous.
* Hidden checks are copied in after the process exits; the model never sees them.
* Every number above comes from `results/<tag>.jsonl`, written by the harness from the provider
  usage field; nothing is estimated. Cost `$0.000000` for beam-free is a reported zero
  (`metered=false` on all 16 rows), not missing usage data — but a zero metering bucket is not
  proof of $0 billing; treat it as "not metered", not "free".
* Rubric scores come from an independent model (never a model under test) with mechanical
  item-counting: 1 = all items hit, 0.5 = one missing, 0 = otherwise. Human confirmation is still
  outstanding — the spec's V/P tasks were meant to be human-graded.

## Cheapest next steps (in order)

1. **Harden the task set before spending on paid tiers.** Replace the contract-complete prompts with
   held-out variants: same cards, but give the failing test as the only spec (no signature, no
   accessible names) and grade the behaviour. That is the only change here that creates ranking
   signal, and it costs no tokens beyond a re-run.
2. **Install the two missing reference routes** so calibration can exist: a working `opencode-zen`
   key (or another Sonnet 5.5 route) and the Codex runtime through the same harness. Until then
   `model_tier.quality` stays "not measured" for every tier.
3. **Finish hy4-flash-free and glm-5.3-flash** with the resumable driver
   (`node run_eval.mjs --model … --tasks P2,P3,P4`); both stalled on the 120–180s per-task cap, so
   raise `--timeout` for those two and let them run one session at a time.
4. **Repeat the whole pilot once** for repeatability (Architect's bar) — the driver is idempotent per
   task, so use `--repeat 2` on the hardened set, not this one.
