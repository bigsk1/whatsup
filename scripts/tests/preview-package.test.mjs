import assert from 'node:assert/strict';
import { cp, mkdir, mkdtemp, readdir, readFile, rm, symlink, writeFile } from 'node:fs/promises';
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
const advisory = 'https://github.com/advisories/GHSA-fc5q-39xc-vjpf';

function audit() {
  const finding = (name, isDirect, via, effects, range, fixName = 'vite', fixVersion = '6.2.7') => ({
    name, severity: 'high', isDirect, via, effects, range, nodes: [`node_modules/${name}`],
    fixAvailable: { name: fixName, version: fixVersion, isSemVerMajor: true },
  });
  return { auditReportVersion: 2, vulnerabilities: {
    '@sveltejs/vite-plugin-svelte': finding('@sveltejs/vite-plugin-svelte', true, ['vite'], [], '>=6.0.0-next.0', '@sveltejs/vite-plugin-svelte', '5.1.1'),
    fdir: finding('fdir', false, ['picomatch'], ['svelte-check', 'tinyglobby'], '>=5.2.1'),
    picomatch: finding('picomatch', false, [{ url: advisory, name: 'picomatch', dependency: 'picomatch', severity: 'high',
      range: '<=4.0.7', cwe: ['CWE-1333'], cvss: { score: 7.5, vectorString: 'CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:H' },
    }], ['fdir', 'tinyglobby', 'vite', 'vitest'], '*'),
    'svelte-check': finding('svelte-check', true, ['fdir'], [], '>=4.0.0', 'svelte-check', '3.8.6'),
    tinyglobby: finding('tinyglobby', false, ['fdir', 'picomatch'], ['vite', 'vitest'], '*'),
    vite: finding('vite', true, ['picomatch', 'tinyglobby'], ['@sveltejs/vite-plugin-svelte', 'vitest'], '>=6.3.0-beta.0'),
    vitest: finding('vitest', true, ['picomatch', 'tinyglobby', 'vite'], [], '>=3.1.2', 'vitest', '3.1.4'),
  }, metadata: { vulnerabilities: { info: 0, low: 0, moderate: 0, high: 7, critical: 0, total: 7 } } };
}

