#!/usr/bin/env node
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import readline from 'node:readline/promises';
import { execFileSync, spawn } from 'node:child_process';

function usage() {
  return `Configure Codex to use the Lechun provider.

Usage:
  LECHUN_API_KEY=... node scripts/configure-lechun-provider.mjs [options]

Default mode is dry-run. No files are changed unless --apply is present.

Options:
  --codex-home <path>   Codex home directory. Defaults to CODEX_HOME or ~/.codex
  --provider <name>     Provider name. Defaults to lechun
  --base-url <url>      Provider base URL. Defaults to https://codexapi.lechun.cc
  --model <model>       Model name. Defaults to gpt-5.5
  --reasoning <level>   Reasoning effort. Defaults to high
  --backup-dir <path>   Full Codex snapshot directory. Defaults to ~/CodexBackups
  --compressed-backup   Use gzip-compressed tar.gz for the full snapshot. Default is faster uncompressed tar
  --no-full-backup      Do not create a full Codex home snapshot before apply
  --apply               Write config.toml. Without this, only prints a plan
  --yes                 Skip apply confirmation
  --force               Allow --apply while Codex appears to be running
  --help                Show this help
`;
}

function parseArgs(argv) {
  const args = {
    codexHome: process.env.CODEX_HOME || path.join(os.homedir(), '.codex'),
    provider: 'lechun',
    baseUrl: process.env.BASE_URL || 'https://codexapi.lechun.cc',
    model: process.env.MODEL || 'gpt-5.5',
    reasoning: process.env.REASONING_EFFORT || 'high',
    backupDir: process.env.CODEX_BACKUP_DIR || path.join(os.homedir(), 'CodexBackups'),
    fullBackup: true,
    compressedBackup: process.env.CODEX_COMPRESSED_BACKUP === '1',
    apiKey: process.env.LECHUN_API_KEY || process.env.API_KEY || '',
    apply: false,
    yes: false,
    force: false,
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
    else if (arg === '--base-url') args.baseUrl = requireValue(argv, ++i, arg);
    else if (arg === '--model') args.model = requireValue(argv, ++i, arg);
    else if (arg === '--reasoning') args.reasoning = requireValue(argv, ++i, arg);
    else if (arg === '--backup-dir') args.backupDir = requireValue(argv, ++i, arg);
    else if (arg === '--compressed-backup') args.compressedBackup = true;
    else if (arg === '--no-full-backup') args.fullBackup = false;
    else throw new Error(`Unknown argument: ${arg}`);
  }

  args.codexHome = path.resolve(expandHome(args.codexHome));
  args.backupDir = path.resolve(expandHome(args.backupDir));
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

function statSize(file) {
  try {
    return fs.statSync(file).size;
  } catch {
    return null;
  }
}

function runLong(cmd, args, { archive, label, env } = {}) {
  return new Promise((resolve, reject) => {
    const started = Date.now();
    let stderr = '';
    const child = spawn(cmd, args, {
      stdio: ['ignore', 'ignore', 'pipe'],
      env: env || process.env,
      windowsHide: false,
    });

    const timer = setInterval(() => {
      const elapsed = Math.round((Date.now() - started) / 1000);
      const size = archive ? statSize(archive) : null;
      const sizeText = size === null ? 'archive not visible yet' : `${formatBytes(size)} written`;
      console.error(`[snapshot] ${label}: ${sizeText}, ${elapsed}s elapsed`);
    }, 5000);

    child.stderr.on('data', (chunk) => {
      stderr += chunk.toString();
    });

    child.on('error', (error) => {
      clearInterval(timer);
      reject(error);
    });

    child.on('close', (code) => {
      clearInterval(timer);
      if (code === 0) {
        const size = archive ? statSize(archive) : null;
        if (archive) console.error(`[snapshot] ${label}: finished${size === null ? '' : `, final size ${formatBytes(size)}`}`);
        resolve();
        return;
      }
      reject(new Error(`${cmd} failed with exit code ${code}${stderr ? `: ${stderr.trim()}` : ''}`));
    });
  });
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
    return run('ps', ['-axo', 'pid=,comm=,args='])
      .split('\n')
      .filter((line) => /Codex\.app|\/codex(\s|$)|codex$/.test(line))
      .filter((line) => !line.includes('configure-lechun-provider.mjs'))
      .slice(0, 20);
  } catch {
    return [];
  }
}

function maskSecret(value) {
  if (!value) return '';
  if (value.length <= 8) return '********';
  return `${value.slice(0, 4)}...${value.slice(-4)}`;
}

function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function removeSection(text, section) {
  return text.replace(new RegExp(`^\\[${escapeRegExp(section)}\\]\\r?\\n[\\s\\S]*?(?=^\\[|(?![\\s\\S]))`, 'gm'), '');
}

