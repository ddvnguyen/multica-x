#!/usr/bin/env node
// Run the M-424 quality eval for one model through a uniform headless harness.
// Usage: node run_eval.mjs --model opencode-go/beam-free --tag beam-free [--tasks G1,R1] [--repeat 1]
import { spawn, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const args = process.argv.slice(2);
const arg = (name, def) => {
  const i = args.indexOf(`--${name}`);
  return i >= 0 ? args[i + 1] : def;
};
const model = arg('model');
const tag = arg('tag');
const ALL = ['G1', 'G2', 'G3', 'G4', 'R1', 'R2', 'R3', 'R4', 'V1', 'V2', 'V3', 'V4', 'P1', 'P2', 'P3', 'P4'];
const tasks = (arg('tasks') || ALL.join(',')).split(',').filter(Boolean);
const timeoutMs = Number(arg('timeout', '300')) * 1000;
const repeat = Number(arg('repeat', '1'));
if (!model || !tag) {
  console.error('need --model and --tag');
  process.exit(2);
}

const runsRoot = path.join(here, 'runs', tag);
fs.mkdirSync(runsRoot, { recursive: true });
const resultsFile = path.join(here, 'results', `${tag}.jsonl`);
fs.mkdirSync(path.dirname(resultsFile), { recursive: true });

function runTask(task, attempt) {
  const prompt = fs.readFileSync(path.join(here, 'tasks', task, 'prompt.md'), 'utf8');
  const dir = path.join(runsRoot, attempt > 1 ? `${task}-r${attempt}` : task);
  fs.rmSync(dir, { recursive: true, force: true });
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'TASK.md'), prompt);
  const started = Date.now();
  return new Promise((resolve) => {
    const child = spawn('omp', ['-p', '--no-session', '--mode', 'json', '--auto-approve', '--no-skills', '--no-rules', '--model', model, '--cwd', dir, prompt], {
      stdio: ['ignore', 'pipe', 'pipe'],
      env: { ...process.env },
    });
    let stdout = '';
    let stderr = '';
    let killed = false;
    const timer = setTimeout(() => { killed = true; child.kill('SIGKILL'); }, timeoutMs);
    child.stdout.on('data', (d) => { stdout += d; });
    child.stderr.on('data', (d) => { stderr += d; });
    child.on('close', (code) => {
      clearTimeout(timer);
      fs.writeFileSync(path.join(dir, 'log.jsonl'), stdout);
      fs.writeFileSync(path.join(dir, 'stderr.log'), stderr);
      const rec = parseLog(stdout, task, attempt, model, dir, Date.now() - started, code, killed ? 'timeout' : null);
      const gradeArgs = ['grade.mjs', task, dir];
      const g = spawnGrading(gradeArgs);
      rec.pass = g.pass;
      rec.grade_detail = g.detail;
      fs.appendFileSync(resultsFile, JSON.stringify(rec) + '\n');
      console.error(`[${tag}] ${task}${attempt > 1 ? ` r${attempt}` : ''} pass=${rec.pass} in=${rec.input_tokens} out=${rec.output_tokens} ${Math.round(rec.duration_ms / 1000)}s ${rec.error ? 'ERR ' + rec.error.slice(0, 80) : ''}`);
      resolve(rec);
    });
  });
}

function spawnGrading(gradeArgs) {
  const r = spawnSync(process.execPath, [path.join(here, gradeArgs[0]), gradeArgs[1], gradeArgs[2]], { encoding: 'utf8', timeout: 240000 });
  const line = (r.stdout || '').trim().split('\n').pop() || '{}';
  try {
    const d = JSON.parse(line);
    return { pass: !!d.pass, detail: d.detail };
  } catch {
    return { pass: false, detail: `grader crashed: ${(r.stdout || '') + (r.stderr || '')}`.slice(0, 500) };
  }
}

function parseLog(stdout, task, attempt, model, dir, durationMs, code, killed) {
  const rec = {
    task, attempt, model, dir, duration_ms: durationMs, exit: code, killed,
    input_tokens: 0, output_tokens: 0, cache_read: 0, cache_write: 0, total_tokens: 0,
    cost_usd: 0, metered: false, api_calls: 0, stop_reason: null, error: null, tool_calls: 0, answer_chars: 0,
  };
  const seen = new Set();
  let lastAssistant = null;
  for (const line of stdout.split('\n')) {
    if (!line.startsWith('{')) continue;
    let ev;
    try { ev = JSON.parse(line); } catch { continue; }
    if (ev.type === 'message_end' && ev.message && ev.message.role === 'assistant') {
      const m = ev.message;
      const key = `${m.timestamp}|${m.responseId || ''}`;
      if (m.usage && !seen.has(key)) {
        seen.add(key);
        rec.api_calls += 1;
        rec.input_tokens += m.usage.input || 0;
        rec.output_tokens += m.usage.output || 0;
        rec.cache_read += m.usage.cacheRead || 0;
        rec.cache_write += m.usage.cacheWrite || 0;
        rec.total_tokens += m.usage.totalTokens || 0;
        if (m.usage.cost && typeof m.usage.cost.total === 'number') {
          rec.cost_usd += m.usage.cost.total;
          if (m.usage.cost.total > 0) rec.metered = true;
        }
      }
      lastAssistant = m;
      rec.stop_reason = m.stopReason || rec.stop_reason;
      if (m.errorMessage) rec.error = String(m.errorMessage).slice(0, 400);
      if (Array.isArray(m.content)) {
        rec.answer_chars = m.content.filter((c) => c.type === 'text').map((c) => c.text).join('\n').length;
      }
      if (Array.isArray(m.toolResults)) rec.tool_calls += m.toolResults.length;
    }
    if (ev.type === 'agent_end' && Array.isArray(ev.messages)) {
      for (const m of ev.messages) if (m.role === 'assistant' && Array.isArray(m.toolResults)) rec.tool_calls = Math.max(rec.tool_calls, 0);
    }
  }
  if (rec.input_tokens === 0 && rec.output_tokens === 0 && !rec.error) rec.error = 'no usage reported';
  rec.answer = lastAssistantText(stdout);
  return rec;
}

function lastAssistantText(stdout) {
  let text = '';
  for (const line of stdout.split('\n')) {
    if (!line.startsWith('{')) continue;
    let ev;
    try { ev = JSON.parse(line); } catch { continue; }
    if (ev.type === 'message_end' && ev.message && ev.message.role === 'assistant' && Array.isArray(ev.message.content)) {
      const t = ev.message.content.filter((c) => c.type === 'text').map((c) => c.text).join('\n');
      if (t.trim()) text = t;
    }
  }
  return text;
}

function tool_callsCount(stdout) {
  let n = 0;
  for (const line of stdout.split('\n')) {
    if (line.includes('"type":"tool_start"') || line.includes('"type":"tool_use"')) n += 1;
  }
  return n;
}

(async () => {
  const done = new Set();
  if (fs.existsSync(resultsFile)) {
    for (const line of fs.readFileSync(resultsFile, 'utf8').split('\n')) {
      if (!line.trim()) continue;
      try { const r = JSON.parse(line); done.add(`${r.task}:${r.attempt}`); } catch {}
    }
  }
  for (let attempt = 1; attempt <= repeat; attempt += 1) {
    for (const t of tasks) {
      if (done.has(`${t}:${attempt}`)) { console.error(`[${tag}] ${t} already recorded, skipping`); continue; }
      await runTask(t, attempt);
    }
  }
  console.error('done: ' + resultsFile);
})();
