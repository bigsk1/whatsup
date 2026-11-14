import { createHash } from 'node:crypto';
import { lstatSync, readFileSync, readdirSync } from 'node:fs';
import path from 'node:path';

const moduleName = 'github.com/tailscale/tailcat';
const moduleVersion = 'v0.7.1-0.20261105151022-e00cdde6ff78';
const revision = 'e00cdde6ff787f801361ff85ed085775c775d4c5';
const replacementPath = './third_party/tailcat';
const verified = new WeakSet();
const hash = (bytes) => createHash('sha256').update(bytes).digest('hex');
const sha256 = /^[a-f0-9]{64}$/;

function fail() {
  throw new Error('Reviewed Tailcat privacy patch is missing, changed or unsafe. Review its source and manifest before continuing.');
}

function regularFile(filename, maximum) {
  const stat = lstatSync(filename);
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size < 1 || stat.size > maximum) fail();
  return readFileSync(filename);
}

function directory(filename) {
  const stat = lstatSync(filename);
  if (!stat.isDirectory() || stat.isSymbolicLink()) fail();
}

// Synchronous because every subprocess boundary rechecks it, including tool
// compilation and scanners. This never downloads or mutates upstream/cache files.
export function verifyTailcatPatch(root, pins) {
  try {
    root = path.resolve(root);
    directory(root);
    directory(path.join(root, 'config'));
    directory(path.join(root, 'third_party'));
    const location = path.join(root, 'third_party/tailcat');
    directory(location);
    const manifestBytes = regularFile(path.join(root, 'config/tailcat-patch.json'), 64 << 10);
    const manifest = JSON.parse(manifestBytes.toString('utf8'));
    if (manifest.schemaVersion !== 1 || manifest.module !== moduleName || manifest.version !== moduleVersion
        || manifest.upstreamRevision !== revision || manifest.patchVersion !== 2
        || manifest.replacement !== replacementPath || pins?.[moduleName] !== moduleVersion
        || manifest.upstreamRepository !== 'https://github.com/tailscale/tailcat'
        || JSON.stringify(manifest.purpose) !== JSON.stringify(['derp_application_stats_opt_out', 'disable_bearer_debug_logging', 'readonly_client_status'])
        || !Array.isArray(manifest.files) || manifest.files.length < 5 || manifest.files.length > 64) fail();
    const declared = new Map();
    for (const entry of manifest.files) {
      if (typeof entry.path !== 'string' || !/^(?:[a-z][a-z0-9_]*\.go|go\.mod|go\.sum|LICENSE)$/.test(entry.path)
          || declared.has(entry.path) || !Number.isSafeInteger(entry.size) || entry.size < 1 || entry.size > (8 << 20)
          || typeof entry.sha256 !== 'string' || !sha256.test(entry.sha256)) fail();
      if (entry.path === 'whatsup_privacy_test.go') {
        if (entry.upstreamSHA256 !== null) fail();
      } else if (typeof entry.upstreamSHA256 !== 'string' || !sha256.test(entry.upstreamSHA256)
          || (entry.path !== 'tailcat.go' && entry.sha256 !== entry.upstreamSHA256)) fail();
      declared.set(entry.path, entry);
    }
    for (const name of ['LICENSE', 'go.mod', 'go.sum', 'tailcat.go', 'whatsup_privacy_test.go']) {
      if (!declared.has(name)) fail();
    }
    const actual = readdirSync(location);
    if (actual.length !== declared.size) fail();
    for (const name of actual) {
      const entry = declared.get(name);
      if (!entry) fail();
      const bytes = regularFile(path.join(location, name), 8 << 20);
      if (bytes.length !== entry.size || hash(bytes) !== entry.sha256) fail();
    }
    const localModule = regularFile(path.join(location, 'go.mod'), 1 << 20).toString('utf8');
    if (!/^module github\.com\/tailscale\/tailcat\r?$/m.test(localModule) || /^\s*replace\b/m.test(localModule)) fail();
    const rootModule = regularFile(path.join(root, 'go.mod'), 1 << 20).toString('utf8');
    const replacements = rootModule.match(/^\s*replace\b.*$/gm) ?? [];
    if (replacements.length !== 1 || replacements[0].trim() !== `replace ${moduleName} => ${replacementPath}`
        || !rootModule.split(/\r?\n/).some((line) => line.trim() === `${moduleName} ${moduleVersion}`)) fail();
    const approval = Object.freeze({ module: moduleName, version: moduleVersion, path: replacementPath, root,
      manifestSHA256: hash(manifestBytes), patchVersion: manifest.patchVersion });
    verified.add(approval);
    return approval;
  } catch {
    fail();
  }
}

// Callers cannot enable a replacement by passing a similarly shaped object.
// Only a successful verification in this process can issue an approval.
export function isVerifiedTailcatPatch(value) {
  return value !== null && typeof value === 'object' && verified.has(value);
}

export function isApprovedReplacement(module, version, target, approval) {
  return isVerifiedTailcatPatch(approval) && module === approval.module
    && version === approval.version && target === approval.path;
}
