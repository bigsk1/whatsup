import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdtemp, readFile, readdir, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const installer = path.join(root, 'scripts/install-linux-review.py');
const python = process.platform === 'win32' ? 'python' : 'python3';
const pythonAvailable = spawnSync(python, ['--version'], { windowsHide: true, stdio: 'ignore', timeout: 10000 }).status === 0;
const options = { skip: pythonAvailable ? false : 'Python is only required for Linux installation checks' };
const hash = (bytes) => createHash('sha256').update(bytes).digest('hex');

async function bundleFixture(t) {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'whatsup-linux-install-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const elf = Buffer.alloc(80);
  Buffer.from([0x7f, 0x45, 0x4c, 0x46, 2, 1, 1]).copy(elf);
  elf.writeUInt16LE(2, 16);
  elf.writeUInt16LE(62, 18);
  const manifest = {
    schemaVersion: 1, kind: 'development-review', version: '0.0.2',
    reviewID: '20261114T070809123Z-gaaaaaaaaaaaa-dirty', platform: 'linux', arch: 'x64',
    developmentOnly: true, releaseEligible: false, signed: false,
    source: { commit: 'a'.repeat(40), dirty: true, digestSHA256: 'b'.repeat(64) },
    files: [{ name: 'whatsup', size: elf.length, sha256: hash(elf) }],
  };
  const probe = {
    schemaVersion: 1, kind: 'linux-webview-check', version: manifest.version,
    reviewID: manifest.reviewID, sourceDigestSHA256: manifest.source.digestSHA256,
    files: [{ name: 'whatsup-webview-check', size: elf.length, sha256: hash(elf) }],
  };
  const manifestBytes = JSON.stringify(manifest) + '\n';
  await writeFile(path.join(directory, 'whatsup'), elf);
  await writeFile(path.join(directory, 'whatsup-webview-check'), elf);
  await writeFile(path.join(directory, 'build-manifest.json'), manifestBytes);
  await writeFile(path.join(directory, 'probe-manifest.json'), JSON.stringify(probe) + '\n');
  await writeFile(path.join(directory, 'SHA256SUMS'), `${hash(elf)}  whatsup\n${hash(manifestBytes)}  build-manifest.json\n`);
  return { directory, manifest, probe, elf };
}

function check(directory) {
  return spawnSync(python, ['-I', '-B', installer, '--check-bundle', directory], { cwd: root, windowsHide: true, encoding: 'utf8', timeout: 10000 });
}

test('Linux private installer validates matching app/probe bytes without privileged side effects', options, async (t) => {
  const { directory } = await bundleFixture(t);
  const before = (await readdir(directory)).sort();
  const result = check(directory);
  assert.equal(result.status, 0, result.stderr);
  const summary = JSON.parse(result.stdout);
  assert.equal(summary.bundleValid, true);
  assert.equal(summary.profileTemplateValid, true);
  assert.equal(summary.hostPolicyUnverified, true);
  assert.equal(summary.systemChanged, false);
  assert.equal(summary.releaseEligible, false);
  assert.deepEqual((await readdir(directory)).sort(), before);
});

test('Linux private installer rejects mixed builds, tampering and checksum path injection', options, async (t) => {
  const { directory, probe, elf } = await bundleFixture(t);
  await writeFile(path.join(directory, 'probe-manifest.json'), JSON.stringify({ ...probe, sourceDigestSHA256: 'c'.repeat(64) }));
  assert.match(check(directory).stderr, /must match/);
  await writeFile(path.join(directory, 'probe-manifest.json'), JSON.stringify(probe));
  await writeFile(path.join(directory, 'whatsup'), Buffer.concat([elf, Buffer.from('tampered')]));
  assert.match(check(directory).stderr, /size and SHA-256/);
  await writeFile(path.join(directory, 'whatsup'), elf);
  await writeFile(path.join(directory, 'SHA256SUMS'), `${hash(elf)}  ../../etc/sudoers\n`);
  assert.match(check(directory).stderr, /exactly the app/);
});

test('Linux installer refuses a source symlink and enforces root before installation work', options, async (t) => {
  const { directory } = await bundleFixture(t);
  const source = path.join(directory, 'whatsup');
  const original = await readFile(source);
  await rm(source);
  await writeFile(path.join(directory, 'actual-elf'), original);
  try {
    await symlink(path.join(directory, 'actual-elf'), source, 'file');
    assert.match(check(directory).stderr, /without symlinks/);
  } catch (error) {
    if (!['EPERM', 'EACCES'].includes(error.code)) throw error;
  }
  const code = `import importlib.util,sys\nfrom unittest.mock import patch\nspec=importlib.util.spec_from_file_location('installer',sys.argv[1])\nm=importlib.util.module_from_spec(spec)\nspec.loader.exec_module(m)\nwith patch.object(m.sys,'platform','linux'), patch.object(m.os,'geteuid',return_value=1000,create=True):\n try:\n  m.install_bundle({})\n except m.InstallError as e:\n  print(str(e))\n else:\n  raise RuntimeError('privilege boundary not enforced')\n`;
  const result = spawnSync(python, ['-I', '-B', '-c', code, installer], { cwd: root, windowsHide: true, encoding: 'utf8', timeout: 10000 });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /requires root/);
});