function setTopLevel(text, key, value) {
  const line = `${key} = "${value.replaceAll('"', '\\"')}"`;
  const pattern = new RegExp(`^${escapeRegExp(key)}\\s*=.*$`, 'm');
  if (pattern.test(text)) return text.replace(pattern, line);
  const firstSection = text.search(/^\[/m);
  if (firstSection >= 0) return `${text.slice(0, firstSection)}${line}\n${text.slice(firstSection)}`;
  return `${text.trimEnd()}\n${line}\n`;
}

function ensureHistory(text) {
  const pattern = /^\[history\]\r?\n[\s\S]*?(?=^\[|(?![\s\S]))/m;
  const match = text.match(pattern);
  if (!match) return `${text.trimEnd()}\n\n[history]\npersistence = "save-all"\n`;
  const section = match[0];
  const updated = /^persistence\s*=/m.test(section)
    ? section.replace(/^persistence\s*=.*$/m, 'persistence = "save-all"')
    : `${section.trimEnd()}\npersistence = "save-all"\n`;
  return text.slice(0, match.index) + updated + text.slice(match.index + section.length);
}

function buildConfig(original, args) {
  let text = original;
  text = text.replace(/^\s*disable_response_storage\s*=\s*true\s*(?:#.*)?\r?\n?/gm, '');
  text = removeSection(text, `model_providers.${args.provider}`);
  text = setTopLevel(text, 'model_provider', args.provider);
  text = setTopLevel(text, 'model', args.model);
  text = setTopLevel(text, 'model_reasoning_effort', args.reasoning);
  text = ensureHistory(text);

  const providerBlock = `
[model_providers.${args.provider}]
name = "${args.provider}"
wire_api = "responses"
requires_openai_auth = true
base_url = "${args.baseUrl}"
experimental_bearer_token = "${args.apiKey}"
`;

  return `${text.trimEnd()}\n${providerBlock}`;
}

function ensureDir(dir) {
  fs.mkdirSync(dir, { recursive: true });
}

function backupConfig(codexHome, configPath, provider) {
  const ts = new Date().toISOString().replace(/[-:TZ.]/g, '').slice(0, 14);
  const backupRoot = path.join(codexHome, 'repair_backups', `${ts}-provider-config`);
  const backupPath = path.join(backupRoot, 'files', 'config.toml');
  ensureDir(path.dirname(backupPath));
  fs.copyFileSync(configPath, backupPath);
  fs.writeFileSync(path.join(backupRoot, 'manifest.json'), JSON.stringify({
    createdAt: new Date().toISOString(),
    codexHome,
    provider,
    operationId: process.env.CODEX_TOOLKIT_OPERATION_ID || null,
    entries: [{ type: 'file', source: configPath, backup: backupPath }],
  }, null, 2) + '\n');
  return backupRoot;
}

function isInsidePath(child, parent) {
  const rel = path.relative(parent, child);
  return rel === '' || (!!rel && !rel.startsWith('..') && !path.isAbsolute(rel));
}

async function createFullSnapshot(args) {
  if (!args.fullBackup) return null;
  if (isInsidePath(args.backupDir, args.codexHome)) {
    throw new Error(`Refusing to write full backup inside Codex home: ${args.backupDir}`);
  }

  ensureDir(args.backupDir);
  const ts = new Date().toISOString().replace(/[-:TZ.]/g, '').slice(0, 14);
  const parent = path.dirname(args.codexHome);
  const base = path.basename(args.codexHome);
  const excludes = [`${base}/repair_backups`];
  console.error(`[snapshot] Creating full Codex home snapshot from ${args.codexHome}`);
  console.error(`[snapshot] Destination directory: ${args.backupDir}`);
  console.error('[snapshot] This can take several minutes for large history directories.');

  const tarCmd = process.platform === 'win32' ? 'tar.exe' : 'tar';
  if (commandWorks(tarCmd, ['--version'])) {
    const format = args.compressedBackup ? 'tar.gz' : 'tar';
    const archive = path.join(args.backupDir, `codex-home-${ts}.${format}`);
    const tarArgs = args.compressedBackup ? ['-czf', archive] : ['-cf', archive];
    await runLong(tarCmd, [
      ...tarArgs,
      '-C',
      parent,
      ...excludes.map((item) => `--exclude=${item}`),
      base,
    ], { archive, label: `${format} backup` });

    const metadata = writeSnapshotMetadata({ archive, args, excludes, format, restoreHint: `Extract ${archive} into ${parent}` });
    return { archive, metadata, excluded: excludes, format };
  }

  if (process.platform !== 'win32') {
    throw new Error('tar was not found; install tar or pass --no-full-backup if you have another full backup.');
  }

  const archive = path.join(args.backupDir, `codex-home-${ts}.zip`);
  await createWindowsZipSnapshot({ codexHome: args.codexHome, archive });
  const metadata = writeSnapshotMetadata({
    archive,
    args,
    excludes: ['repair_backups'],
    format: 'zip',
    restoreHint: `Extract ${archive} contents into ${args.codexHome}`,
  });
  return { archive, metadata, excluded: ['repair_backups'], format: 'zip' };
}

function writeSnapshotMetadata({ archive, args, excludes, format, restoreHint }) {
  const metadata = `${archive}.json`;
  fs.writeFileSync(metadata, JSON.stringify({
    createdAt: new Date().toISOString(),
    codexHome: args.codexHome,
    archive,
    format,
    excluded: excludes,
    reason: 'before-provider-config',
    restoreHint,
  }, null, 2) + '\n');
  return metadata;
}

async function createWindowsZipSnapshot({ codexHome, archive }) {
  const ps = `
$ErrorActionPreference = 'Stop'
$src = $env:CODEX_SNAPSHOT_SRC
$archive = $env:CODEX_SNAPSHOT_ARCHIVE
$exclude = Join-Path $src 'repair_backups'
if (Test-Path -LiteralPath $archive) {
  Remove-Item -LiteralPath $archive -Force
}
$items = @(Get-ChildItem -LiteralPath $src -Force | Where-Object { $_.FullName -ne $exclude })
if ($items.Count -eq 0) {
  throw "Codex home is empty: $src"
}
Compress-Archive -LiteralPath $items.FullName -DestinationPath $archive -CompressionLevel Optimal -Force
`;
  await runLong('powershell.exe', ['-NoProfile', '-ExecutionPolicy', 'Bypass', '-Command', ps], {
    archive,
    label: 'zip backup',
    env: {
      ...process.env,
      CODEX_SNAPSHOT_SRC: codexHome,
      CODEX_SNAPSHOT_ARCHIVE: archive,
    },
  });
}

function readAuthSignature(codexHome) {
  const authPath = path.join(codexHome, 'auth.json');
  if (!fs.existsSync(authPath)) return null;
  const st = fs.statSync(authPath);
  return `${st.size}:${st.mtimeMs}`;
}

async function confirmApply(args) {
  if (args.yes) return;
  const rl = readline.createInterface({ input: process.stdin, output: process.stdout });
  const answer = await rl.question(`Apply provider config to ${args.codexHome}? Type "CONFIGURE" to continue: `);
  rl.close();
  if (answer !== 'CONFIGURE') throw new Error('Aborted by user');
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
  if (!args.apiKey) throw new Error('Missing API key. Set LECHUN_API_KEY or API_KEY before running.');

  const configPath = path.join(args.codexHome, 'config.toml');
  if (!fs.existsSync(configPath)) throw new Error(`Config not found: ${configPath}`);

  const original = fs.readFileSync(configPath, 'utf8');
  const next = buildConfig(original, args);
  const processWarning = detectCodexProcesses();
  const plan = {
    mode: args.apply ? 'apply' : 'dry-run',
    codexHome: args.codexHome,
    configPath,
    provider: args.provider,
    baseUrl: args.baseUrl,
    model: args.model,
    reasoning: args.reasoning,
    apiKey: maskSecret(args.apiKey),
    configWillChange: next !== original,
    fullBackup: args.fullBackup ? {
      enabled: true,
      backupDir: args.backupDir,
      excludes: ['repair_backups'],
      format: args.compressedBackup ? 'tar.gz' : 'tar',
      note: 'A full Codex home snapshot is created before apply. Default format is faster uncompressed tar. It contains Codex auth, config, history, logs, and local state; do not share it.',
    } : { enabled: false },
    codexProcessWarning: processWarning,
    notes: [
      'Default dry-run does not change files.',
      'Close Codex Desktop before --apply when possible. Use --force only if you accept concurrent write risk.',
      'config.toml will contain the API key after apply. Do not share it.',
    ],
  };

  if (!args.apply) {
    printJson(plan);
    return;
  }
  if (processWarning.length && !args.force) {
    printJson(plan);
    throw new Error('Codex appears to be running. Close Codex and retry, or pass --force.');
  }

  await confirmApply(args);
  const authBefore = readAuthSignature(args.codexHome);
  const fullSnapshot = await createFullSnapshot(args);
  console.error('[config] Backing up config.toml and writing provider configuration...');
  const backupRoot = backupConfig(args.codexHome, configPath, args.provider);
  fs.writeFileSync(configPath, next, 'utf8');
  const authAfter = readAuthSignature(args.codexHome);
  if (authBefore !== authAfter) throw new Error(`auth.json changed unexpectedly under ${args.codexHome}`);

  printJson({
    before: plan,
    after: {
      configPath,
      fullSnapshot,
      backupRoot,
      configChanged: next !== original,
      authUnchanged: true,
    },
  });
}

main().catch((error) => {
  console.error(error.message);
  process.exitCode = 1;
});
