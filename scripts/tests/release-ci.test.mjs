import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { assertReleaseContext, changelogSection, parseReleaseChecksums, previewReleaseNotes, releaseArguments, releaseNames, verifyReleasePackage } from '../lib/release-ci.mjs';
import { sourceToolIdentity } from '../lib/packaging-tools.mjs';
import { fileURLToPath } from 'node:url';

const commit = 'a'.repeat(40);
const digest = 'b'.repeat(64);
const scanNames = ['root npm audit', 'frontend npm audit', 'govulncheck', 'Wails CLI binary', 'govulncheck binary',
  'github.com/tailscale/tailcat upstream version advisories', 'github.com/wailsapp/wails/v3 upstream version advisories'];
const packagingTools = JSON.parse(await readFile(new URL('../../config/packaging-tools.json', import.meta.url), 'utf8'));
const root = fileURLToPath(new URL('../../', import.meta.url));
const sourceTools = Object.fromEntries(await Promise.all(['nfpm', 'cyclonedx-gomod'].map(async (name) => [name, await sourceToolIdentity({ root, name })])));
const minimumGo = JSON.parse(await readFile(new URL('../../config/toolchain.json', import.meta.url), 'utf8')).minimumGo;
const arWriterPolicy = JSON.parse(await readFile(new URL('../../config/nfpm-ar-writer.json', import.meta.url), 'utf8'));
function upstreamAr() {
  const official = { id: 'GO-2026-6905', aliases: [], database_specific: { review_status: 'REVIEWED' }, affected: [
    { package: { ecosystem: 'Go', name: 'github.com/blakesmith/ar' }, ecosystem_specific: { imports: [{ path: 'github.com/blakesmith/ar', symbols: ['Reader.Read'] }] } },
  ] };
  return { module: 'github.com/blakesmith/ar', version: 'v0.0.0-20190502131153-809d4375e1fb', passed: true,
    ids: ['GO-2026-6905'], codeRemoved: [{ id: 'GO-2026-6905', symbols: ['Reader.Read'] }], raw: { query: { vulns: [official] }, official } };
}
function toolEvidence(name, platform) {
  const pin = packagingTools.tools[name];
  return pin.kind === 'source' ? { name, kind: 'source', version: pin.version,
    lockSHA256: sourceTools[name].lockSHA256, binarySHA256: 'e'.repeat(64), compilerGo: minimumGo, scanPassed: true }
    : { name, kind: 'archive', version: pin.version, archiveSHA256: pin[platform].sha256 };
}
const context = { version: '0.1.0', ref: 'refs/tags/v0.1.0', event: 'push', actions: 'true', dirty: false,
  commit, tagCommit: commit, repository: 'example/whatsup' };

test('only a clean immutable version tag can reach release operations', () => {
  assert.equal(assertReleaseContext(context).tag, 'v0.1.0');
  assert.equal(assertReleaseContext({ ...context, event: 'workflow_dispatch' }).tag, 'v0.1.0');
  for (const change of [{ ref: 'refs/heads/main' }, { ref: 'refs/tags/v0.0.17' }, { event: 'pull_request' },
    { actions: undefined }, { dirty: true }, { tagCommit: 'c'.repeat(40) }, { repository: 'https://example.invalid' }]) {
    assert.throws(() => assertReleaseContext({ ...context, ...change }), /requires a clean checkout/);
  }
});

test('checksum inputs refuse path traversal, duplicates and malformed hashes', () => {
  assert.equal(parseReleaseChecksums(`${digest}  installer.exe\n`).size, 1);
  for (const input of [`${digest}  ../installer.exe\n`, `${digest}  /installer.exe\n`, `${digest}  nested/file.deb\n`,
    `${digest}  installer.exe\n${digest}  installer.exe\n`, `bad  installer.exe\n`]) {
    assert.throws(() => parseReleaseChecksums(input));
  }
});

