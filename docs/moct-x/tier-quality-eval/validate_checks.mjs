#!/usr/bin/env node
// Prove every executable check passes on a known-good reference solution.
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const tasks = ['G1', 'G2', 'G3', 'G4', 'R1', 'R2', 'R3', 'R4'];
let bad = 0;
for (const t of tasks) {
  const dir = path.join(here, 'runs', '_gold', t);
  fs.rmSync(dir, { recursive: true, force: true });
  fs.mkdirSync(dir, { recursive: true });
  for (const f of fs.readdirSync(path.join(here, 'gold', t))) {
    fs.copyFileSync(path.join(here, 'gold', t, f), path.join(dir, f));
  }
  const r = spawnSync(process.execPath, [path.join(here, 'grade.mjs'), t, dir], { encoding: 'utf8', timeout: 240000 });
  const line = (r.stdout || '').trim().split('\n').pop() || '{}';
  let d = {};
  try { d = JSON.parse(line); } catch {}
  console.log(`${t}: ${d.pass ? 'PASS' : 'FAIL'} ${d.pass ? '' : (d.detail || r.stderr || '').slice(0, 400)}`);
  if (!d.pass) bad += 1;
}
process.exit(bad ? 1 : 0);
