import { constants } from 'node:fs';
import { lstat, mkdir, open, realpath, writeFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import path from 'node:path';
import { parseVersion } from './workflow.mjs';

const commitPattern = /^(?:[a-f0-9]{40}|[a-f0-9]{64})$/;
const digestPattern = /^[a-f0-9]{64}$/;
const maxBinaryBytes = 256 * 1024 * 1024;

function sourceIdentity(sourceCommit, sourceDirty) {
  if (!commitPattern.test(sourceCommit) || typeof sourceDirty !== 'boolean') {
    throw new Error('A full source commit and explicit dirty-source state are required');
  }
  return `g${sourceCommit.slice(0, 12)}${sourceDirty ? '-dirty' : ''}`;
}

export function createReviewID({ sourceCommit, sourceDirty, now = new Date() }) {
  const identity = sourceIdentity(sourceCommit, sourceDirty);
  if (!(now instanceof Date) || !Number.isFinite(now.getTime())) throw new Error('Invalid review build time');
  return `${now.toISOString().replace(/[-:.]/g, '')}-${identity}`;
}

async function regularDirectory(directory) {
  const info = await lstat(directory);
  if (!info.isDirectory() || info.isSymbolicLink()) throw new Error('Review build paths must use regular directories without symlinks');
}

// This protects ordinary artifact path mistakes. The account owning the source
// workspace remains trusted; it is not filesystem confinement against that user.
async function inspectAncestors(directory) {
  const ancestors = [];
  for (let candidate = path.resolve(directory); ; candidate = path.dirname(candidate)) {
    ancestors.push(candidate);
    if (path.dirname(candidate) === candidate) break;
  }
  for (const ancestor of ancestors.reverse()) await regularDirectory(ancestor);
}

async function ensureChild(parent, name) {
  const directory = path.join(parent, name);
  try { await mkdir(directory, { mode: 0o700 }); }
  catch (error) { if (error.code !== 'EEXIST') throw error; }
  await regularDirectory(directory);
  return directory;
}

function toolVersions(toolchain) {
  const selected = {};
  for (const key of ['go', 'node', 'wails']) {
    const value = toolchain?.[key];
    if (typeof value !== 'string' || !/^(?:v|go)?\d+\.\d+\.\d+$/.test(value)) throw new Error('Exact stable tool versions are required');
    parseVersion(value);
    selected[key] = value;
  }
  return selected;
}

function validationEvidence(validation = {}) {
  const { componentChecks = 'not-run-for-this-snapshot', advisoryGate = 'not-run-for-this-snapshot', nativeUI = 'not-run' } = validation;
  if (!['not-run-for-this-snapshot', 'passed'].includes(componentChecks)
      || !['not-run-for-this-snapshot', 'failed', 'failed-known-advisory', 'passed'].includes(advisoryGate)
      || !['not-run', 'reported-pass'].includes(nativeUI)) {
    throw new Error('Unsupported development validation evidence');
  }
  return { componentChecks, advisoryGate, nativeUI };
}

function patchEvidence(patches) {
  if (patches === undefined) return undefined;
  const names = ['github.com/tailscale/tailcat', 'github.com/wailsapp/wails/v3'];
  if (!Array.isArray(patches) || patches.length !== 2 || new Set(patches.map((entry) => entry?.module)).size !== 2) throw new Error('Both reviewed native patch identities are required');
  return patches.map((entry) => {
    if (!names.includes(entry?.module) || typeof entry.version !== 'string' || !/^v\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?$/.test(entry.version)
      || !Number.isSafeInteger(entry.patchVersion) || entry.patchVersion < 1 || !digestPattern.test(entry.manifestSHA256)
      || (entry.module === 'github.com/wailsapp/wails/v3' && !digestPattern.test(entry.treeSHA256))) throw new Error('Invalid reviewed native patch evidence');
    return { module: entry.module, version: entry.version, patchVersion: entry.patchVersion, manifestSHA256: entry.manifestSHA256,
      ...(entry.module === 'github.com/wailsapp/wails/v3' ? { treeSHA256: entry.treeSHA256 } : {}) };
  });
}

/**
 * Preserve one native DEVELOPMENT candidate, separate from gated release
 * packaging. Only the exact native build output and selected public metadata
 * enter this directory; no source tree, logs, state, credentials or extra files.
 * A failed archive leaves its reserved directory for inspection, never reuse.
 */
export async function archiveReviewBuild({
  root, version, platform, arch, binary, sourceCommit, sourceDirty, sourceDigest,
  toolchain, reviewID, validation, patches, now = new Date(),
}) {
  if (!path.isAbsolute(root)) throw new Error('Review builds require an absolute workspace path');
  const parsed = parseVersion(version);
  if (version !== parsed.join('.')) throw new Error('Review version must be an exact stable numeric application version');
  if (!['win32', 'linux'].includes(platform) || !['x64', 'arm64'].includes(arch)) {
    throw new Error('Unsupported native review platform or architecture');
  }
  const identity = sourceIdentity(sourceCommit, sourceDirty);
  if (!digestPattern.test(sourceDigest)) throw new Error('A SHA-256 source digest is required');
  const id = reviewID ?? createReviewID({ sourceCommit, sourceDirty, now });
  if (typeof id !== 'string' || !/^\d{8}T\d{9}Z-g[a-f0-9]{12}(?:-dirty)?$/.test(id)
      || !id.endsWith(`-${identity}`)) {
    throw new Error('Review ID must be a bounded timestamp matching this source commit and dirty state');
  }
  const createdAt = new Date(now).toISOString();
  const selectedTools = toolVersions(toolchain);
  const selectedPatches = patchEvidence(patches);
  const evidence = validationEvidence(validation);
  const workspace = path.resolve(root);
  await inspectAncestors(workspace);
  if (await realpath(workspace) !== workspace) throw new Error('Review workspace must not use a symlink or alias');

  const binaryName = platform === 'win32' ? 'whatsup.exe' : 'whatsup';
  const expectedBinary = path.join(workspace, 'build', 'bin', binaryName);
  if (binary !== undefined && path.resolve(binary) !== expectedBinary) {
    throw new Error('Review snapshot accepts only the exact native build output');
  }
  await inspectAncestors(path.dirname(expectedBinary));
  const before = await lstat(expectedBinary);
  if (!before.isFile() || before.isSymbolicLink() || before.size === 0 || before.size > maxBinaryBytes) {
    throw new Error('Native build output must be a bounded nonempty regular file');
  }
  const handle = await open(expectedBinary, constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0));
  let bytes;
  try {
    const info = await handle.stat();
    if (!info.isFile() || info.size !== before.size || info.dev !== before.dev || info.ino !== before.ino) {
      throw new Error('Native build output changed during inspection');
    }
    bytes = await handle.readFile();
    if (bytes.length !== info.size) throw new Error('Native build output changed during reading');
  } finally { await handle.close(); }
  const magic = platform === 'win32' ? Buffer.from('MZ') : Buffer.from([0x7f, 0x45, 0x4c, 0x46]);
  if (!bytes.subarray(0, magic.length).equals(magic)) throw new Error('Native build output does not match the selected platform');

  let parent = workspace;
  for (const segment of ['artifacts', 'development', version, id]) parent = await ensureChild(parent, segment);
  const directory = path.join(parent, `${platform}-${arch}`);
  try { await mkdir(directory, { mode: 0o700 }); }
  catch (error) {
    if (error.code === 'EEXIST') throw new Error('Development review candidate already exists; it will not be overwritten');
    throw error;
  }
  await regularDirectory(directory);
  const manifest = {
    schemaVersion: 1, kind: 'development-review', version, reviewID: id, createdAt,
    platform, arch, developmentOnly: true, releaseEligible: false, signed: false,
    source: { commit: sourceCommit, dirty: sourceDirty, digestSHA256: sourceDigest },
    toolchain: selectedTools,
    ...(selectedPatches ? { patches: selectedPatches } : {}),
    validation: evidence,
    limitations: [
      'Private unsigned development candidate; not an installer or approved release.',
      'Readiness, signing, SBOM and signed-update gates remain independent and closed.',
      'A review snapshot does not establish live transport or native UI verification; consult its recorded validation scope.',
      'Checksums detect changed bytes; they are not release signatures.',
    ],
    files: [{ name: binaryName, size: bytes.length, sha256: createHash('sha256').update(bytes).digest('hex') }],
  };
  const manifestBytes = Buffer.from(JSON.stringify(manifest, null, 2) + '\n');
  await writeFile(path.join(directory, binaryName), bytes, { flag: 'wx', mode: 0o700 });
  await writeFile(path.join(directory, 'build-manifest.json'), manifestBytes, { flag: 'wx', mode: 0o600 });
  const manifestHash = createHash('sha256').update(manifestBytes).digest('hex');
  await writeFile(path.join(directory, 'SHA256SUMS'), `${manifest.files[0].sha256}  ${binaryName}\n${manifestHash}  build-manifest.json\n`, { flag: 'wx', mode: 0o600 });
  return { directory, reviewID: id, manifest, binaryName };
}