async function fixture(t, platform = 'win32-x64', changes = {}) {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'whatsup-release-test-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const names = releaseNames('0.1.0', platform);
  const manifest = { version: '0.1.0', platform: platform.split('-')[0], arch: 'x64', sourceCommit: commit,
    sourceDigestSHA256: digest, sourceDirty: false, unsignedPreview: true, schemaVersion: 1,
    signed: false, authenticode: false, inAppUpdatesEnabled: false, releaseEligible: false,
    checks: { functional: 'passed', integration: 'passed', nativeBuild: 'passed', goRaceEnabled: true,
      recordedAt: new Date().toISOString(), nativeUIAcceptance: 'not-run-by-this-command', scans: scanNames.map((name) => ({ name, passed: true })) },
    packaging: toolEvidence(platform === 'win32-x64' ? 'nsis' : 'nfpm', platform),
    sbom: { ...toolEvidence('cyclonedx-gomod', platform),
      format: 'CycloneDX JSON', file: 'sbom.cdx.json' }, ...changes };
  const installer = platform === 'win32-x64' ? 'MZsynthetic installer' : '!<arch>\nsynthetic package';
  manifest.files = [{ name: names.installer, size: Buffer.byteLength(installer), sha256: createHash('sha256').update(installer).digest('hex') }];
  manifest.packagingToolScans = [manifest.packaging, manifest.sbom].filter((tool) => tool.kind === 'source')
    .map((tool) => ({ name: tool.name, version: tool.version, binarySHA256: tool.binarySHA256, passed: true, upstreamAr: upstreamAr() }));
  const files = new Map([[names.installer, installer],
    ['sbom.cdx.json', JSON.stringify({ bomFormat: 'CycloneDX', components: [{ name: 'synthetic component', version: '1.0.0' }],
      metadata: { properties: [{ name: 'whatsup:source:commit', value: commit }, { name: 'whatsup:source:sha256', value: digest }, { name: 'whatsup:source:dirty', value: 'false' }] } })],
    ['build-manifest.json', JSON.stringify(manifest)]]);
  for (const [name, content] of files) await writeFile(path.join(directory, name), content);
  await writeFile(path.join(directory, 'SHA256SUMS'), [...files].map(([name, content]) => `${createHash('sha256').update(content).digest('hex')}  ${name}\n`).join(''));
  return { directory, names, context: { ...assertReleaseContext(context), sourceDigestSHA256: digest, packagingTools, sourceTools, minimumGo, arWriterPolicy } };
}

test('both platform bundles require checksum-covered provenance and a nonempty SBOM', async (t) => {
  for (const platform of ['win32-x64', 'linux-x64']) {
    const value = await fixture(t, platform);
    assert.equal((await verifyReleasePackage(value.directory, value.context, platform)).names.installer, value.names.installer);
  }
});

test('changed installer bytes cannot enter the attested bundle', async (t) => {
  const value = await fixture(t);
  await writeFile(path.join(value.directory, value.names.installer), 'MZchanged');
  await assert.rejects(verifyReleasePackage(value.directory, value.context, 'win32-x64'), /checksum mismatch/);
});

test('missing or unexpected files fail closed', async (t) => {
  const value = await fixture(t);
  await rm(path.join(value.directory, 'sbom.cdx.json'));
  await assert.rejects(verifyReleasePackage(value.directory, value.context, 'win32-x64'), /missing files/);
  await writeFile(path.join(value.directory, 'extra.exe'), 'MZ');
  await assert.rejects(verifyReleasePackage(value.directory, value.context, 'win32-x64'), /unexpected files/);
});

test('a valid checksum cannot excuse a dirty, wrong-version or wrong-source candidate', async (t) => {
  for (const changes of [{ sourceDirty: true }, { version: '0.0.17' }, { sourceCommit: 'c'.repeat(40) },
    { sourceDigestSHA256: 'd'.repeat(64) }, { unsignedPreview: false }]) {
    const value = await fixture(t, 'win32-x64', changes);
    await assert.rejects(verifyReleasePackage(value.directory, value.context, 'win32-x64'), /provenance/);
  }
});

