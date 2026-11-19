#!/usr/bin/env node
// Aggregate one tag's runs + rubric into a raw weighted score.
// Usage: node compute.mjs --tag <tag> [--json]
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const args = process.argv.slice(2);
const arg = (n, d) => { const i = args.indexOf(`--${n}`); return i >= 0 ? args[i + 1] : d; };
const tag = arg('tag');
const recs = fs.readFileSync(path.join(here, 'results', `${tag}.jsonl`), 'utf8').trim().split('\n').map((l) => JSON.parse(l));
const rubricFile = path.join(here, 'results', `${tag}.rubric.json`);
const rubric = fs.existsSync(rubricFile) ? JSON.parse(fs.readFileSync(rubricFile, 'utf8')) : {};

const WEIGHT = { G1: 7.5, G2: 7.5, G3: 7.5, G4: 7.5, R1: 6.25, R2: 6.25, R3: 6.25, R4: 6.25, V1: 5, V2: 5, V3: 5, V4: 5, P1: 6.25, P2: 6.25, P3: 6.25, P4: 6.25 };
const CAT = (t) => (t[0] === 'G' ? 'go' : t[0] === 'R' ? 'react' : t[0] === 'V' ? 'review' : 'plan');

const rows = recs.map((r) => {
  const rb = rubric[r.task];
  const score = rb ? rb.score : r.pass ? 1 : 0;
  return { ...r, score, graded_by: rb ? (rb.grader || 'rubric') : 'executable check' };
});

const byCat = {};
let raw = 0;
let tokens = 0;
let cost = 0;
for (const row of rows) {
  const w = WEIGHT[row.task] || 0;
  raw += w * (row.score ?? 0);
  tokens += row.total_tokens || 0;
  cost += row.cost_usd || 0;
  byCat[CAT(row.task)] = byCat[CAT(row.task)] || { n: 0, weigh: 0, score: 0 };
  byCat[CAT(row.task)].n += 1;
  byCat[CAT(row.task)].weigh += w * (row.score ?? 0);
  byCat[CAT(row.task)].score += row.score ?? 0;
}
const out = {
  tag,
  model: rows[0]?.model,
  tasks: rows.length,
  raw_score: Math.round(raw * 100) / 100,
  by_category: Object.fromEntries(Object.entries(byCat).map(([k, v]) => [k, { tasks: v.n, weighted: Math.round(v.weigh * 100) / 100, mean: Math.round((v.score / v.n) * 100) / 100 }])),
  total_tokens: tokens,
  tokens_per_task: rows.length ? Math.round(tokens / rows.length) : 0,
  cost_usd: Math.round(cost * 1e6) / 1e6,
  metered: rows.some((r) => r.metered),
  unmetered_runs: rows.filter((r) => !r.metered).length,
  per_task: rows.map((r) => ({ task: r.task, score: r.score, graded_by: r.graded_by, tokens: r.total_tokens, ms: r.duration_ms, error: r.error ? String(r.error).slice(0, 120) : null })),
};
if (args.includes('--json')) console.log(JSON.stringify(out, null, 2));
else {
  console.log(`${tag} (${out.model}): raw=${out.raw_score} tasks=${out.tasks} tokens/task=${out.tokens_per_task} cost=$${out.cost_usd}`);
  for (const [k, v] of Object.entries(out.by_category)) console.log(`  ${k}: mean=${v.mean} weighted=${v.weighted}`);
  for (const t of out.per_task) console.log(`  ${t.task} ${t.score} ${t.tokens}tok ${Math.round(t.ms / 1000)}s ${t.error ? 'ERR' : ''}`);
}
