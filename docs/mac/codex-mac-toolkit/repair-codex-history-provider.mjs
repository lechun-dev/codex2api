#!/usr/bin/env node
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import readline from 'node:readline/promises';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { finished } from 'node:stream/promises';

const MAX_BUFFER = 64 * 1024 * 1024;
const SCRIPT_DIR = path.dirname(fileURLToPath(import.meta.url));
let SQLITE_BIN = 'sqlite3';
let ROLLOUT_SCAN_PROGRESS = { total: 0, current: 0, bytes: 0, lastPrint: 0 };

function usage() {
  return `Repair Codex history provider metadata and session index.

Usage:
  node scripts/repair-codex-history-provider.mjs [options]

Default mode is dry-run. No files are changed unless --apply is present.

Options:
  --codex-home <path>   Codex home directory. Defaults to CODEX_HOME or ~/.codex
  --provider <name>     Provider to write. Defaults to model_provider from config.toml
  --sqlite-bin <path>   sqlite3 executable. Defaults to SQLITE_BIN, bundled tools/sqlite3.exe, then sqlite3
  --apply               Write changes. Without this, only prints a plan
  --yes                 Skip the interactive confirmation in --apply mode
  --force               Allow --apply while Codex appears to be running
  --restore <dir>       Restore files from a backup directory created by this script
  --help                Show this help

Examples:
  node scripts/repair-codex-history-provider.mjs
  node scripts/repair-codex-history-provider.mjs --provider lechun --apply
  node scripts/repair-codex-history-provider.mjs --restore ~/.codex/repair_backups/20260614120442-history-provider --apply
`;
}

function parseArgs(argv) {
  const args = {
    codexHome: process.env.CODEX_HOME || path.join(os.homedir(), '.codex'),
    provider: '',
    sqliteBin: process.env.SQLITE_BIN || '',
    apply: false,
    yes: false,
    force: false,
    restore: '',
    help: false,
  };

  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === '--help' || arg === '-h') args.help = true;
    else if (arg === '--apply') args.apply = true;
    else if (arg === '--yes') args.yes = true;
    else if (arg === '--force') args.force = true;
    else if (arg === '--codex-home') args.codexHome = requireValue(argv, ++i, arg);
    else if (arg === '--provider') args.provider = requireValue(argv, ++i, arg);
    else if (arg === '--sqlite-bin') args.sqliteBin = requireValue(argv, ++i, arg);
    else if (arg === '--restore') args.restore = requireValue(argv, ++i, arg);
    else throw new Error(`Unknown argument: ${arg}`);
  }

  args.codexHome = path.resolve(expandHome(args.codexHome));
  if (args.sqliteBin) args.sqliteBin = path.resolve(expandHome(args.sqliteBin));
  if (args.restore) args.restore = path.resolve(expandHome(args.restore));
  return args;
}

function requireValue(argv, index, flag) {
  const value = argv[index];
  if (!value || value.startsWith('--')) throw new Error(`${flag} requires a value`);
  return value;
}

function expandHome(value) {
  if (value === '~') return os.homedir();
  if (value.startsWith('~/')) return path.join(os.homedir(), value.slice(2));
  return value;
}

function run(cmd, args, opts = {}) {
  return execFileSync(cmd, args, {
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
    maxBuffer: MAX_BUFFER,
    ...opts,
  });
}

function resolveSqliteBin(requested) {
  const candidates = [];
  if (requested) candidates.push(requested);
  if (process.platform === 'win32') {
    candidates.push(path.join(SCRIPT_DIR, 'tools', 'sqlite3.exe'));
    candidates.push(path.join(path.dirname(SCRIPT_DIR), 'tools', 'sqlite3.exe'));
  }
  candidates.push('sqlite3');

  for (const candidate of candidates) {
    if (!candidate) continue;
    if (candidate.includes(path.sep) || candidate.includes('/') || candidate.includes('\\')) {
      if (fs.existsSync(candidate)) return candidate;
      continue;
    }
    try {
      run(candidate, ['--version']);
      return candidate;
    } catch {
      // Try the next candidate.
    }
  }

  return requested || 'sqlite3';
}

