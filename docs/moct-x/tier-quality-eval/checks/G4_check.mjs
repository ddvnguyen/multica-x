// G4 grader: applies up.sql then down.sql on a scratch SQLite db and asserts the round trip.
import { DatabaseSync } from 'node:sqlite';
import fs from 'node:fs';
import path from 'node:path';

const dir = process.argv[2] ?? '.';
const read = (f) => fs.readFileSync(path.join(dir, f), 'utf8');
const fail = (m) => { console.error('FAIL: ' + m); process.exit(1); };

const up = read('up.sql');
const down = read('down.sql');
const db = new DatabaseSync(':memory:');
db.exec(`CREATE TABLE model_tier (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, name TEXT NOT NULL, idx INTEGER NOT NULL);`);
db.exec(`INSERT INTO model_tier (id, workspace_id, name, idx) VALUES ('t0','w1','fast',0),('t1','w1','cheap',1);`);

db.exec(up);
let cols = db.prepare(`PRAGMA table_info(model_tier)`).all().map((c) => c.name);
if (!cols.includes('quality')) fail('up.sql did not add column quality (cols: ' + cols.join(',') + ')');
let rows = db.prepare(`SELECT id, name, idx, quality FROM model_tier ORDER BY idx`).all();
if (rows.length !== 2) fail('up.sql changed the row count: ' + rows.length);
if (String(rows[0].quality) !== '0' || String(rows[1].quality) !== '0') fail('existing rows must read quality 0, got ' + JSON.stringify(rows));
try { db.exec(`INSERT INTO model_tier (id, workspace_id, name, idx) VALUES ('t2','w1','mid',2);`); }
catch (e) { fail('insert omitting quality must succeed (DEFAULT 0): ' + e.message); }
const q = db.prepare(`SELECT quality FROM model_tier WHERE id='t2'`).get();
if (String(q.quality) !== '0') fail('insert omitting quality must default to 0, got ' + q.quality);
db.exec(`UPDATE model_tier SET quality = 73 WHERE id='t0'`);

db.exec(down);
cols = db.prepare(`PRAGMA table_info(model_tier)`).all().map((c) => c.name);
if (cols.includes('quality')) fail('down.sql left column quality behind');
const after = db.prepare(`SELECT id, workspace_id, name, idx FROM model_tier ORDER BY idx`).all();
const want = [
  { id: 't0', workspace_id: 'w1', name: 'fast', idx: 0 },
  { id: 't1', workspace_id: 'w1', name: 'cheap', idx: 1 },
  { id: 't2', workspace_id: 'w1', name: 'mid', idx: 2 },
];
if (JSON.stringify(after) !== JSON.stringify(want)) fail('down.sql lost or changed rows: ' + JSON.stringify(after));
console.log('PASS g4');
