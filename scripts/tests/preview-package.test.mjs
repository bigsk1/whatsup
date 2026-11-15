import assert from 'node:assert/strict';
import { cp, mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { evaluateNpmAudit } from '../lib/npm-advisories.mjs';
import { sourceToolIdentity, validateToolAsset, verifyArWriterSubset } from '../lib/packaging-tools.mjs';
import { npmLockComponents, previewArtifactName } from '../lib/preview-package.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const read = (name) => readFile(path.join(root, name), 'utf8');
const json = async (name) => JSON.parse(await read(name));
const advisory = 'https://github.com/advisories/GHSA-gcqx-w48m-w2h4';

function audit() {
  return { auditReportVersion: 2, vulnerabilities: {
    '@sveltejs/vite-plugin-svelte': { name: '@sveltejs/vite-plugin-svelte', severity: 'low', nodes: ['node_modules/@sveltejs/vite-plugin-svelte'], effects: [], via: ['deepmerge'], fixAvailable: false },
    deepmerge: { name: 'deepmerge', severity: 'low', nodes: ['node_modules/deepmerge'], effects: ['@sveltejs/vite-plugin-svelte'], fixAvailable: false,
      via: [{ url: advisory, name: 'deepmerge', severity: 'low' }] },
  }, metadata: { vulnerabilities: { total: 2 } } };
}

test('the advisory exception covers only the exact dev chain and expires closed', async () => {
  const exception = await json('config/advisory-exception.json');
  const lock = await json('frontend/package-lock.json');
  const options = { exception, lock, frontend: true, now: new Date('2026-11-15T12:00:00Z') };
  assert.equal(evaluateNpmAudit(audit(), options).exceptions[0].expiresOn, '2026-12-15');
  for (const change of [
    (value) => { value.vulnerabilities.deepmerge.severity = 'moderate'; },
    (value) => { value.vulnerabilities.deepmerge.via[0].url = 'https://github.com/advisories/GHSA-new'; },
    (value) => { value.vulnerabilities.deepmerge.fixAvailable = true; },
    (value) => { value.vulnerabilities.deepmerge.nodes.push('node_modules/other/node_modules/deepmerge'); },
    (value) => { value.vulnerabilities.other = value.vulnerabilities.deepmerge; },
    (value) => { value.metadata.vulnerabilities.total = 3; },
    (value) => { value.error = { code: 'EAI_AGAIN' }; },
  ]) {
    const value = audit(); change(value);
    assert.throws(() => evaluateNpmAudit(value, options));
  }
  assert.throws(() => evaluateNpmAudit(audit(), { ...options, frontend: false }));
  assert.throws(() => evaluateNpmAudit(audit(), { ...options, now: new Date('2026-12-15T00:00:00Z') }));
  const wrongLock = structuredClone(lock); wrongLock.packages['node_modules/deepmerge'].dev = false;
  assert.throws(() => evaluateNpmAudit(audit(), { ...options, lock: wrongLock }));
  wrongLock.packages['node_modules/deepmerge'].dev = true; wrongLock.packages['node_modules/deepmerge'].version = '4.3.2';
  assert.throws(() => evaluateNpmAudit(audit(), { ...options, lock: wrongLock }));
  assert.throws(() => evaluateNpmAudit({ auditReportVersion: 2, vulnerabilities: {}, metadata: { vulnerabilities: { total: 1 } } }, options));
});

test('packaging tool pins require an official fixed HTTPS asset and SHA256', async () => {
  const config = await json('config/packaging-tools.json');
  for (const [name, definition] of Object.entries(config.tools)) {
    for (const target of Object.keys(definition).filter((key) => /^(?:linux|win32)-/.test(key))) {
      assert.equal(validateToolAsset(name, definition, target), definition[target]);
      const mutations = definition.kind === 'source' ? [{ executable: '../outside' }]
        : [{ sha256: 'latest' }, { url: 'https://evil.invalid/tool.zip' }, { executable: '../outside' }];
      for (const update of mutations) {
        assert.throws(() => validateToolAsset(name, { ...definition, [target]: { ...definition[target], ...update } }, target));
      }
      if (definition.kind === 'source') assert.throws(() => validateToolAsset(name, { ...definition, module: 'evil.invalid/tool' }, target));
    }
  }
});

test('the writer-only ar subset rejects new reader code and every retained-byte change, and binds source tool identity', async (t) => {
  const fixture = await mkdtemp(path.join(os.tmpdir(), 'whatsup-buildtool-subset-'));
  t.after(() => rm(fixture, { recursive: true, force: true }));
  for (const directory of ['config', 'third_party', 'tools']) await mkdir(path.join(fixture, directory));
  await cp(path.join(root, 'third_party/ar-writer'), path.join(fixture, 'third_party/ar-writer'), { recursive: true });
  await cp(path.join(root, 'tools/packaging'), path.join(fixture, 'tools/packaging'), { recursive: true });
  for (const name of ['nfpm-ar-writer.json', 'packaging-tools.json']) await cp(path.join(root, 'config', name), path.join(fixture, 'config', name));
  await verifyArWriterSubset(fixture);
  const expected = await sourceToolIdentity({ root, name: 'nfpm' });
  assert.deepEqual(await sourceToolIdentity({ root: fixture, name: 'nfpm' }), expected);
  await writeFile(path.join(fixture, 'third_party/ar-writer/reader.go'), 'package ar\ntype Reader struct {}\n');
  await assert.rejects(verifyArWriterSubset(fixture), /inventory/);
  await rm(path.join(fixture, 'third_party/ar-writer/reader.go'));
  await writeFile(path.join(fixture, 'third_party/ar-writer/writer.go'), 'package ar\n');
  await assert.rejects(sourceToolIdentity({ root: fixture, name: 'nfpm' }), /bytes changed/);
  await cp(path.join(root, 'third_party/ar-writer/writer.go'), path.join(fixture, 'third_party/ar-writer/writer.go'));
  await writeFile(path.join(fixture, 'tools/packaging/extra.go'), 'package main\nfunc init() {}\n');
  await assert.rejects(sourceToolIdentity({ root: fixture, name: 'nfpm' }), /wrapper inventory/);
  await rm(path.join(fixture, 'tools/packaging/extra.go'));
  const main = path.join(fixture, 'tools/packaging/main.go');
  await rm(main);
  try {
    await symlink(path.join(root, 'tools/packaging/main.go'), main, 'file');
    await assert.rejects(sourceToolIdentity({ root: fixture, name: 'nfpm' }), /without links/);
    await rm(main);
  } catch (error) {
    if (!['EPERM', 'EACCES'].includes(error.code)) throw error;
  }
  await cp(path.join(root, 'tools/packaging/main.go'), main);
  await writeFile(path.join(fixture, 'tools/packaging/main.go'), '// different build source\npackage main\n');
  assert.notEqual((await sourceToolIdentity({ root: fixture, name: 'nfpm' })).lockSHA256, expected.lockSHA256);
});

test('frontend SBOM inventory is deterministic and rejects unsafe, malformed or unbounded locks', async () => {
  const lock = await json('frontend/package-lock.json');
  const components = npmLockComponents(lock);
  assert.deepEqual(components, npmLockComponents(lock));
  assert.equal(components.length, Object.keys(lock.packages).length - 1);
  assert.ok(components.some((entry) => entry.name === 'svelte'));
  assert.ok(components.every((entry) => entry.purl.startsWith('pkg:npm/') && !JSON.stringify(entry).includes('C:\\')));
  for (const update of [{ version: '../escape' }, { integrity: 'sha512-invalid' }, { link: true }]) {
    const wrong = structuredClone(lock); Object.assign(wrong.packages['node_modules/svelte'], update);
    assert.throws(() => npmLockComponents(wrong));
  }
  const wrong = structuredClone(lock); wrong.packages['../outside'] = wrong.packages['node_modules/svelte'];
  assert.throws(() => npmLockComponents(wrong));
});

test('preview installer names are explicit and unsupported native targets fail', () => {
  assert.equal(previewArtifactName('0.1.0', 'win32', 'x64'), 'WhatsUp-0.1.0-windows-x64-setup.exe');
  assert.equal(previewArtifactName('0.1.0', 'linux', 'x64'), 'whatsup_0.1.0_amd64.deb');
  for (const [version, platform, arch] of [['0.1.0', 'linux', 'arm64'], ['latest', 'win32', 'x64'], ['0.1.0', 'darwin', 'x64']]) {
    assert.throws(() => previewArtifactName(version, platform, arch));
  }
});

test('native uninstall recipes preserve profile data and retain scoped Linux sandboxing', async () => {
  const nsis = await read('build/windows/nsis/project.nsi');
  const nfpm = await read('build/linux/nfpm/nfpm.yaml');
  assert.doesNotMatch(nsis, /RMDir\s+\/r|DeleteRegKey[^\n]+(?:Credential|AppUserModelId)|Delete[^\n]+(?:\.vault|\.sqlite)/i);
  assert.match(nsis, /RequestExecutionLevel user/);
  assert.match(nsis, /Quit WhatsUp from its tray menu/);
  assert.match(nfpm, /dst: \/opt\/whatsup-development\/whatsup\n/);
  assert.match(nfpm, /dst: \/etc\/apparmor.d\/whatsup-development/);
  assert.match(nfpm, /dst: \/usr\/share\/applications\/whatsup.desktop/);
  const post = await read('build/linux/nfpm/scripts/postinstall.sh');
  const remove = await read('build/linux/nfpm/scripts/postremove.sh');
  assert.match(post, /apparmor_parser -r \/etc\/apparmor.d\/whatsup-development/);
  assert.doesNotMatch(post + remove, /sysctl|rm -r|\.vault|\.sqlite/);
  assert.match(remove, /remove\|purge\)/);
});