test('release arguments publish the verified tag without overwriting an existing release', () => {
  const files = Array.from({ length: 7 }, (_, index) => path.resolve(`synthetic-${index}`));
  const args = releaseArguments(assertReleaseContext(context), path.resolve('notes.md'), files);
  assert.equal(args.includes('--draft'), false);
  assert.equal(args.includes('--latest'), true);
  assert.equal(args.includes('--verify-tag'), true);
  assert.equal(args.includes('--clobber'), false);
  assert.equal(args[args.indexOf('--title') + 1], 'v0.1.0 (unsigned)');
  assert.throws(() => releaseArguments(assertReleaseContext(context), path.resolve('notes.md'), []));
});

test('release notes come from the tagged changelog section and stay structured', () => {
  const changelog = '# Changelog\r\n\r\n## 0.1.0 (unsigned preview)\r\n\r\n- Synthetic feature\r\n  wrapped onto two lines.\r\n- Second item.\r\n\r\nSynthetic\r\nnote.\r\n\r\n## 0.0.17\r\n\r\n- Older entry.\r\n';
  const changes = changelogSection(changelog, '0.1.0');
  assert.equal(changes, '- Synthetic feature wrapped onto two lines.\n- Second item.\n\nSynthetic note.');
  assert.throws(() => changelogSection(changelog, '0.2.0'), /no "## 0\.2\.0" section/);
  assert.throws(() => changelogSection('## 0.1.0\n\nProse without bullets.\n', '0.1.0'), /at least one bullet/);
  assert.throws(() => changelogSection('## 0.1.01\n\n- Wrong version.\n', '0.1.0'), /no "## 0\.1\.0" section/);
  const notes = previewReleaseNotes('0.1.0', 'example/whatsup', changes);
  for (const heading of ["## What's new", '## Downloads', '## Install', '## Verify your download', '## Good to know']) {
    assert.match(notes, new RegExp(`^${heading}$`, 'm'));
  }
  assert.match(notes, /^- Synthetic feature wrapped onto two lines\.$/m);
  assert.match(notes, /\| Windows 10 or 11 \(x64\) \| `WhatsUp-0\.1\.0-windows-x64-setup\.exe` \|/);
  assert.match(notes, /\*\*More info\*\* and \*\*Run anyway\*\*/);
  assert.match(notes, /gh attestation verify whatsup_0\.1\.0_amd64\.deb --repo example\/whatsup --signer-workflow example\/whatsup\/\.github\/workflows\/release\.yml/);
  assert.match(notes, /not Windows code signing/);
  assert.throws(() => previewReleaseNotes('0.1.0', 'example/whatsup', ''));
  assert.throws(() => previewReleaseNotes('0.1.0', 'https://example.invalid', changes));
});

test('the current changelog has notes for the current application version', async () => {
  const { version } = JSON.parse(await readFile(new URL('../../package.json', import.meta.url), 'utf8'));
  const changelog = await readFile(new URL('../../CHANGELOG.md', import.meta.url), 'utf8');
  assert.match(changelogSection(changelog, version), /^- /m);
});

test('missing, failed or expired checks cannot be made releasable by valid checksums', async (t) => {
  for (const checks of [undefined, {}, { functional: 'failed' },
    { functional: 'passed', integration: 'passed', nativeBuild: 'passed', goRaceEnabled: false, nativeUIAcceptance: 'not-run-by-this-command',
      scans: scanNames.map((name) => ({ name, passed: true })), recordedAt: '2020-01-01T00:00:00Z' }]) {
    const value = await fixture(t, 'win32-x64', { checks });
    await assert.rejects(verifyReleasePackage(value.directory, value.context, 'win32-x64'), /checks are missing/);
  }
});

async function rewriteCovered(value, filename, mutate) {
  const target = path.join(value.directory, filename);
  const changed = JSON.stringify(mutate(JSON.parse(await readFile(target, 'utf8'))));
  await writeFile(target, changed);
  const sums = parseReleaseChecksums(await readFile(path.join(value.directory, 'SHA256SUMS'), 'utf8'));
  sums.set(filename, createHash('sha256').update(changed).digest('hex'));
  await writeFile(path.join(value.directory, 'SHA256SUMS'), [...sums].map(([name, hash]) => `${hash}  ${name}\n`).join(''));
}

