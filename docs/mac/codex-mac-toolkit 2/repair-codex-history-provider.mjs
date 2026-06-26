#!/usr/bin/env node
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import readline from 'node:readline/promises';
import { execFileSync } from 'node:child_process';

const MAX_BUFFER = 64 * 1024 * 1024;

function usage() {
  return `Repair Codex history provider metadata and session index.

Usage:
  node scripts/repair-codex-history-provider.mjs [options]

Default mode is dry-run. No files are changed unless --apply is present.

Options:
  --codex-home <path>   Codex home directory. Defaults to CODEX_HOME or ~/.codex
  --provider <name>     Provider to write. Defaults to model_provider from config.toml
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
    else if (arg === '--restore') args.restore = requireValue(argv, ++i, arg);
    else throw new Error(`Unknown argument: ${arg}`);
  }

  args.codexHome = path.resolve(expandHome(args.codexHome));
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

function sqlite(dbFile, sql, args = []) {
  return run('sqlite3', [...args, dbFile, sql]);
}

function sqliteJson(dbFile, sql) {
  const out = run('sqlite3', ['-json', dbFile, sql]).trim();
  return out ? JSON.parse(out) : [];
}

function sqlQuote(value) {
  return `'${String(value).replaceAll("'", "''")}'`;
}

function ensureDir(dir) {
  fs.mkdirSync(dir, { recursive: true });
}

function readConfigProvider(codexHome) {
  const configPath = path.join(codexHome, 'config.toml');
  if (!fs.existsSync(configPath)) return '';
  const content = fs.readFileSync(configPath, 'utf8');
  const match = content.match(/^\s*model_provider\s*=\s*"([^"]+)"\s*$/m);
  return match?.[1] || '';
}