function sqlite(dbFile, sql, args = []) {
  return run(SQLITE_BIN, [...args, dbFile, sql]);
}

function sqliteJson(dbFile, sql) {
  const out = run(SQLITE_BIN, ['-json', dbFile, sql]).trim();
  return out ? JSON.parse(out) : [];
}

function sqlQuote(value) {
  return `'${String(value).replaceAll("'", "''")}'`;
}

function ensureDir(dir) {
  fs.mkdirSync(dir, { recursive: true });
}

function formatBytes(bytes) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = Number(bytes) || 0;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function readConfigProvider(codexHome) {
  const configPath = path.join(codexHome, 'config.toml');
  if (!fs.existsSync(configPath)) return '';
  const content = fs.readFileSync(configPath, 'utf8');
  const match = content.match(/^\s*model_provider\s*=\s*"([^"]+)"\s*$/m);
  return match?.[1] || '';
}

function discoverStateDbs(codexHome) {
  const files = [];
  const skipped = [];

  const stack = [codexHome];
  while (stack.length) {
    const dir = stack.pop();
    if (!fs.existsSync(dir)) continue;
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) {
        if (entry.name === 'repair_backups') continue;
        stack.push(full);
        continue;
      }
      if (!entry.isFile() || !/^state_\d+\.sqlite$/.test(entry.name)) continue;
      const status = inspectStateDb(full);
      if (status.usable) files.push(full);
      else skipped.push({ file: full, reason: status.reason });
    }
  }
  return { files: [...new Set(files)].sort(), skipped };
}