test('the Linux launcher icon has a self-contained asset in a registered hicolor directory', async () => {
  const config = (await read('build/linux/nfpm/nfpm.yaml')).replaceAll('\r\n', '\n');
  const launcher = await read('build/linux/desktop');
  const iconName = /^Icon=([A-Za-z0-9._-]+)$/m.exec(launcher)?.[1];
  assert.equal(iconName, 'whatsup');
  const assets = config.split(/(?=^  - src: )/m).map((block) => ({
    source: /^  - src: (.+)$/m.exec(block)?.[1],
    destination: /^    dst: (.+)$/m.exec(block)?.[1],
    mode: /^      mode: (.+)$/m.exec(block)?.[1],
  })).filter((entry) => entry.destination?.startsWith('/usr/share/icons/hicolor/'));
  // A PNG in an unregistered size directory is not discoverable by icon name.
  // scalable/apps is listed by the supported Ubuntu hicolor index.theme.
  const scalable = assets.find((entry) => entry.destination === `/usr/share/icons/hicolor/scalable/apps/${iconName}.svg`);
  assert.ok(scalable, 'The icon cannot be installed only into unregistered 1024x1024/apps');
  assert.equal(scalable.source, './build/appicon.svg');
  assert.equal(scalable.mode, '0644');
  const svg = await read(scalable.source);
  assert.match(svg, /^<svg\b[^>]*viewBox="0 0 1024 1024"/);
  assert.doesNotMatch(svg, /<(?:script|image|foreignObject)\b|<!DOCTYPE|<!ENTITY/i);
  for (const [, , target] of svg.matchAll(/\b(?:href|xlink:href)\s*=\s*(["'])(.*?)\1/g)) assert.match(target, /^#[\w.-]+$/);
  for (const [, target] of svg.matchAll(/url\((.*?)\)/g)) assert.match(target, /^#[\w.-]+$/);
});
