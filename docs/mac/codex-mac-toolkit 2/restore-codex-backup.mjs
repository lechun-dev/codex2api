#!/usr/bin/env node
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import readline from 'node:readline/promises';
import { execFileSync } from 'node:child_process';

function usage() {
  return `Restore Codex files from toolkit backups.

Usage:
  node scripts/restore-codex-backup.mjs [options]

Default mode is dry-run. No files are changed unless --apply is present.

Options:
  --codex-home <path>   Codex home directory. Defaults to CODEX_HOME or ~/.codex
  --backup-dir <path>   Backup directory containing manifest.json, or full snapshot archive
  --snapshot <path>     Full Codex snapshot archive (.tar, .tar.gz, or .zip)
  --apply               Restore files. Without this, only prints a plan
  --yes                 Skip restore confirmation
  --help                Show this help
`;
}

function parseArgs(argv) {
  const args = {
    codexHome: process.env.CODEX_HOME || path.join(os.homedir(), '.codex'),
    backupDir: '',
    snapshot: '',
    apply: false,
    yes: false,
    help: false,
  };

  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === '--help' || arg === '-h') args.help = true;
    else if (arg === '--apply') args.apply = true;
    else if (arg === '--yes') args.yes = true;
    else if (arg === '--codex-home') args.codexHome = requireValue(argv, ++i, arg);
    else if (arg === '--backup-dir') args.backupDir = requireValue(argv, ++i, arg);
    else if (arg === '--snapshot') args.snapshot = requireValue(argv, ++i, arg);
    else throw new Error(`Unknown argument: ${arg}`);
  }

  args.codexHome = path.resolve(expandHome(args.codexHome));
  if (args.backupDir) args.backupDir = path.resolve(expandHome(args.backupDir));
  if (args.snapshot) args.snapshot = path.resolve(expandHome(args.snapshot));
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
    maxBuffer: 16 * 1024 * 1024,
    ...opts,
  });
}

function commandWorks(cmd, args = ['--version']) {
  try {
    run(cmd, args);
    return true;
  } catch {
    return false;
  }
}

function findManifestBackups(codexHome) {
  const root = path.join(codexHome, 'repair_backups');
  if (!fs.existsSync(root)) return [];
  return fs.readdirSync(root, { withFileTypes: true })
    .filter((entry) => entry.isDirectory())
    .map((entry) => path.join(root, entry.name))
    .filter((dir) => fs.existsSync(path.join(dir, 'manifest.json')))
    .map((dir) => {
      const manifest = JSON.parse(fs.readFileSync(path.join(dir, 'manifest.json'), 'utf8'));
      return { dir, manifest, createdAt: manifest.createdAt || path.basename(dir) };
    })
    .sort((a, b) => String(b.createdAt).localeCompare(String(a.createdAt)));
}

function backupTimeMillis(backup) {
  const value = backup?.manifest?.createdAt || backup?.createdAt || '';
  const millis = Date.parse(value);
  if (Number.isFinite(millis)) return millis;
  const match = path.basename(backup?.dir || '').match(/^(\d{14})-/);
  if (!match) return 0;
  const ts = match[1];
  return Date.UTC(
    Number(ts.slice(0, 4)),
    Number(ts.slice(4, 6)) - 1,
    Number(ts.slice(6, 8)),
    Number(ts.slice(8, 10)),
    Number(ts.slice(10, 12)),
    Number(ts.slice(12, 14))
  );
}

function combineManifestBackups(backups) {
  const sorted = [...backups].sort((a, b) => backupTimeMillis(a) - backupTimeMillis(b));
  const entriesBySource = new Map();
  for (const backup of sorted) {
    const entries = Array.isArray(backup.manifest.entries) ? backup.manifest.entries : [];
    for (const entry of entries) {
      if (!entry?.source || entriesBySource.has(entry.source)) continue;
      entriesBySource.set(entry.source, entry);
    }
  }

  const newest = [...sorted].sort((a, b) => backupTimeMillis(b) - backupTimeMillis(a))[0];
  const dirs = sorted.map((item) => item.dir);
  return {
    dir: dirs.join(path.delimiter),
    dirs,
    manifest: {
      createdAt: newest?.manifest?.createdAt || newest?.createdAt || null,
      codexHome: newest?.manifest?.codexHome,
      operationId: newest?.manifest?.operationId || null,
      combined: true,
      backups: dirs,
      entries: [...entriesBySource.values()],
    },
  };
}

