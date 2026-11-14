import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { lstat, mkdtemp, mkdir, readFile, readdir, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { archiveReviewBuild, createReviewID } from '../lib/review-build.mjs';
import { within } from '../lib/workflow.mjs';

const sourceCommit = '0123456789abcdef0123456789abcdef01234567';
const sourceDigest = 'a'.repeat(64);
const now = new Date('2026-11-14T07:08:09.123Z');
const tools = { go: '1.27.4', node: '26.13.1', wails: 'v3.1.0' };

async function fixture(t) {
  const root = await mkdtemp(path.join(os.tmpdir(), 'whatsup-review-'));
  t.after(async () => {
    assert.equal(within(os.tmpdir(), root), true);
    assert.match(path.basename(root), /^whatsup-review-/);
    await rm(root, { recursive: true, force: true });
  });
  await mkdir(path.join(root, 'build', 'bin'), { recursive: true });
  const windowsBytes = Buffer.from('MZ synthetic Windows review binary\0');
  const linuxBytes = Buffer.from('\x7fELF synthetic Linux review binary\0');
  await writeFile(path.join(root, 'build', 'bin', 'whatsup.exe'), windowsBytes);
  await writeFile(path.join(root, 'build', 'bin', 'whatsup'), linuxBytes);
  await writeFile(path.join(root, 'build', 'bin', 'identity.vault'), 'synthetic private state must never copy');
  return { root, windowsBytes, linuxBytes };
}

function options(root, overrides = {}) {
  return {
    root, version: '0.0.1', platform: 'win32', arch: 'x64', sourceCommit,
    sourceDirty: true, sourceDigest, toolchain: tools, now, ...overrides,
  };
}

test('review snapshot copies only the native binary and binds dirty provenance to verified checksums', async (t) => {
  const { root, windowsBytes } = await fixture(t);
  const archived = await archiveReviewBuild(options(root, {
    validation: { componentChecks: 'passed', advisoryGate: 'failed-known-advisory', nativeUI: 'reported-pass', privateData: 'must not enter manifest' },
    toolchain: { ...tools, privateData: 'must not enter manifest' },
  }));
  assert.equal(archived.reviewID, '20261114T070809123Z-g0123456789ab-dirty');
  assert.equal(archived.directory, path.join(root, 'artifacts/development/0.0.1', archived.reviewID, 'win32-x64'));
  assert.deepEqual((await readdir(archived.directory)).sort(), ['SHA256SUMS', 'build-manifest.json', 'whatsup.exe']);
  assert.deepEqual(await readFile(path.join(archived.directory, 'whatsup.exe')), windowsBytes);
  const manifestBytes = await readFile(path.join(archived.directory, 'build-manifest.json'));
  const manifest = JSON.parse(manifestBytes);
  assert.equal(manifest.developmentOnly, true);
  assert.equal(manifest.releaseEligible, false);
  assert.equal(manifest.signed, false);
  assert.deepEqual(manifest.source, { commit: sourceCommit, dirty: true, digestSHA256: sourceDigest });
  assert.deepEqual(manifest.toolchain, tools);
  assert.equal(manifest.validation.advisoryGate, 'failed-known-advisory');
  assert.equal(manifest.validation.nativeUI, 'reported-pass');
  assert.equal(manifestBytes.includes(Buffer.from('privateData')), false);
  const checksumLines = (await readFile(path.join(archived.directory, 'SHA256SUMS'), 'utf8')).trim().split('\n');
  for (const line of checksumLines) {
    const [digest, name] = line.split('  ');
    assert.equal(createHash('sha256').update(await readFile(path.join(archived.directory, name))).digest('hex'), digest);
  }
});

test('same review ID permits the other platform but never overwrites an existing candidate', async (t) => {
  const { root, windowsBytes, linuxBytes } = await fixture(t);
  const first = await archiveReviewBuild(options(root));
  assert.equal(first.manifest.validation.nativeUI, 'not-run');
  const second = await archiveReviewBuild(options(root, { platform: 'linux', reviewID: first.reviewID }));
  assert.equal(path.dirname(first.directory), path.dirname(second.directory));
  assert.deepEqual(await readFile(path.join(second.directory, 'whatsup')), linuxBytes);
  const originalManifest = await readFile(path.join(first.directory, 'build-manifest.json'));
  await writeFile(path.join(root, 'build/bin/whatsup.exe'), 'MZ replacement must not overwrite first snapshot');
  await assert.rejects(archiveReviewBuild(options(root, { reviewID: first.reviewID })), /already exists/);
  assert.deepEqual(await readFile(path.join(first.directory, 'whatsup.exe')), windowsBytes);
  assert.deepEqual(await readFile(path.join(first.directory, 'build-manifest.json')), originalManifest);
});

test('review snapshots bind both native patch inventories without accepting extra private metadata', async (t) => {
  const { root } = await fixture(t);
  const patches = [
    { module: 'github.com/tailscale/tailcat', version: 'v0.7.1-0.20261105151022-e00cdde6ff78', patchVersion: 2, manifestSHA256: 'a'.repeat(64) },
    { module: 'github.com/wailsapp/wails/v3', version: 'v3.1.0', patchVersion: 1, manifestSHA256: 'b'.repeat(64), treeSHA256: 'c'.repeat(64), privateData: 'must not enter manifest' },
  ];
  const result = await archiveReviewBuild(options(root, { patches }));
  assert.equal(result.manifest.patches[1].treeSHA256, 'c'.repeat(64));
  assert.equal(JSON.stringify(result.manifest).includes('privateData'), false);
  await assert.rejects(() => archiveReviewBuild(options(root, { patches: patches.slice(1) })), /Both reviewed native patch/);
  await assert.rejects(() => archiveReviewBuild(options(root, { patches: [patches[0], { ...patches[1], treeSHA256: '' }] })), /Invalid reviewed native patch/);
});

test('review snapshot rejects traversal, false clean IDs and arbitrary source data before creating output', async (t) => {
  const { root } = await fixture(t);
  const invalid = [
    { version: '../outside' }, { version: 'v0.0.1' }, { version: '0.0.1-beta.1' },
    { platform: '../outside' }, { arch: 'x64/../outside' },
    { reviewID: '../outside' }, { reviewID: '20261114T070809123Z-g0123456789ab' },
    { sourceDirty: 'false' }, { sourceCommit: 'HEAD' }, { sourceDigest: 'not-a-hash' },
    { binary: path.join(root, 'build/bin/identity.vault') },
    { validation: { advisoryGate: 'ignore-advisories' } },
  ];
  for (const entry of invalid) await assert.rejects(archiveReviewBuild(options(root, entry)));
  await assert.rejects(lstat(path.join(root, 'artifacts')), { code: 'ENOENT' });
  await assert.rejects(archiveReviewBuild(options(root, { platform: 'linux', binary: path.join(root, 'build/bin/whatsup.exe') })), /exact native build output/);
});

test('review snapshot rejects a native output with the wrong executable signature', async (t) => {
  const { root } = await fixture(t);
  await writeFile(path.join(root, 'build/bin/whatsup.exe'), 'synthetic vault bytes');
  await assert.rejects(archiveReviewBuild(options(root)), /does not match/);
  await assert.rejects(lstat(path.join(root, 'artifacts')), { code: 'ENOENT' });
});

test('review snapshot rejects symlinked artifact roots and native binary sources', async (t) => {
  const { root } = await fixture(t);
  const elsewhere = path.join(root, 'elsewhere');
  await mkdir(elsewhere);
  await symlink(elsewhere, path.join(root, 'artifacts'), process.platform === 'win32' ? 'junction' : 'dir');
  await assert.rejects(archiveReviewBuild(options(root)), /without symlinks/);
  assert.deepEqual(await readdir(elsewhere), []);
  await rm(path.join(root, 'artifacts'));
  const realBinary = path.join(root, 'build/bin/actual-whatsup.exe');
  await writeFile(realBinary, 'MZ synthetic native binary');
  await rm(path.join(root, 'build/bin/whatsup.exe'));
  try { await symlink(realBinary, path.join(root, 'build/bin/whatsup.exe'), 'file'); }
  catch (error) {
    if (process.platform !== 'win32' || error.code !== 'EPERM') throw error;
    // Windows may deny file symlink creation without Developer Mode. The
    // directory-junction rejection above remains a real non-skipped check.
    t.diagnostic('Windows denied the additional file-symlink fixture; directory junction rejection passed.');
    return;
  }
  await assert.rejects(archiveReviewBuild(options(root)), /regular file/);
});

test('review IDs record clean versus dirty source and reject invalid source identity', () => {
  assert.equal(createReviewID({ sourceCommit, sourceDirty: false, now }), '20261114T070809123Z-g0123456789ab');
  assert.throws(() => createReviewID({ sourceCommit: '../HEAD', sourceDirty: false, now }));
  assert.throws(() => createReviewID({ sourceCommit, sourceDirty: false, now: new Date('invalid') }));
});