test('the advisory exception covers only the reviewed frontend tooling graph and expires closed', async () => {
  const exception = await json('config/advisory-exception.json');
  const lock = await json('frontend/package-lock.json');
  const options = { exception, lock, frontend: true, now: new Date('2026-11-17T12:00:00Z') };
  assert.deepEqual(evaluateNpmAudit(audit(), options), { passed: true, exceptions: [{
    advisory, package: 'picomatch', version: '4.0.7', scope: 'frontend build and test tooling only', expiresOn: '2026-12-17',
  }] });
  const reordered = audit();
  reordered.vulnerabilities.vite.via.reverse();
  reordered.vulnerabilities.picomatch.effects.reverse();
  assert.equal(evaluateNpmAudit(reordered, options).passed, true);
  const alternative = audit();
  for (const name of ['fdir', 'picomatch', 'tinyglobby']) {
    alternative.vulnerabilities[name].fixAvailable = { name: 'vitest', version: '3.1.4', isSemVerMajor: true };
  }
  assert.equal(evaluateNpmAudit(alternative, options).passed, true);
  for (const change of [
    (value) => { value.vulnerabilities.picomatch.severity = 'critical'; },
    (value) => { value.vulnerabilities.picomatch.via[0].url = 'https://github.com/advisories/GHSA-gcqx-w48m-w2h4'; },
    (value) => { value.vulnerabilities.picomatch.via.push({ ...value.vulnerabilities.picomatch.via[0], url: 'https://github.com/advisories/GHSA-new' }); },
    (value) => { value.vulnerabilities.picomatch.via[0].cvss.score = 8; },
    (value) => { value.vulnerabilities.picomatch.fixAvailable.isSemVerMajor = false; },
    (value) => { value.vulnerabilities.picomatch.fixAvailable.version = '8.3.9'; },
    (value) => { value.vulnerabilities.vite.fixAvailable = { name: 'vitest', version: '3.1.4', isSemVerMajor: true }; },
    (value) => { value.vulnerabilities.picomatch.nodes.push('node_modules/other/node_modules/picomatch'); },
    (value) => { value.vulnerabilities.picomatch.effects.push('another-package'); },
    (value) => { value.vulnerabilities.vite.isDirect = false; },
    (value) => { value.vulnerabilities.vite.via = ['another-package', 'picomatch']; },
    (value) => { value.vulnerabilities.other = value.vulnerabilities.picomatch; },
    (value) => { value.metadata.vulnerabilities.total = 8; },
    (value) => { value.metadata.vulnerabilities.critical = 1; },
    (value) => { value.error = { code: 'EAI_AGAIN' }; },
  ]) {
    const value = audit(); change(value);
    assert.throws(() => evaluateNpmAudit(value, options));
  }
  assert.throws(() => evaluateNpmAudit(audit(), { ...options, frontend: false }));
  for (const now of ['2026-11-16T23:59:59Z', '2026-12-17T00:00:00Z'])
    assert.throws(() => evaluateNpmAudit(audit(), { ...options, now: new Date(now) }));
  for (const expiresOn of ['2026-11-31', '2026-12-19'])
    assert.throws(() => evaluateNpmAudit(audit(), { ...options, exception: { ...exception, expiresOn } }));
  for (const change of [
    (value) => { value.packages['node_modules/picomatch'].version = '4.0.8'; },
    (value) => { value.packages['node_modules/vite'].version = '8.3.9'; },
    (value) => { delete value.packages['node_modules/picomatch'].devOptional; },
    (value) => { value.packages['node_modules/svelte'].dependencies.picomatch = '4.0.7'; },
    (value) => { value.packages['node_modules/other/node_modules/picomatch'] = value.packages['node_modules/picomatch']; },
  ]) {
    const wrongLock = structuredClone(lock); change(wrongLock);
    assert.throws(() => evaluateNpmAudit(audit(), { ...options, lock: wrongLock }));
  }
  const versionBump = structuredClone(lock);
  versionBump.version = versionBump.packages[''].version = '0.2.1';
  assert.equal(evaluateNpmAudit(audit(), { ...options, lock: versionBump }).passed, true);
  const clean = audit();
  clean.vulnerabilities = {};
  assert.throws(() => evaluateNpmAudit(clean, options));
  clean.metadata.vulnerabilities = { info: 0, low: 0, moderate: 0, high: 0, critical: 0, total: 0 };
  assert.deepEqual(evaluateNpmAudit(clean, { ...options, frontend: false }), { passed: true, exceptions: [] });
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
  assert.match(nfpm, /dst: \/usr\/share\/applications\/io\.github\.bigsk1\.whatsup\.desktop/);
  const post = await read('build/linux/nfpm/scripts/postinstall.sh');
  const remove = await read('build/linux/nfpm/scripts/postremove.sh');
  assert.match(post, /apparmor_parser -r \/etc\/apparmor.d\/whatsup-development/);
  assert.doesNotMatch(post + remove, /sysctl|rm -r|\.vault|\.sqlite/);
  assert.match(remove, /remove\|purge\)/);
});

test('every retained third-party license file is installed by both packages and removed by the Windows uninstaller', async () => {
  const nsis = (await read('build/windows/nsis/project.nsi')).replaceAll('\r\n', '\n');
  const nfpm = (await read('build/linux/nfpm/nfpm.yaml')).replaceAll('\r\n', '\n');
  const entries = await readdir(path.join(root, 'third_party/licenses'), { recursive: true, withFileTypes: true });
  const files = entries.filter((entry) => entry.isFile())
    .map((entry) => path.relative(path.join(root, 'third_party/licenses'), path.join(entry.parentPath, entry.name)).split(path.sep).join('/'))
    .sort();
  // A notice kept in the source tree but left out of an installer is a
  // distribution defect that no other check would notice.
  assert.ok(files.length >= 5 && files.includes('pion-MIT.txt') && files.includes('pion-transport-xor-BSD-3-Clause.txt'));
  for (const file of files) {
    const windowsPath = file.replaceAll('/', '\\');
    assert.ok(nsis.includes(`File "..\\..\\..\\third_party\\licenses\\${windowsPath}"`), `Windows installer omits ${file}`);
    assert.ok(nsis.includes(`Delete "$INSTDIR\\third_party\\licenses\\${windowsPath}"`), `Windows uninstaller leaves ${file} behind`);
    assert.ok(nfpm.includes(`  - src: ./third_party/licenses/${file}\n    dst: /usr/share/doc/whatsup/third_party/licenses/${file}\n`), `Linux package omits ${file}`);
  }
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