function latestBackupOperation(backups) {
  if (!backups.length) return null;
  const latest = backups[0];
  const operationId = latest.manifest?.operationId;
  if (operationId) {
    const group = backups.filter((backup) => backup.manifest?.operationId === operationId);
    return combineManifestBackups(group);
  }

  const latestName = path.basename(latest.dir);
  if (latestName.endsWith('-history-provider')) {
    const latestMs = backupTimeMillis(latest);
    const providerConfig = backups.find((backup) => {
      if (!path.basename(backup.dir).endsWith('-provider-config')) return false;
      const backupMs = backupTimeMillis(backup);
      return backupMs <= latestMs && latestMs - backupMs <= 30 * 60 * 1000;
    });
    if (providerConfig) return combineManifestBackups([providerConfig, latest]);
  }

  return latest;
}

function latestLegacyRepairBackup(codexHome) {
  const root = path.join(codexHome, 'repair_backups');
  if (!fs.existsSync(root)) return null;
  const dirs = fs.readdirSync(root, { withFileTypes: true })
    .filter((entry) => entry.isDirectory())
    .map((entry) => path.join(root, entry.name))
    .sort((a, b) => path.basename(b).localeCompare(path.basename(a)));

  for (const dir of dirs) {
    const entries = [];
    walkFiles(dir, (file) => {
      const rel = path.relative(dir, file);
      if (rel === 'manifest.json') return;
      if (file.endsWith('.backup')) {
        const withoutBackup = rel.slice(0, -'.backup'.length);
        entries.push({
          type: 'file',
          source: path.join(codexHome, withoutBackup),
          backup: file,
        });
      } else {
        entries.push({
          type: 'file',
          source: path.join(codexHome, rel),
          backup: file,
        });
      }
    });
    if (entries.length) {
      return {
        dir,
        manifest: {
          createdAt: path.basename(dir),
          codexHome,
          entries,
          legacyFormat: 'repair_backups-directory',
        },
      };
    }
  }
  return null;
}

function latestLegacySuffixBackup(codexHome) {
  const pattern = /^(?<source>.+)\.(?<timestamp>\d{14})\.(?<suffix>before-provider-lechun|before-history-repair)\.bak$/;
  const candidates = [];
  walkFiles(codexHome, (file) => {
    if (file.includes(`${path.sep}repair_backups${path.sep}`)) return;
    const match = file.match(pattern);
    if (match?.groups?.source) {
      candidates.push({
        timestamp: match.groups.timestamp,
        source: match.groups.source,
        backup: file,
        suffix: match.groups.suffix,
      });
    }
  });
  if (!candidates.length) return null;

  const latestTimestamp = candidates.map((item) => item.timestamp).sort().at(-1);
  const entries = candidates
    .filter((item) => item.timestamp === latestTimestamp)
    .map((item) => ({ type: 'file', source: item.source, backup: item.backup }));

  return {
    dir: `${codexHome}/legacy-suffix-backups/${latestTimestamp}`,
    manifest: {
      createdAt: latestTimestamp,
      codexHome,
      entries,
      legacyFormat: 'timestamp-suffix-bak',
    },
  };
}

function walkFiles(root, visit) {
  if (!fs.existsSync(root)) return;
  const stack = [root];
  while (stack.length) {
    const current = stack.pop();
    for (const entry of fs.readdirSync(current, { withFileTypes: true })) {
      const full = path.join(current, entry.name);
      if (entry.isDirectory()) stack.push(full);
      else if (entry.isFile()) visit(full);
    }
  }
}

function loadBackup(args) {
  if (args.snapshot) return loadFullSnapshot(args, args.snapshot);
  if (args.backupDir) {
    if (fs.existsSync(args.backupDir) && fs.statSync(args.backupDir).isFile()) {
      return loadFullSnapshot(args, args.backupDir);
    }
    const manifestPath = path.join(args.backupDir, 'manifest.json');
    if (fs.existsSync(manifestPath)) {
      return { dir: args.backupDir, manifest: JSON.parse(fs.readFileSync(manifestPath, 'utf8')) };
    }
    const relEntries = [];
    walkFiles(args.backupDir, (file) => {
      const rel = path.relative(args.backupDir, file);
      if (file.endsWith('.backup')) {
        relEntries.push({ type: 'file', source: path.join(args.codexHome, rel.slice(0, -'.backup'.length)), backup: file });
      } else {
        relEntries.push({ type: 'file', source: path.join(args.codexHome, rel), backup: file });
      }
    });
    if (!relEntries.length) throw new Error(`Missing manifest and no restorable files found: ${args.backupDir}`);
    return { dir: args.backupDir, manifest: { createdAt: path.basename(args.backupDir), codexHome: args.codexHome, entries: relEntries, legacyFormat: 'explicit-directory' } };
  }
  const backups = findManifestBackups(args.codexHome);
  if (backups.length) return latestBackupOperation(backups);
  const legacyRepair = latestLegacyRepairBackup(args.codexHome);
  if (legacyRepair) return legacyRepair;
  const legacySuffix = latestLegacySuffixBackup(args.codexHome);
  if (legacySuffix) return legacySuffix;
  const snapshot = latestFullSnapshot(args);
  if (snapshot) return snapshot;
  throw new Error(
    `No manifest, legacy, or full snapshot backups found. Checked ${path.join(args.codexHome, 'repair_backups')} and ${defaultSnapshotDir()}.`
  );
}