test('scan failures, missing race, SBOM substitution and updater flags fail even with matching hashes', async (t) => {
  for (const platform of ['win32-x64', 'linux-x64']) {
    const value = await fixture(t, platform);
    await verifyReleasePackage(value.directory, value.context, platform);
    await rewriteCovered(value, 'build-manifest.json', (manifest) => { manifest.checks.goRaceEnabled = false; return manifest; });
    await assert.rejects(verifyReleasePackage(value.directory, value.context, platform), /checks are missing/);
  }
  for (const mutate of [
    (manifest) => { manifest.checks.scans.pop(); return manifest; },
    (manifest) => { manifest.checks.scans[0].passed = false; return manifest; },
    (manifest) => { manifest.inAppUpdatesEnabled = true; return manifest; },
    (manifest) => { manifest.packaging.lockSHA256 = 'f'.repeat(64); return manifest; },
    (manifest) => { manifest.packagingToolScans = []; return manifest; },
    (manifest) => { delete manifest.packagingToolScans[0].upstreamAr; return manifest; },
  ]) {
    const value = await fixture(t, 'linux-x64');
    await rewriteCovered(value, 'build-manifest.json', mutate);
    await assert.rejects(verifyReleasePackage(value.directory, value.context, 'linux-x64'));
  }
  const value = await fixture(t);
  await rewriteCovered(value, 'sbom.cdx.json', (sbom) => { sbom.metadata.properties[0].value = 'c'.repeat(40); return sbom; });
  await assert.rejects(verifyReleasePackage(value.directory, value.context, 'win32-x64'), /SBOM provenance/);
});

test('expired or unapproved advisory exceptions cannot survive bundle assembly', async (t) => {
  const value = await fixture(t);
  await rewriteCovered(value, 'build-manifest.json', (manifest) => {
    manifest.checks.scans[1].exceptions = [{ advisory: 'https://example.invalid/unknown', expiresOn: '2020-01-01' }];
    return manifest;
  });
  await assert.rejects(verifyReleasePackage(value.directory, value.context, 'win32-x64'), /unapproved or expired/);
});

test('all checked-in workflow actions use full commit SHAs', async () => {
  const directory = new URL('../../.github/workflows/', import.meta.url);
  for (const filename of await readdir(directory)) {
    if (!/\.ya?ml$/.test(filename)) continue;
    const contents = await readFile(new URL(filename, directory), 'utf8');
    for (const match of contents.matchAll(/uses:\s*(\S+)/g)) {
      assert.match(match[1], /^[\w.-]+\/[\w.-]+@[a-f0-9]{40}$/, `${filename}: mutable action reference`);
    }
  }
});

test('release workflow separates build, attestation and publishing credentials with immutable actions', async () => {
  const workflow = await readFile(new URL('../../.github/workflows/release.yml', import.meta.url), 'utf8');
  const jobs = workflow.split(/^  (?:build|assemble|attest|release):$/m);
  assert.equal(jobs.length, 5);
  assert.doesNotMatch(jobs[1], /(?:contents|id-token|attestations): write/);
  assert.doesNotMatch(jobs[2], /(?:contents|id-token|attestations): write/);
  assert.match(jobs[3], /id-token: write/);
  assert.match(jobs[3], /attestations: write/);
  assert.match(jobs[3], /uses: actions\/attest@[a-f0-9]{40}/);
  assert.doesNotMatch(jobs[3], /contents: write|uses: actions\/checkout|run:/);
  assert.match(jobs[4], /contents: write/);
  assert.match(jobs[4], /run: node scripts\/whatsup\.mjs release-publish/);
  assert.doesNotMatch(jobs[4], /id-token: write|attestations: write|artifact-metadata: write/);
  assert.doesNotMatch(jobs[1] + jobs[2], /artifact-metadata: write/);
  assert.match(workflow, /ubuntu-24\.04/);
  assert.match(workflow, /windows-latest/);
  for (const match of workflow.matchAll(/uses: (\S+)/g)) assert.match(match[1], /^[\w.-]+\/[\w.-]+@[a-f0-9]{40}$/);
  assert.doesNotMatch(workflow, /continue-on-error|if: always\(\)|--clobber|release publish/);
});
