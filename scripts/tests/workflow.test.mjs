import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import {
  assertReviewedGoGraph, atLeast, checksums, collectArtifacts, collectSourceFiles, parseVersion, requireApp,
  requireFrontendScripts, requireFullReadiness, safeArtifactName, stableNpmVersion, validatePackageName, within,
} from '../lib/workflow.mjs';

async function cleanup(directory) {
  assert.equal(within(os.tmpdir(), directory), true);
  assert.match(path.basename(directory), /^whatsup-(?:workflow|prerequisites)-/);
  await rm(directory, { recursive: true, force: true });
}

test('tool versions reject moving labels, prereleases, and command fragments', () => {
  for (const bad of ['latest', 'main', '1.2.3-beta.1', '1.2', '01.2.3', '1.2.3 && whoami', '1.2.3\n']) {
    assert.throws(() => parseVersion(bad));
  }
  assert.deepEqual(parseVersion('go1.27.4'), [1, 27, 4]);
  assert.deepEqual(parseVersion('v3.1.0'), [3, 1, 0]);
  assert.equal(atLeast('1.27.4', '1.27.4'), true);
  assert.equal(atLeast('1.27.3', '1.27.4'), false);
  assert.equal(atLeast('1.28.0', '1.27.4'), true);
});

test('dependency names cannot introduce CLI options or traversal', () => {
  assert.equal(validatePackageName('@wailsio/runtime'), '@wailsio/runtime');
  for (const bad of ['--registry=evil', '../x', '@scope/../x', 'x;whoami', 'https://evil/x', 'x\n']) {
    assert.throws(() => validatePackageName(bad));
  }
});

test('artifact destination checks reject outside paths and same-prefix neighbors', () => {
  const root = path.join(os.tmpdir(), 'whatsup-root');
  assert.equal(within(root, path.join(root, 'artifacts', 'app.exe')), true);
  assert.equal(within(root, root), false);
  assert.equal(within(root, path.join(root, '..', 'outside.exe')), false);
  assert.equal(within(root, `${root}-neighbor/app.exe`), false);
});

test('artifact names reject traversal, secret files, and ambiguous checksum records', () => {
  assert.equal(safeArtifactName('WhatsUp-0.1.0-linux-amd64.AppImage'), 'WhatsUp-0.1.0-linux-amd64.AppImage');
  for (const bad of ['../app.exe', 'app.exe\nother.exe', 'app.exe\n', 'identity.private.json', '.hidden.exe', 'a b.exe', 'a..b.exe', 'app.svg']) {
    assert.throws(() => safeArtifactName(bad));
  }
});

test('source checks include untracked Go files and exclude caches and dependency trees', async (t) => {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'whatsup-workflow-'));
  t.after(() => cleanup(directory));
  for (const name of ['internal/chat', 'node_modules/unsafe', '.local/scaffold', 'frontend/dist', 'build/bin']) {
    await mkdir(path.join(directory, name), { recursive: true });
    await writeFile(path.join(directory, name, 'source.go'), 'package synthetic\n');
  }
  await writeFile(path.join(directory, 'untracked_test.go'), 'package synthetic\n');
  assert.deepEqual((await collectSourceFiles(directory, '.go')).map((file) => path.relative(directory, file)), [path.join('internal', 'chat', 'source.go'), 'untracked_test.go']);
});

test('partial implementation and absent real integration cannot pass full packaging guard', async (t) => {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'whatsup-prerequisites-'));
  t.after(() => cleanup(directory));
  await assert.rejects(requireFullReadiness(directory, { applicationReady: false }), /remain blocked/);
  await assert.rejects(requireFullReadiness(directory, { applicationReady: true }), /Real direct/);
  const policy = { applicationReady: true, transportIntegrationVerified: true };
  await assert.rejects(requireFullReadiness(directory, policy), /integration test sources/);
  await mkdir(path.join(directory, 'tests/integration'), { recursive: true });
  await writeFile(path.join(directory, 'tests/integration/README.md'), 'Planned tests only.');
  await assert.rejects(requireFullReadiness(directory, policy), /integration test sources/);
});

test('each individual frontend validation gate is mandatory', () => {
  const names = ['format:check', 'check', 'test', 'test:e2e', 'build', 'test:e2e:bundle'];
  const scripts = Object.fromEntries(names.map((name) => [name, 'synthetic-test-command']));
  for (const name of names) {
    assert.throws(() => requireFrontendScripts({ scripts: { ...scripts, [name]: '' } }, names), new RegExp(`missing: ${name}`));
  }
  requireFrontendScripts({ scripts }, names);
});

test('Go updates cannot move or replace reviewed Tailcat and underlying transport pins', () => {
  const pins = {
    'github.com/tailscale/tailcat': 'v0.7.1-0.20261105151022-e00cdde6ff78',
    'tailscale.com': 'v1.105.0-pre.0.20261013225543-fb79793d99f5',
    'github.com/tailscale/wireguard-go': 'v0.0.0-20260928213353-e61b6b78110b',
  };
  const graph = Object.entries(pins).map(([module, version]) => `${module} ${version}`).join('\n');
  assertReviewedGoGraph(graph, pins);
  for (const module of Object.keys(pins)) {
    assert.throws(() => assertReviewedGoGraph(graph.replace(`${module} ${pins[module]}`, `${module} v9.9.9`), pins), /moved or was replaced/);
    assert.throws(() => assertReviewedGoGraph(graph.replace(`${module} ${pins[module]}`, `${module} ${pins[module]} => ./unreviewed`), pins), /moved or was replaced/);
  }
  assert.throws(() => assertReviewedGoGraph(graph, { ...pins, 'tailscale.com': 'main' }), /immutable/);
});

test('dependency updates keep TypeScript within its reviewed compatible major', () => {
  const metadata = { 'dist-tags': { latest: '7.0.0' }, versions: { '6.0.3': {}, '6.1.0': {}, '6.2.0-beta.1': {}, '7.0.0': {} } };
  assert.equal(stableNpmVersion(metadata), '7.0.0');
  assert.equal(stableNpmVersion(metadata, 6), '6.1.0');
  assert.throws(() => stableNpmVersion(metadata, 5), /No current stable/);
});

test('packaging rejects empty outputs and checksums reflect changed artifact bytes', async (t) => {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'whatsup-workflow-'));
  t.after(() => cleanup(directory));
  await assert.rejects(collectArtifacts(directory), /No native package/);
  await writeFile(path.join(directory, 'identity.private.json'), 'synthetic-secret');
  await mkdir(path.join(directory, 'nested.exe'));
  await writeFile(path.join(directory, 'WhatsUp-0.1.0.exe'), 'first package');
  const files = await collectArtifacts(directory);
  assert.deepEqual(files.map((x) => x.name), ['WhatsUp-0.1.0.exe']);
  const before = await checksums(files);
  assert.equal(before, `${createHash('sha256').update('first package').digest('hex')}  WhatsUp-0.1.0.exe\n`);
  await writeFile(files[0].filename, 'tampered package');
  assert.notEqual(await checksums(files), before);
  assert.equal((await readFile(files[0].filename, 'utf8')), 'tampered package');
});

test('full app commands cannot pass with only workflow files', async (t) => {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'whatsup-prerequisites-'));
  t.after(() => cleanup(directory));
  await assert.rejects(requireApp(directory), /Application prerequisite missing: go.mod/);
  await writeFile(path.join(directory, 'go.mod'), 'module example.invalid/test\n');
  await assert.rejects(requireApp(directory), /Application prerequisite missing: go.sum/);
});
