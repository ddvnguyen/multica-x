# M-424 — tier quality eval, task set v1 (executable)

The goal is a *measured* `model_tier.quality`: one number per (runtime, model) produced by
running the same fixed task set and grading the outputs mechanically, not by asking a model
to grade itself.

## What runs

* 16 fixed tasks in `tasks/<id>/prompt.md` — 4 Go (G1–G4), 4 React (R1–R4), 4 diff reviews
  (V1–V4, real merged PRs #226 and #250 from `multica-x`), 4 plans (P1–P4).
* Every task runs in its own empty directory with `--no-session`, so no task sees another's
  context. One attempt, no retries, no human hints.
* The uniform harness is `omp` (v18.12.6) in headless JSON mode:
  `omp -p --no-session --mode json --auto-approve --no-skills --no-rules --model <model> --cwd <dir> "<prompt>"`.
  One harness for every model keeps the comparison about the model, not the CLI.
* Checks are **hidden** from the model: they are copied into the run directory only after the
  process exits, so the model cannot read its own grader.

## Grading

| Task | Check | Where |
|---|---|---|
| G1–G3 | `go test` against a held-out `_test.go` in `package claim` (Go 1.23.6, stdlib only) | `grade.mjs` |
| G4 | SQLite round trip via `node:sqlite`: apply `up.sql`, then `down.sql`, assert columns + rows | `checks/G4_check.mjs` |
| R1–R4 | `vitest` + jsdom + `@testing-library/react` against held-out `.test.jsx` | `grade.mjs` |
| V1–V4, P1–P4 | rubric in `checks/rubrics.json`, graded by an independent model (`opencode-go/glm-5.3`, never a model under test); score = 1 (all items), 0.5 (one item missing), 0 | `grade_rubric.mjs` |

Weighted raw score (`compute.mjs`): Go 30 (7.5 each), React 25 (6.25), review 20 (5), plan 25 (6.25).
`quality = a*raw + b` is only published once DeepSeek 4.1 Flash **and** Sonnet 5.5 are both
measured; otherwise raw is published and calibrated quality is “not measured”.

## Prove the checks are real

```
node validate_checks.mjs      # gold solutions in gold/ must pass all 8 executable checks
```
`gold/` also fails when the logic is broken: dropping the idx sort in G1 and the callbacks in R1
both fail their checks, so the suites are not vacuous.

## Run it

```
node run_eval.mjs --model opencode-go/beam-free --tag beam-free [--tasks G1,R1] [--timeout 180]
node grade_rubric.mjs --tag beam-free --grader opencode-go/glm-5.3
node compute.mjs --tag beam-free
```

Environment used for the pilot: Node v24.15.0, Go 1.23.6 (downloaded into `../../tools/go`),
`omp` 18.12.6 with the workspace `opencode-go` provider. `grade.mjs` reads
`EVAL_TOOLS_DIR` / `EVAL_GO_BIN` / `EVAL_VITEST_BIN` if the toolchains live elsewhere.

Runs are resumable: a task already present in `results/<tag>.jsonl` is skipped, so a slow model
can be run in chunks. Raw per-task logs stay in `runs/<tag>/<task>/log.jsonl` (chat + usage) and
are not committed because of size; `results/*.jsonl` holds the same facts per run.