function defaultSnapshotDir() {
  return process.env.CODEX_BACKUP_DIR || path.join(os.homedir(), 'CodexBackups');
}

function latestFullSnapshot(args) {
  const backupDir = defaultSnapshotDir();
  if (!fs.existsSync(backupDir)) return null;
  const candidates = fs.readdirSync(backupDir, { withFileTypes: true })
    .filter((entry) => entry.isFile())
    .map((entry) => path.join(backupDir, entry.name))
    .filter((file) => /^codex-home-\d{14}\.(tar|tar\.gz|zip)$/.test(path.basename(file)))
    .map((archive) => {
      const stat = fs.statSync(archive);
      const metadataPath = `${archive}.json`;
      let metadata = {};
      if (fs.existsSync(metadataPath)) {
        try {
          metadata = JSON.parse(fs.readFileSync(metadataPath, 'utf8'));
        } catch {
          metadata = {};
        }
      }
      return { archive, metadataPath: fs.existsSync(metadataPath) ? metadataPath : null, metadata, mtimeMs: stat.mtimeMs, size: stat.size };
    })
    .sort((a, b) => b.mtimeMs - a.mtimeMs);
  if (!candidates.length) return null;
  return snapshotBackupObject(args, candidates[0]);
}

function loadFullSnapshot(args, archive) {
  if (!fs.existsSync(archive)) throw new Error(`Snapshot archive not found: ${archive}`);
  const metadataPath = `${archive}.json`;
  let metadata = {};
  if (fs.existsSync(metadataPath)) {
    metadata = JSON.parse(fs.readFileSync(metadataPath, 'utf8'));
  }
  const stat = fs.statSync(archive);
  return snapshotBackupObject(args, { archive, metadataPath: fs.existsSync(metadataPath) ? metadataPath : null, metadata, mtimeMs: stat.mtimeMs, size: stat.size });
}

function snapshotBackupObject(args, snapshot) {
  const format = snapshot.archive.endsWith('.zip') ? 'zip' : (snapshot.archive.endsWith('.tar.gz') ? 'tar.gz' : 'tar');
  return {
    kind: 'full-snapshot',
    dir: snapshot.archive,
    manifest: {
      createdAt: snapshot.metadata.createdAt || new Date(snapshot.mtimeMs).toISOString(),
      codexHome: snapshot.metadata.codexHome || args.codexHome,
      entries: [],
      fullSnapshot: {
        archive: snapshot.archive,
        metadata: snapshot.metadataPath,
        format,
        size: snapshot.size,
      },
    },
  };
}

function ensureDir(dir) {
  fs.mkdirSync(dir, { recursive: true });
}

function createPreRestoreBackup(codexHome, entries) {
  const ts = new Date().toISOString().replace(/[-:TZ.]/g, '').slice(0, 14);
  const backupRoot = path.join(codexHome, 'repair_backups', `${ts}-before-restore`);
  const manifestEntries = [];
  ensureDir(backupRoot);

  for (const entry of entries) {
    if (!fs.existsSync(entry.source)) continue;
    const rel = path.relative(codexHome, entry.source);
    if (rel.startsWith('..')) continue;
    const backup = path.join(backupRoot, 'files', rel);
    ensureDir(path.dirname(backup));
    fs.copyFileSync(entry.source, backup);
    manifestEntries.push({ type: 'file', source: entry.source, backup });
  }

  fs.writeFileSync(path.join(backupRoot, 'manifest.json'), JSON.stringify({
    createdAt: new Date().toISOString(),
    codexHome,
    entries: manifestEntries,
  }, null, 2) + '\n');
  return backupRoot;
}

function restoreEntry(entry) {
  if (!entry.source || !entry.backup) throw new Error(`Invalid manifest entry: ${JSON.stringify(entry)}`);
  if (!fs.existsSync(entry.backup)) throw new Error(`Backup file missing: ${entry.backup}`);
  ensureDir(path.dirname(entry.source));
  fs.copyFileSync(entry.backup, entry.source);
}

