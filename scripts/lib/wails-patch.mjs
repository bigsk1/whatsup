import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { chmodSync, closeSync, copyFileSync, existsSync, lstatSync, mkdirSync, openSync, readFileSync, readdirSync, renameSync, rmSync, unlinkSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { verifyTailcatPatch } from './tailcat-patch.mjs';

const moduleName = 'github.com/wailsapp/wails/v3';
const moduleVersion = 'v3.1.0';
const replacementPath = './.local/modules/wails-v3';
const upstreamSum = 'h1:ld0AXSicXiTTsIvmTq3qNn9FzCP8vIHRSJdkRLgaAo8=';
const upstreamGoModSum = 'h1:tzDY3W1jzN3ksSqfGalTt7K3PwnGfIqtl+IgIXlXz0I=';
const verified = new WeakSet();
const digest = (bytes) => createHash('sha256').update(bytes).digest('hex');
const sha256 = /^[a-f0-9]{64}$/;
function fail() { throw new Error('Reviewed Wails native patch is missing, changed or unsafe; run npm run setup only after reviewing its patch and manifest.'); }
function file(filename, maximum = 8 << 20) { const s = lstatSync(filename); if (!s.isFile() || s.isSymbolicLink() || s.size > maximum) fail(); return readFileSync(filename); }
function directory(filename) { const s = lstatSync(filename); if (!s.isDirectory() || s.isSymbolicLink()) fail(); }
function safeName(name) { return typeof name === 'string' && name.length > 0 && name.length <= 1024 && !/[\\:\x00-\x1f]/.test(name) && !name.startsWith('/') && name.split('/').every((p) => p && p !== '.' && p !== '..'); }
function privateDirectory(root, relative) {
  directory(root);
  let current = root;
  for (const component of relative.split('/')) { current = path.join(current, component); if (!existsSync(current)) mkdirSync(current, { mode: 0o700 }); directory(current); }
  return current;
}

// Include every regular file, including upstream tests/licenses and generated JS.
// No cache marker can waive this full inventory check at a subprocess boundary.
export function wailsTreeInventory(location) {
  directory(location);
  const entries = [];
  function visit(relative) {
    const current = path.join(location, relative);
    directory(current);
    for (const name of readdirSync(current).sort()) {
      const next = relative ? `${relative}/${name}` : name;
      if (!safeName(next)) fail();
      const info = lstatSync(path.join(location, next));
      if (info.isSymbolicLink()) fail();
      if (info.isDirectory()) visit(next);
      else if (info.isFile()) { entries.push(next); if (entries.length > 10000) fail(); }
      else fail();
    }
  }
  visit('');
  entries.sort();
  const hash = createHash('sha256'); let total = 0;
  const caseNames = new Set();
  for (const name of entries) {
    if (caseNames.has(name.toLowerCase())) fail(); caseNames.add(name.toLowerCase());
    const bytes = file(path.join(location, name)); total += bytes.length; if (total > (64 << 20)) fail();
    hash.update(`${name}\0${bytes.length}\0`); hash.update(bytes);
  }
  return { sha256: hash.digest('hex'), count: entries.length, size: total, files: entries };
}

function sourceDefinition(root, checkSums) {
  try {
    root = path.resolve(root); directory(root); directory(path.join(root, 'config')); directory(path.join(root, 'third_party')); directory(path.join(root, 'third_party/patches'));
    const manifestBytes = file(path.join(root, 'config/wails-patch.json'), 128 << 10);
    const m = JSON.parse(manifestBytes.toString('utf8'));
    if (m.schemaVersion !== 1 || m.module !== moduleName || m.version !== moduleVersion || m.patchVersion !== 1 || m.replacement !== replacementPath
      || m.upstreamSum !== upstreamSum || m.upstreamGoModSum !== upstreamGoModSum
      || JSON.stringify(m.purpose) !== JSON.stringify(['native_only_file_drop', 'completed_reader_response'])
      || !sha256.test(m.upstreamTree?.sha256) || !sha256.test(m.patchedTree?.sha256)
      || !Number.isSafeInteger(m.upstreamTree?.count) || !Number.isSafeInteger(m.patchedTree?.count)
      || m.upstreamTree.count < 1 || m.patchedTree.count < 1 || m.patchedTree.count > 10000
      || !Number.isSafeInteger(m.upstreamTree?.size) || !Number.isSafeInteger(m.patchedTree?.size)
      || m.upstreamTree.size < 1 || m.patchedTree.size < 1 || m.patchedTree.size > (64 << 20)
      || m.patch?.path !== 'third_party/patches/wails-v3-native.patch' || !sha256.test(m.patch?.sha256)
      || !Number.isSafeInteger(m.patch?.size) || m.patch.size < 1 || m.patch.size > (1 << 20)) fail();
    const patch = file(path.join(root, m.patch.path), 1 << 20);
    if (patch.length !== m.patch.size || digest(patch) !== m.patch.sha256) fail();
    const mod = file(path.join(root, 'go.mod'), 1 << 20).toString('utf8');
    const replacements = mod.match(/^\s*replace\b.*$/gm) ?? [];
    const expected = [`replace ${moduleName} => ${replacementPath}`, 'replace github.com/tailscale/tailcat => ./third_party/tailcat'];
    if (replacements.length !== expected.length || expected.some((line) => !replacements.some((found) => found.trim() === line))
      || !mod.split(/\r?\n/).some((line) => line.trim() === `${moduleName} ${moduleVersion}`)) fail();
    if (checkSums) {
      const sums = file(path.join(root, 'go.sum'), 1 << 20).toString('utf8').split(/\r?\n/);
      if (!sums.includes(`${moduleName} ${moduleVersion} ${upstreamSum}`) || !sums.includes(`${moduleName} ${moduleVersion}/go.mod ${upstreamGoModSum}`)) fail();
    }
    return { manifest: m, manifestSHA256: digest(manifestBytes), patch };
  } catch { fail(); }
}
export function verifyWailsDefinition(root) { return sourceDefinition(root, true); }

export function verifyWailsPatch(root) {
  try {
    const definition = verifyWailsDefinition(root);
    for (const name of ['.local', '.local/modules', '.local/modules/wails-v3']) directory(path.join(root, name));
    const inventory = wailsTreeInventory(path.join(root, replacementPath));
    const expected = definition.manifest.patchedTree;
    if (inventory.sha256 !== expected.sha256 || inventory.count !== expected.count || inventory.size !== expected.size) fail();
    const mod = file(path.join(root, replacementPath, 'go.mod')).toString('utf8');
    if (!/^module github\.com\/wailsapp\/wails\/v3\r?$/m.test(mod) || /^\s*replace\b/m.test(mod)) fail();
    const approval = Object.freeze({ module: moduleName, version: moduleVersion, path: replacementPath, root: path.resolve(root), patchVersion: 1, manifestSHA256: definition.manifestSHA256, treeSHA256: inventory.sha256 });
    verified.add(approval); return approval;
  } catch { fail(); }
}
export function isVerifiedWailsPatch(value) { return value !== null && typeof value === 'object' && verified.has(value); }
export function isApprovedWailsReplacement(module, version, target, approval) { return isVerifiedWailsPatch(approval) && module === approval.module && version === approval.version && target === approval.path; }

// Deliberately supports only exact text additions/changes, never fuzz, binary
// patches, renames, deletions, absolute paths or an external patch executable.
export function applyWailsTextPatch(location, patch) {
  const lines = patch.toString('utf8').split('\n'); let index = 0;
  const seen = new Set();
  while (index < lines.length - 1) {
    const old = lines[index++]; const next = lines[index++];
    if (!old?.startsWith('--- ') || !next?.startsWith('+++ b/')) fail();
    const name = next.slice(6); if (!safeName(name) || seen.has(name) || !/\.(?:go|c|h)$/.test(name) || (old !== '--- /dev/null' && old !== `--- a/${name}`)) fail(); seen.add(name);
    const filename = path.join(location, name); let original = [];
    if (old !== '--- /dev/null') { const text = file(filename).toString('utf8'); if (!text.endsWith('\n') || text.includes('\r')) fail(); original = text.slice(0, -1).split('\n'); }
    else {
      if (existsSync(filename)) fail();
      const parent = path.dirname(name).replaceAll('\\', '/');
      if (parent !== '.') privateDirectory(location, parent);
    }
    let cursor = 0; const output = []; let hunks = 0;
    while (index < lines.length - 1 && !lines[index].startsWith('--- ')) {
      const hunk = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@$/.exec(lines[index++]); if (!hunk) fail(); hunks++;
      const start = Number(hunk[1]) === 0 ? 0 : Number(hunk[1])-1;
      if (start < cursor || start > original.length) fail(); output.push(...original.slice(cursor, start)); cursor = start;
      if ((Number(hunk[3]) === 0 ? 0 : Number(hunk[3])-1) !== output.length) fail();
      let removed = 0, added = 0;
      while (index < lines.length - 1 && !lines[index].startsWith('@@ ') && !lines[index].startsWith('--- ')) {
        const row = lines[index++]; const marker = row[0]; const text = row.slice(1);
        if (marker === ' ' || marker === '-') { if (original[cursor++] !== text) fail(); removed++; }
        if (marker === ' ' || marker === '+') { output.push(text); added++; }
        if (![' ', '-', '+'].includes(marker)) fail();
      }
      if (removed !== Number(hunk[2] ?? 1) || added !== Number(hunk[4] ?? 1)) fail();
    }
    if (!hunks) fail(); output.push(...original.slice(cursor)); writeFileSync(filename, output.join('\n')+'\n', { mode: 0o600 });
  }
  if (!seen.size) fail();
}

export function ensureWailsPatch(root, { executable = 'go', env = process.env } = {}) {
  const definition = verifyWailsDefinition(root);
  const destination = path.join(root, replacementPath);
  if (existsSync(destination)) return verifyWailsPatch(root);
  const local = privateDirectory(path.resolve(root), '.local');
  const modules = privateDirectory(path.resolve(root), '.local/modules');
  const lock = path.join(local, 'wails-materialization.lock');
  const descriptor = openSync(lock, 'wx', 0o600);
  const stage = path.join(modules, `wails-v3-stage-${process.pid}`);
  try {
    const bootstrap = privateDirectory(path.resolve(root), '.local/wails-bootstrap');
    for (const name of readdirSync(bootstrap)) { if (!['go.mod', 'go.sum'].includes(name)) fail(); file(path.join(bootstrap, name), 1 << 20); }
    writeFileSync(path.join(bootstrap, 'go.mod'), 'module whatsup-wails-bootstrap\n\ngo 1.27.4\n', { mode: 0o600 });
    const result = spawnSync(executable, ['mod', 'download', '-json', `${moduleName}@${moduleVersion}`], { cwd: bootstrap, env: { ...env, GOTOOLCHAIN: 'local', GOWORK: 'off', GOFLAGS: '', GOSUMDB: 'sum.golang.org', GONOSUMDB: 'none' }, shell: false, windowsHide: true, encoding: 'utf8', timeout: 300000, maxBuffer: 1 << 20 });
    if (result.error || result.status !== 0) throw new Error('Exact checksum-verified Wails bootstrap failed');
    const downloaded = JSON.parse(result.stdout);
    if (downloaded.Path !== moduleName || downloaded.Version !== moduleVersion || downloaded.Sum !== upstreamSum || downloaded.GoModSum !== upstreamGoModSum || typeof downloaded.Dir !== 'string') fail();
    const pristine = wailsTreeInventory(downloaded.Dir); const expected = definition.manifest.upstreamTree;
    if (pristine.sha256 !== expected.sha256 || pristine.count !== expected.count || pristine.size !== expected.size) fail();
    if (existsSync(stage)) fail(); mkdirSync(stage, { mode: 0o700 });
    for (const name of pristine.files) { const relative = path.dirname(name).replaceAll('\\', '/'); if (relative !== '.') { let at = stage; for (const part of relative.split('/')) { at=path.join(at,part); if (!existsSync(at)) mkdirSync(at,{mode:0o700}); directory(at); } }; const target=path.join(stage,name); copyFileSync(path.join(downloaded.Dir,name),target); chmodSync(target,0o600); }
    applyWailsTextPatch(stage, definition.patch);
    const patched = wailsTreeInventory(stage); const wanted = definition.manifest.patchedTree;
    if (patched.sha256 !== wanted.sha256 || patched.count !== wanted.count || patched.size !== wanted.size) fail();
    renameSync(stage, destination);
    return verifyWailsPatch(root);
  } finally {
    if (path.dirname(stage) === modules && path.basename(stage) === `wails-v3-stage-${process.pid}` && existsSync(stage) && !lstatSync(stage).isSymbolicLink()) rmSync(stage, { recursive: true });
    closeSync(descriptor); unlinkSync(lock);
  }
}

// Mechanically derived from the committed app graph. Tools keep official module
// identities and cannot silently compile an app-private replacement as a CLI.
export function prepareOfficialToolModule(root) {
  verifyWailsPatch(root);
  const directory = privateDirectory(path.resolve(root), '.local/tool-module');
  const original = file(path.join(root, 'go.mod'), 1 << 20).toString('utf8');
  const mod = original.replace(/^module [^\r\n]+/m, 'module whatsup-official-tools').replace(/^replace [^\r\n]+\r?\n?/gm, '');
  if (/^\s*replace\b/m.test(mod)) fail();
  for (const [name, bytes] of [['go.mod', Buffer.from(mod)], ['go.sum', file(path.join(root, 'go.sum'), 1 << 20)]]) {
    const target = path.join(directory, name); if (existsSync(target)) file(target, 1 << 20); writeFileSync(target, bytes, { mode: 0o600 });
  }
  return directory;
}

export function mergeUpstreamChecksumRows(existing, rows) {
  const selected = new Map();
  for (const row of existing.split(/\r?\n/).filter(Boolean)) {
    const fields = row.split(' '); if (fields.length !== 3 || selected.has(`${fields[0]} ${fields[1]}`)) fail(); selected.set(`${fields[0]} ${fields[1]}`, row);
  }
  for (const row of rows) {
    const fields = row.split(' ');
    if (fields.length !== 3 || !['github.com/tailscale/tailcat', moduleName].includes(fields[0]) || !/^v\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?(?:\/go\.mod)?$/.test(fields[1]) || !/^h1:[A-Za-z0-9+/]{43}=$/.test(fields[2])) fail();
    const key = `${fields[0]} ${fields[1]}`; if (selected.has(key) && selected.get(key) !== row) fail(); selected.set(key, row);
  }
  return [...selected.values()].sort().join('\n')+'\n';
}

// Explicit update only: tidy legitimately prunes sums for replaced modules.
// Resolve their immutable upstream identities using Go's supported checksum
// verification, then retain authenticated rows before ordinary guards resume.
// Setup/build remain frozen and cannot repair a missing committed checksum.
export function retainReplacementSums(root, pins, { executable = 'go', env = process.env } = {}) {
  const definition = sourceDefinition(root, false);
  const tailcat = verifyTailcatPatch(root, pins);
  const bootstrap = privateDirectory(path.resolve(root), '.local/replacement-sum-bootstrap');
  for (const name of readdirSync(bootstrap)) { if (!['go.mod', 'go.sum'].includes(name)) fail(); file(path.join(bootstrap, name), 1 << 20); }
  writeFileSync(path.join(bootstrap, 'go.mod'), 'module whatsup-replacement-checksums\n\ngo 1.27.4\n', { mode: 0o600 });
  const rows = [];
  for (const identity of [{ module: moduleName, version: moduleVersion }, tailcat]) {
    const result = spawnSync(executable, ['mod', 'download', '-json', `${identity.module}@${identity.version}`], { cwd: bootstrap, env: { ...env, GOTOOLCHAIN: 'local', GOWORK: 'off', GOFLAGS: '', GOSUMDB: 'sum.golang.org', GONOSUMDB: 'none' }, shell: false, windowsHide: true, encoding: 'utf8', timeout: 300000, maxBuffer: 1 << 20 });
    if (result.error || result.status !== 0) throw new Error('Authenticated upstream replacement checksum resolution failed');
    const resolved = JSON.parse(result.stdout);
    if (resolved.Path !== identity.module || resolved.Version !== identity.version || resolved.Error || typeof resolved.Sum !== 'string' || typeof resolved.GoModSum !== 'string') fail();
    if (identity.module === moduleName && (resolved.Sum !== definition.manifest.upstreamSum || resolved.GoModSum !== definition.manifest.upstreamGoModSum)) fail();
    if (identity.module === tailcat.module) {
      const goMod = file(path.join(root, tailcat.path, 'go.mod'));
      const expected = 'h1:'+createHash('sha256').update(`${digest(goMod)}  go.mod\n`).digest('base64');
      if (resolved.GoModSum !== expected) fail();
    }
    rows.push(`${identity.module} ${identity.version} ${resolved.Sum}`, `${identity.module} ${identity.version}/go.mod ${resolved.GoModSum}`);
  }
  const filename = path.join(root, 'go.sum');
  const before = file(filename, 1 << 20).toString('utf8');
  const after = mergeUpstreamChecksumRows(before, rows);
  if (before !== after) writeFileSync(filename, after);
  verifyWailsDefinition(root);
}