function discoverStateDbs(codexHome) {
  const dirs = [codexHome, path.join(codexHome, 'sqlite')];
  const files = [];
  const skipped = [];
  for (const dir of dirs) {
    if (!fs.existsSync(dir)) continue;
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
      if (!entry.isFile() || !/^state_\d+\.sqlite$/.test(entry.name)) continue;
      const file = path.join(dir, entry.name);
      const status = inspectStateDb(file);
      if (status.usable) files.push(file);
      else skipped.push({ file, reason: status.reason });
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

function readSessionMetas(file) {
  const content = fs.readFileSync(file, 'utf8');
  const lines = content.split(/\n/);
  const metas = [];
  let invalidJsonLines = 0;

  for (let i = 0; i < lines.length; i += 1) {
    const line = lines[i];
    if (!line.trim()) continue;
    try {
      const obj = JSON.parse(line);
      if (obj?.type === 'session_meta' && obj?.payload?.id) {
        metas.push({ index: i, obj });
      }
    } catch {
      invalidJsonLines += 1;
    }
  }

  return { lines, metas, invalidJsonLines };
}

function analyzeRollouts(files, provider) {
  const providerCounts = new Map();
  let metaLines = 0;
  let filesWithoutMeta = 0;
  let filesNeedingProviderRewrite = 0;
  let invalidJsonLines = 0;

  for (const file of files) {
    const parsed = readSessionMetas(file);
    invalidJsonLines += parsed.invalidJsonLines;
    if (!parsed.metas.length) {
      filesWithoutMeta += 1;
      continue;
    }
    let needsRewrite = false;
    for (const meta of parsed.metas) {
      metaLines += 1;
      const current = meta.obj.payload.model_provider || '';
      providerCounts.set(current, (providerCounts.get(current) || 0) + 1);
      if (current !== provider) needsRewrite = true;
    }
    if (needsRewrite) filesNeedingProviderRewrite += 1;
  }

  return {
    files: files.length,
    metaLines,
    filesWithoutMeta,
    invalidJsonLines,
    filesNeedingProviderRewrite,
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

function buildSummary({ codexHome, provider, dbFiles, skippedDbFiles, rolloutFiles }) {
  const sessionIndex = path.join(codexHome, 'session_index.jsonl');
  const dbs = dbFiles.map((dbFile) => analyzeDb(dbFile, provider));
  const mainDb = chooseMainDb(dbs);
  const indexRows = mainDb ? Number(sqlite(mainDb.dbFile, 'SELECT COUNT(*) FROM threads;').trim()) : 0;
  return {
    codexHome,
    provider,
    sessionIndex,
    sessionIndexLines: countLines(sessionIndex),
    targetIndexRows: indexRows,
    dbs,
    skippedDbFiles,
    rollouts: analyzeRollouts(rolloutFiles, provider),
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

function detectCodexProcesses() {
  try {
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
    entries,
  };
  writeManifest(backupRoot, manifest);
  return backupRoot;
}

function rolloutFilesToRewrite(files, provider) {
  return files.filter((file) => {
    const parsed = readSessionMetas(file);
    return parsed.metas.some((meta) => meta.obj.payload.model_provider !== provider);
  });
}

function rewriteRolloutProvider(file, provider) {
  const parsed = readSessionMetas(file);
  let changed = false;
  for (const meta of parsed.metas) {
    if (meta.obj.payload.model_provider !== provider) {
      meta.obj.payload.model_provider = provider;
      parsed.lines[meta.index] = JSON.stringify(meta.obj);
      changed = true;
    }
  }
  if (changed) fs.writeFileSync(file, parsed.lines.join('\n'), 'utf8');
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
  const source = dbInfos.sort((a, b) => b.count - a.count)[0];
  if (!source) return;

  for (const target of dbInfos) {
    if (target.file === source.file || target.count >= source.count) continue;
    const attachName = 'source_threads';
    sqlite(target.file, `
      PRAGMA busy_timeout=10000;
      ATTACH ${sqlQuote(source.file)} AS ${attachName};
      BEGIN IMMEDIATE;
      INSERT INTO main.threads
      SELECT * FROM ${attachName}.threads
      WHERE NOT EXISTS (SELECT 1 FROM main.threads WHERE main.threads.id = ${attachName}.threads.id);
      UPDATE main.threads SET model_provider = ${sqlQuote(provider)} WHERE model_provider <> ${sqlQuote(provider)};
      COMMIT;
      DETACH ${attachName};
    `);
  }
}

function buildSessionIndex(dbFile) {
  const columns = new Set(tableColumns(dbFile));
  const titleExpr = columns.has('title') ? 'substr(title, 1, 240)' : "''";
  const firstExpr = columns.has('first_user_message') ? 'substr(first_user_message, 1, 240)' : "''";
  const previewExpr = columns.has('preview') ? 'substr(preview, 1, 240)' : "''";
  const updatedAtMsExpr = columns.has('updated_at_ms') ? 'updated_at_ms' : 'NULL';
  const rows = sqliteJson(dbFile, `
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

  return rows.map((row) => {
    const updatedMs = Number(row.updated_at_ms || 0) || Number(row.updated_at || 0) * 1000;
    return JSON.stringify({
      id: row.id,
      thread_name: row.title || row.first_user_message || row.preview || 'Untitled',
      updated_at: new Date(updatedMs).toISOString(),
    });
  }).join('\n') + '\n';
}

function writeFileAtomic(file, content) {
  const tmp = `${file}.tmp-${process.pid}`;
  fs.writeFileSync(tmp, content, 'utf8');
  fs.renameSync(tmp, file);
}

function repair({ codexHome, provider, dbFiles, rolloutFiles, sessionIndex }) {
  const toRewrite = rolloutFilesToRewrite(rolloutFiles, provider);
  const backupRoot = createBackup({ codexHome, provider, dbFiles, sessionIndex, rolloutFilesToRewrite: toRewrite });

  syncDatabases(dbFiles, provider);
  let changedRollouts = 0;
  for (const file of toRewrite) {
    if (rewriteRolloutProvider(file, provider)) changedRollouts += 1;
  }

  const mainDb = chooseMainDb(dbFiles.map((dbFile) => analyzeDb(dbFile, provider)));
  if (mainDb) writeFileAtomic(sessionIndex, buildSessionIndex(mainDb.dbFile));

  return { backupRoot, changedRollouts };
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
  const summary = buildSummary({
    codexHome: args.codexHome,
    provider,
    dbFiles,
    skippedDbFiles: discoveredDbs.skipped,
    rolloutFiles,
  });
  const toRewrite = rolloutFilesToRewrite(rolloutFiles, provider);
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
  const repairResult = repair({ codexHome: args.codexHome, provider, dbFiles, rolloutFiles, sessionIndex });
  const after = buildSummary({
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
