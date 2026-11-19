#!/usr/bin/env node
// Grade one task output directory. Usage: node grade.mjs <taskId> <dir>
// Prints a single JSON line: {"task","pass","detail"}
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const TOOLS = process.env.EVAL_TOOLS_DIR || path.resolve(here, '../../tools');
const GO = process.env.EVAL_GO_BIN || path.join(TOOLS, 'go/bin/go');
const VITEST = process.env.EVAL_VITEST_BIN || path.join(TOOLS, 'node_modules/.bin/vitest');

const task = process.argv[2];
const dir = path.resolve(process.argv[3] || '.');
const out = (pass, detail) => {
  console.log(JSON.stringify({ task, pass, detail: String(detail).slice(0, 2000) }));
  process.exit(pass ? 0 : 1);
};

const copyCheck = (src, dst) => fs.copyFileSync(path.join(here, 'checks', src), path.join(dir, dst));
const sh = (cmd, args, opts = {}) =>
  spawnSync(cmd, args, { encoding: 'utf8', timeout: opts.timeout ?? 180000, cwd: dir, env: { ...process.env, ...(opts.env || {}) } });

if (/^G[123]$/.test(task)) {
  const goFiles = fs.readdirSync(dir).filter((f) => f.endsWith('.go'));
  if (goFiles.length === 0) out(false, 'no .go file produced');
  copyCheck(`${task}_test.go`, `${task}_test.go`);
  if (!fs.existsSync(path.join(dir, 'go.mod'))) {
    const init = sh(GO, ['mod', 'init', 'evalthing']);
    if (init.status !== 0) out(false, `go mod init failed: ${init.stderr}`);
  }
  const r = sh(GO, ['test', './...'], { env: { GOCACHE: path.join(TOOLS, 'gocache'), GOFLAGS: '-mod=mod', GOPATH: path.join(TOOLS, 'gopath'), HOME: TOOLS } });
  if (r.status === 0) out(true, 'go test ok');
  out(false, `${r.stdout}\n${r.stderr}`);
}

if (task === 'G4') {
  for (const f of ['up.sql', 'down.sql']) {
    if (!fs.existsSync(path.join(dir, f))) out(false, `missing ${f}`);
  }
  const r = sh(process.execPath, [path.join(here, 'checks', 'G4_check.mjs'), dir]);
  if (r.status === 0) out(true, r.stdout.trim());
  out(false, `${r.stdout}\n${r.stderr}`);
}

if (/^R[1-4]$/.test(task)) {
  const wanted = { R1: 'TierList.jsx', R2: 'UseTiersSwitch.jsx', R3: 'TierPopup.jsx', R4: 'QueuedBadge.jsx' }[task];
  if (!fs.existsSync(path.join(dir, wanted))) out(false, `missing ${wanted}`);
  copyCheck(`${task}.test.jsx`, `${task}.test.jsx`);
  copyCheck('vitest.config.js', 'vitest.config.js');
  const nm = path.join(dir, 'node_modules');
  if (!fs.existsSync(nm)) fs.symlinkSync(path.join(TOOLS, 'node_modules'), nm, 'dir');
  fs.writeFileSync(path.join(dir, 'package.json'), JSON.stringify({ name: 'eval-run', private: true, type: 'module' }, null, 2));
  const r = sh(VITEST, ['run', `${task}.test.jsx`], { env: { CI: '1' } });
  if (r.status === 0) out(true, 'vitest ok');
  out(false, `${r.stdout}\n${r.stderr}`);
}

out(false, `unknown task ${task}`);