function inspectStateDb(dbFile) {
  try {
    const out = sqlite(dbFile, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='threads';").trim();
    if (out === '1') return { usable: true, reason: '' };
    return { usable: false, reason: 'missing threads table' };
  } catch (error) {
    return { usable: false, reason: error.stderr?.trim() || error.message };
  }
}

function tableColumns(dbFile) {
  return sqliteJson(dbFile, 'PRAGMA table_info(threads);').map((row) => row.name);
}

function requireThreadColumns(dbFile) {
  const columns = new Set(tableColumns(dbFile));
  const required = ['id', 'rollout_path', 'model_provider', 'title', 'updated_at', 'archived'];
  const missing = required.filter((name) => !columns.has(name));
  if (missing.length) {
    throw new Error(`${dbFile} has unsupported threads schema; missing ${missing.join(', ')}`);
  }
  return [...columns];
}

function countLines(file) {
  if (!fs.existsSync(file)) return 0;
  const content = fs.readFileSync(file, 'utf8');
  if (!content) return 0;
  return content.endsWith('\n') ? content.split('\n').length - 1 : content.split('\n').length;
}

function listRollouts(codexHome) {
  const roots = [path.join(codexHome, 'sessions'), path.join(codexHome, 'archived_sessions')];
  const result = [];
  for (const root of roots) {
    if (!fs.existsSync(root)) continue;
    const stack = [root];
    while (stack.length) {
      const current = stack.pop();
      for (const entry of fs.readdirSync(current, { withFileTypes: true })) {
        const full = path.join(current, entry.name);
        if (entry.isDirectory()) stack.push(full);
        else if (entry.isFile() && /^rollout-.*\.jsonl$/.test(entry.name)) result.push(full);
      }
    }
  }
  return result.sort();
}

async function scanSessionMetas(file, provider = '') {
  const providers = new Map();
  let metaLines = 0;
  let invalidJsonLines = 0;
  let needsRewrite = false;
  const fileSize = fs.statSync(file).size;
  let fileBytes = 0;

  const rl = readline.createInterface({
    input: fs.createReadStream(file, { encoding: 'utf8' }),
    crlfDelay: Infinity,
  });

  for await (const line of rl) {
    fileBytes += Buffer.byteLength(line, 'utf8') + 1;
    maybePrintRolloutScanProgress(file, fileBytes, fileSize);
    if (!line.includes('session_meta')) continue;
    try {
      const obj = JSON.parse(line);
      if (obj?.type === 'session_meta' && obj?.payload?.id) {
        metaLines += 1;
        const current = obj.payload.model_provider || '';
        providers.set(current, (providers.get(current) || 0) + 1);
        if (provider && current !== provider) needsRewrite = true;
      }
    } catch {
      invalidJsonLines += 1;
    }
  }

  return { metaLines, invalidJsonLines, needsRewrite, providers };
}

function maybePrintRolloutScanProgress(file, fileBytes, fileSize) {
  const now = Date.now();
  if (now - ROLLOUT_SCAN_PROGRESS.lastPrint < 5000) return;
  ROLLOUT_SCAN_PROGRESS.lastPrint = now;
  const total = ROLLOUT_SCAN_PROGRESS.total || '?';
  const current = ROLLOUT_SCAN_PROGRESS.current || '?';
  console.error(
    `[history] scanning rollout ${current}/${total}: ${path.basename(file)} ` +
    `(${formatBytes(fileBytes)}/${formatBytes(fileSize)}, total ${formatBytes(ROLLOUT_SCAN_PROGRESS.bytes + fileBytes)})`
  );
}

async function analyzeRollouts(files, provider) {
  const providerCounts = new Map();
  let metaLines = 0;
  let filesWithoutMeta = 0;
  let filesNeedingProviderRewrite = 0;
  let invalidJsonLines = 0;
  let scannedBytes = 0;
  ROLLOUT_SCAN_PROGRESS = { total: files.length, current: 0, bytes: 0, lastPrint: 0 };

  for (const file of files) {
    ROLLOUT_SCAN_PROGRESS.current += 1;
    const parsed = await scanSessionMetas(file, provider);
    const fileSize = fs.statSync(file).size;
    scannedBytes += fileSize;
    ROLLOUT_SCAN_PROGRESS.bytes = scannedBytes;
    invalidJsonLines += parsed.invalidJsonLines;
    if (!parsed.metaLines) {
      filesWithoutMeta += 1;
      continue;
    }
    metaLines += parsed.metaLines;
    for (const [current, count] of parsed.providers.entries()) {
      providerCounts.set(current, (providerCounts.get(current) || 0) + count);
    }
    if (parsed.needsRewrite) filesNeedingProviderRewrite += 1;
  }

  return {
    files: files.length,
    metaLines,
    filesWithoutMeta,
    invalidJsonLines,
    filesNeedingProviderRewrite,
    scannedBytes,
    providerCounts: Object.fromEntries([...providerCounts.entries()].sort()),
  };
}

function analyzeDb(dbFile, provider) {
  requireThreadColumns(dbFile);
  const providers = sqliteJson(
    dbFile,
    'SELECT model_provider, archived, COUNT(*) AS count FROM threads GROUP BY model_provider, archived ORDER BY model_provider, archived;'
  );
  const threadCount = Number(sqlite(dbFile, 'SELECT COUNT(*) FROM threads;').trim());
  const nonTargetProviderThreads = Number(sqlite(dbFile, `SELECT COUNT(*) FROM threads WHERE model_provider <> ${sqlQuote(provider)};`).trim());
  const integrity = sqlite(dbFile, 'PRAGMA integrity_check;').trim();
  return { dbFile, threadCount, nonTargetProviderThreads, providers, integrity };
}

async function buildSummary({ codexHome, provider, dbFiles, skippedDbFiles, rolloutFiles }) {
  const sessionIndex = path.join(codexHome, 'session_index.jsonl');
  const dbs = dbFiles.map((dbFile) => analyzeDb(dbFile, provider));
  const indexRows = countUnionThreads(dbFiles);
  return {
    codexHome,
    provider,
    sqliteBin: SQLITE_BIN,
    sessionIndex,
    sessionIndexLines: countLines(sessionIndex),
    targetIndexRows: indexRows,
    dbs,
    skippedDbFiles,
    rollouts: await analyzeRollouts(rolloutFiles, provider),
    codexProcessWarning: detectCodexProcesses(),
  };
}

function chooseMainDb(dbAnalyses) {
  if (!dbAnalyses.length) return null;
  const withMostRows = [...dbAnalyses].sort((a, b) => {
    if (b.threadCount !== a.threadCount) return b.threadCount - a.threadCount;
    const aInSqlite = a.dbFile.includes(`${path.sep}sqlite${path.sep}`) ? 1 : 0;
    const bInSqlite = b.dbFile.includes(`${path.sep}sqlite${path.sep}`) ? 1 : 0;
    return bInSqlite - aInSqlite || a.dbFile.localeCompare(b.dbFile);
  });
  return withMostRows[0];
}

function sqlIdent(value) {
  return `"${String(value).replaceAll('"', '""')}"`;
}

function detectCodexProcesses() {
  try {
    if (process.platform === 'win32') {
      const out = run('tasklist', ['/FO', 'CSV', '/NH']);
      return out
        .split('\n')
        .filter((line) => /^"?(Codex|codex)(\.exe)?"/i.test(line))
        .slice(0, 20);
    }
    const out = run('ps', ['-axo', 'pid=,comm=,args=']);
    return out
      .split('\n')
      .filter((line) => /Codex\.app|\/codex(\s|$)|codex$/.test(line))
      .filter((line) => !line.includes('repair-codex-history-provider.mjs'))
      .slice(0, 20);
  } catch {
    return [];
  }
}

function backupFile(codexHome, backupRoot, file, manifestEntries) {
  if (!fs.existsSync(file)) return null;
  const rel = path.relative(codexHome, file);
  if (rel.startsWith('..')) throw new Error(`Refusing to backup file outside codex home: ${file}`);
  const dest = path.join(backupRoot, 'files', rel);
  ensureDir(path.dirname(dest));
  fs.copyFileSync(file, dest);
  manifestEntries.push({ type: 'file', source: file, backup: dest });
  return dest;
}

function backupSqlite(codexHome, backupRoot, dbFile, manifestEntries) {
  if (!fs.existsSync(dbFile)) return null;
  const rel = path.relative(codexHome, dbFile);
  if (rel.startsWith('..')) throw new Error(`Refusing to backup database outside codex home: ${dbFile}`);
  const dest = path.join(backupRoot, 'sqlite-backups', `${rel.replaceAll(path.sep, '__')}.backup`);
  ensureDir(path.dirname(dest));
  sqlite(dbFile, `.backup ${sqlQuote(dest)}`);
  manifestEntries.push({ type: 'sqlite', source: dbFile, backup: dest });
  return dest;
}

function writeManifest(backupRoot, manifest) {
  fs.writeFileSync(path.join(backupRoot, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n');
}

function createBackup({ codexHome, provider, dbFiles, sessionIndex, rolloutFilesToRewrite }) {
  const ts = new Date().toISOString().replace(/[-:TZ.]/g, '').slice(0, 14);
  const backupRoot = path.join(codexHome, 'repair_backups', `${ts}-history-provider`);
  ensureDir(backupRoot);
  const entries = [];

  backupFile(codexHome, backupRoot, sessionIndex, entries);
  for (const dbFile of dbFiles) backupSqlite(codexHome, backupRoot, dbFile, entries);
  for (const file of rolloutFilesToRewrite) backupFile(codexHome, backupRoot, file, entries);

  const manifest = {
    createdAt: new Date().toISOString(),
    codexHome,
    provider,
    operationId: process.env.CODEX_TOOLKIT_OPERATION_ID || null,
    entries,
  };
  writeManifest(backupRoot, manifest);
  return backupRoot;
}

async function rolloutFilesToRewrite(files, provider) {
  const result = [];
  for (const file of files) {
    const parsed = await scanSessionMetas(file, provider);
    if (parsed.needsRewrite) result.push(file);
  }
  return result;
}

async function rewriteRolloutProvider(file, provider) {
  const tmp = `${file}.tmp-${process.pid}`;
  let changed = false;

  const input = fs.createReadStream(file, { encoding: 'utf8' });
  const output = fs.createWriteStream(tmp, { encoding: 'utf8' });
  const rl = readline.createInterface({ input, crlfDelay: Infinity });

  try {
    for await (const line of rl) {
      let out = line;
      if (line.includes('session_meta')) {
        try {
          const obj = JSON.parse(line);
          if (obj?.type === 'session_meta' && obj?.payload?.id && obj.payload.model_provider !== provider) {
            obj.payload.model_provider = provider;
            out = JSON.stringify(obj);
            changed = true;
          }
        } catch {
          // Preserve malformed lines exactly except for normalized trailing newline.
        }
      }
      if (!output.write(`${out}\n`)) {
        await new Promise((resolve) => output.once('drain', resolve));
      }
    }
  } finally {
    rl.close();
  }

  output.end();
  await finished(output);

  if (changed) fs.renameSync(tmp, file);
  else fs.rmSync(tmp, { force: true });
  return changed;
}

function syncDatabases(dbFiles, provider) {
  for (const dbFile of dbFiles) {
    sqlite(dbFile, `
      PRAGMA busy_timeout=10000;
      BEGIN IMMEDIATE;
      UPDATE threads SET model_provider = ${sqlQuote(provider)} WHERE model_provider <> ${sqlQuote(provider)};
      COMMIT;
    `);
  }

  const dbInfos = dbFiles.map((file) => ({ file, count: Number(sqlite(file, 'SELECT COUNT(*) FROM threads;').trim()) }));
  const primary = dbInfos.sort((a, b) => b.count - a.count)[0];
  if (!primary) return { primary: null, mergedIntoPrimary: 0, mirroredFromPrimary: 0 };

  let mergedIntoPrimary = 0;
  for (const source of dbInfos) {
    if (source.file === primary.file) continue;
    if (mergeMissingThreads(primary.file, source.file, provider)) mergedIntoPrimary += 1;
  }

  let mirroredFromPrimary = 0;
  for (const target of dbInfos) {
    if (target.file === primary.file) continue;
    if (mergeMissingThreads(target.file, primary.file, provider)) mirroredFromPrimary += 1;
  }

  return { primary: primary.file, mergedIntoPrimary, mirroredFromPrimary };
}

function mergeMissingThreads(targetDb, sourceDb, provider) {
  const targetColumns = new Set(tableColumns(targetDb));
  const sourceColumns = new Set(tableColumns(sourceDb));
  const columns = [...targetColumns].filter((column) => sourceColumns.has(column));
  if (!columns.includes('id') || columns.length === 0) return false;

  const columnList = columns.map(sqlIdent).join(', ');
  const selectList = columns.map((column) => `source_threads.threads.${sqlIdent(column)}`).join(', ');
  try {
    sqlite(targetDb, `
      PRAGMA busy_timeout=10000;
      ATTACH ${sqlQuote(sourceDb)} AS source_threads;
      BEGIN IMMEDIATE;
      INSERT INTO main.threads (${columnList})
      SELECT ${selectList}
      FROM source_threads.threads
      WHERE NOT EXISTS (
        SELECT 1 FROM main.threads WHERE main.threads.id = source_threads.threads.id
      );
      UPDATE main.threads SET model_provider = ${sqlQuote(provider)} WHERE model_provider <> ${sqlQuote(provider)};
      COMMIT;
      DETACH source_threads;
    `);
    return true;
  } catch (error) {
    console.error(`[history] skipped thread merge from ${sourceDb} to ${targetDb}: ${error.message}`);
    try {
      sqlite(targetDb, 'ROLLBACK; DETACH source_threads;');
    } catch {
      // Best effort cleanup.
    }
    return false;
  }
}

function readThreadIndexRows(dbFile) {
  const columns = new Set(tableColumns(dbFile));
  const titleExpr = columns.has('title') ? 'substr(title, 1, 240)' : "''";
  const firstExpr = columns.has('first_user_message') ? 'substr(first_user_message, 1, 240)' : "''";
  const previewExpr = columns.has('preview') ? 'substr(preview, 1, 240)' : "''";
  const updatedAtMsExpr = columns.has('updated_at_ms') ? 'updated_at_ms' : 'NULL';
  return sqliteJson(dbFile, `
    SELECT
      id,
      ${titleExpr} AS title,
      ${firstExpr} AS first_user_message,
      ${previewExpr} AS preview,
      updated_at,
      ${updatedAtMsExpr} AS updated_at_ms
    FROM threads
    ORDER BY COALESCE(${updatedAtMsExpr}, updated_at * 1000), id
  `);
}

function buildSessionIndex(dbFiles) {
  const rowsById = new Map();
  for (const dbFile of dbFiles) {
    for (const row of readThreadIndexRows(dbFile)) {
      if (!row.id) continue;
      const updatedMs = Number(row.updated_at_ms || 0) || Number(row.updated_at || 0) * 1000;
      const current = rowsById.get(row.id);
      if (!current || updatedMs >= current.updatedMs) {
        rowsById.set(row.id, { ...row, updatedMs });
      }
    }
  }

  return [...rowsById.values()].sort((a, b) => {
    if (a.updatedMs !== b.updatedMs) return a.updatedMs - b.updatedMs;
    return String(a.id).localeCompare(String(b.id));
  }).map((row) => {
    const updatedMs = Number(row.updated_at_ms || 0) || Number(row.updated_at || 0) * 1000;
    return JSON.stringify({
      id: row.id,
      thread_name: row.title || row.first_user_message || row.preview || 'Untitled',
      updated_at: new Date(updatedMs).toISOString(),
    });
  }).join('\n') + '\n';
}

function countUnionThreads(dbFiles) {
  const ids = new Set();
  for (const dbFile of dbFiles) {
    for (const row of sqliteJson(dbFile, 'SELECT id FROM threads;')) {
      if (row.id) ids.add(row.id);
    }
  }
  return ids.size;
}

function writeFileAtomic(file, content) {
  const tmp = `${file}.tmp-${process.pid}`;
  fs.writeFileSync(tmp, content, 'utf8');
  fs.renameSync(tmp, file);
}

async function repair({ codexHome, provider, dbFiles, rolloutFiles, sessionIndex }) {
  const toRewrite = await rolloutFilesToRewrite(rolloutFiles, provider);
  const backupRoot = createBackup({ codexHome, provider, dbFiles, sessionIndex, rolloutFilesToRewrite: toRewrite });

  const dbSync = syncDatabases(dbFiles, provider);
  let changedRollouts = 0;
  for (const file of toRewrite) {
    if (await rewriteRolloutProvider(file, provider)) changedRollouts += 1;
  }

  if (dbFiles.length) writeFileAtomic(sessionIndex, buildSessionIndex(dbFiles));

  return { backupRoot, changedRollouts, dbSync };
}

function validateSessionIndex(file) {
  if (!fs.existsSync(file)) return { lines: 0, bad: 0, duplicates: 0, uniqueIds: 0 };
  const seen = new Set();
  let lines = 0;
  let bad = 0;
  let duplicates = 0;
  const content = fs.readFileSync(file, 'utf8').trim();
  if (!content) return { lines, bad, duplicates, uniqueIds: 0 };
  for (const line of content.split(/\n/)) {
    lines += 1;
    try {
      const obj = JSON.parse(line);
      if (!obj.id || !obj.thread_name || !obj.updated_at) bad += 1;
      if (seen.has(obj.id)) duplicates += 1;
      seen.add(obj.id);
    } catch {
      bad += 1;
    }
  }
  return { lines, bad, duplicates, uniqueIds: seen.size };
}

function restoreBackup(backupRoot, apply) {
  const manifestPath = path.join(backupRoot, 'manifest.json');
  if (!fs.existsSync(manifestPath)) throw new Error(`Missing backup manifest: ${manifestPath}`);
  const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'));
  if (!Array.isArray(manifest.entries)) throw new Error(`Invalid backup manifest: ${manifestPath}`);

  const plan = manifest.entries.map((entry) => ({
    type: entry.type,
    source: entry.source,
    backup: entry.backup,
  }));

  if (!apply) return { dryRun: true, backupRoot, restorePlan: plan };

  for (const entry of manifest.entries) {
    ensureDir(path.dirname(entry.source));
    if (entry.type === 'sqlite') {
      fs.copyFileSync(entry.backup, entry.source);
    } else if (entry.type === 'file') {
      fs.copyFileSync(entry.backup, entry.source);
    }
  }
  return { restored: manifest.entries.length, backupRoot };
}

async function confirmApply(summary, args) {
  if (args.yes) return;
  const rl = readline.createInterface({ input: process.stdin, output: process.stdout });
  const answer = await rl.question(
    `Apply repair to ${summary.codexHome} with provider "${summary.provider}"? Type "repair" to continue: `
  );
  rl.close();
  if (answer !== 'repair') throw new Error('Aborted by user');
}

function printJson(value) {
  console.log(JSON.stringify(value, null, 2));
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  if (args.help) {
    process.stdout.write(usage());
    return;
  }
  SQLITE_BIN = resolveSqliteBin(args.sqliteBin);

  if (args.restore) {
    const result = restoreBackup(args.restore, args.apply);
    printJson(result);
    return;
  }

  if (!fs.existsSync(args.codexHome)) throw new Error(`Codex home does not exist: ${args.codexHome}`);
  const provider = args.provider || readConfigProvider(args.codexHome);
  if (!provider) {
    throw new Error('Unable to determine provider. Pass --provider <name> or set model_provider in config.toml.');
  }

  const discoveredDbs = discoverStateDbs(args.codexHome);
  const dbFiles = discoveredDbs.files;
  if (!dbFiles.length) {
    throw new Error(
      `No usable state_*.sqlite files found under ${args.codexHome}. Skipped: ${JSON.stringify(discoveredDbs.skipped)}`
    );
  }

  const rolloutFiles = listRollouts(args.codexHome);
  const sessionIndex = path.join(args.codexHome, 'session_index.jsonl');
  const summary = await buildSummary({
    codexHome: args.codexHome,
    provider,
    dbFiles,
    skippedDbFiles: discoveredDbs.skipped,
    rolloutFiles,
  });
  const toRewrite = await rolloutFilesToRewrite(rolloutFiles, provider);
  const plan = {
    mode: args.apply ? 'apply' : 'dry-run',
    ...summary,
    plannedChanges: {
      databasesToUpdate: summary.dbs.filter((db) => db.nonTargetProviderThreads > 0).map((db) => db.dbFile),
      rolloutFilesToRewrite: toRewrite.length,
      rebuildSessionIndex: summary.sessionIndexLines !== summary.targetIndexRows,
    },
    notes: [
      'Default dry-run does not change files.',
      'Close Codex Desktop before --apply when possible. Use --force only if you accept concurrent write risk.',
      'Backups created by --apply can be restored with --restore <backup-dir> --apply.',
    ],
  };

  if (!args.apply) {
    printJson(plan);
    return;
  }

  if (summary.codexProcessWarning.length && !args.force) {
    printJson(plan);
    throw new Error('Codex appears to be running. Close Codex and retry, or pass --force.');
  }

  await confirmApply(summary, args);
  const repairResult = await repair({ codexHome: args.codexHome, provider, dbFiles, rolloutFiles, sessionIndex });
  const after = await buildSummary({
    codexHome: args.codexHome,
    provider,
    dbFiles,
    skippedDbFiles: discoveredDbs.skipped,
    rolloutFiles,
  });
  printJson({
    before: summary,
    repair: repairResult,
    after,
    sessionIndexValidation: validateSessionIndex(sessionIndex),
  });
}

main().catch((error) => {
  console.error(error.message);
  process.exitCode = 1;
});