function restoreFullSnapshot(backup, args) {
  const snapshot = backup.manifest.fullSnapshot;
  if (!snapshot?.archive) throw new Error('Invalid full snapshot backup object');
  if (!fs.existsSync(snapshot.archive)) throw new Error(`Snapshot archive missing: ${snapshot.archive}`);

  const currentBackup = moveCurrentCodexHomeAside(args.codexHome);
  const parent = path.dirname(args.codexHome);
  const base = path.basename(args.codexHome);

  if (snapshot.format === 'tar.gz' || snapshot.format === 'tar') {
    const tarCmd = process.platform === 'win32' ? 'tar.exe' : 'tar';
    if (!commandWorks(tarCmd, ['--version'])) throw new Error(`tar was not found; cannot restore ${snapshot.format} snapshot on this machine.`);
    const extractArgs = snapshot.format === 'tar.gz' ? ['-xzf', snapshot.archive, '-C', parent] : ['-xf', snapshot.archive, '-C', parent];
    run(tarCmd, extractArgs);
    if (!fs.existsSync(args.codexHome)) {
      const extracted = path.join(parent, '.codex');
      if (extracted !== args.codexHome && fs.existsSync(extracted)) fs.renameSync(extracted, args.codexHome);
    }
  } else if (snapshot.format === 'zip') {
    ensureDir(args.codexHome);
    expandZipSnapshot(snapshot.archive, args.codexHome);
  } else {
    throw new Error(`Unsupported full snapshot format: ${snapshot.format}`);
  }

  if (!fs.existsSync(args.codexHome)) {
    throw new Error(`Snapshot extraction did not recreate Codex home: ${args.codexHome}. Current backup is at ${currentBackup}`);
  }

  return { restored: 'full-snapshot', archive: snapshot.archive, currentBackup };
}

function moveCurrentCodexHomeAside(codexHome) {
  const ts = new Date().toISOString().replace(/[-:TZ.]/g, '').slice(0, 14);
  const backup = `${codexHome}.before-full-restore-${ts}`;
  if (fs.existsSync(codexHome)) {
    fs.renameSync(codexHome, backup);
  }
  return backup;
}

function expandZipSnapshot(archive, destination) {
  if (process.platform === 'win32') {
    const ps = `
$ErrorActionPreference = 'Stop'
Expand-Archive -LiteralPath $env:CODEX_RESTORE_ARCHIVE -DestinationPath $env:CODEX_RESTORE_DEST -Force
`;
    run('powershell.exe', ['-NoProfile', '-ExecutionPolicy', 'Bypass', '-Command', ps], {
      env: {
        ...process.env,
        CODEX_RESTORE_ARCHIVE: archive,
        CODEX_RESTORE_DEST: destination,
      },
    });
    return;
  }

  if (commandWorks('unzip', ['-v'])) {
    run('unzip', ['-q', archive, '-d', destination]);
    return;
  }

  throw new Error('unzip was not found; cannot restore zip snapshot on this machine.');
}

async function confirmRestore(backupDir, args) {
  if (args.yes) return;
  const rl = readline.createInterface({ input: process.stdin, output: process.stdout });
  const answer = await rl.question(`Restore from ${backupDir}? Type "RESTORE" to continue: `);
  rl.close();
  if (answer !== 'RESTORE') throw new Error('Aborted by user');
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
  if (!fs.existsSync(args.codexHome)) throw new Error(`Codex home does not exist: ${args.codexHome}`);

  const backup = loadBackup(args);
  const entries = Array.isArray(backup.manifest.entries) ? backup.manifest.entries : [];
  const plan = {
    mode: args.apply ? 'apply' : 'dry-run',
    kind: backup.kind || 'manifest',
    codexHome: args.codexHome,
    backupDir: backup.dir,
    backupDirs: backup.dirs || [backup.dir],
    createdAt: backup.manifest.createdAt || null,
    operationId: backup.manifest.operationId || null,
    combined: Boolean(backup.manifest.combined),
    fullSnapshot: backup.manifest.fullSnapshot || null,
    entries: entries.map((entry) => ({ type: entry.type, source: entry.source, backup: entry.backup })),
    notes: [
      'Default dry-run does not change files.',
      'Apply mode creates a before-restore backup first.',
      'Full snapshot restore moves the current Codex home aside before extracting the archive.',
      'Close Codex Desktop before restoring.',
    ],
  };

  if (!args.apply) {
    printJson(plan);
    return;
  }

  await confirmRestore(backup.dir, args);
  if (backup.kind === 'full-snapshot') {
    printJson({ before: plan, restore: restoreFullSnapshot(backup, args) });
    return;
  }
  const preRestoreBackup = createPreRestoreBackup(args.codexHome, entries);
  for (const entry of entries) restoreEntry(entry);
  printJson({ restored: entries.length, backupDir: backup.dir, preRestoreBackup });
}

main().catch((error) => {
  console.error(error.message);
  process.exitCode = 1;
});
