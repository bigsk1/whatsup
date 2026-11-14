import assert from 'node:assert/strict';
import { cp, mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { applyWailsTextPatch, ensureWailsPatch, isVerifiedWailsPatch, mergeUpstreamChecksumRows, prepareOfficialToolModule, retainReplacementSums, verifyWailsDefinition, verifyWailsPatch, wailsTreeInventory } from '../lib/wails-patch.mjs';
import { verifyTailcatPatch } from '../lib/tailcat-patch.mjs';
import { assertReviewedGoGraph, assertToolBuildInfo, validSecurityScans, within } from '../lib/workflow.mjs';
import { replacementAdvisories } from '../lib/replacement-advisories.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const policy = JSON.parse(await readFile(path.join(root, 'config/toolchain.json'), 'utf8'));
const wails = 'github.com/wailsapp/wails/v3';
const target = './.local/modules/wails-v3';

async function fixture(t) {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'whatsup-wails-patch-'));
  t.after(async () => { assert.ok(within(os.tmpdir(), directory)); await rm(directory, { recursive: true, force: true }); });
  for (const name of ['config', 'third_party/patches', '.local/modules/wails-v3/pkg']) await mkdir(path.join(directory, name), { recursive: true });
  for (const name of ['go.mod', 'go.sum', 'third_party/patches/wails-v3-native.patch']) await cp(path.join(root, name), path.join(directory, name));
  const location = path.join(directory, target);
  await writeFile(path.join(location, 'go.mod'), `module ${wails}\n\ngo 1.27.4\n`);
  await writeFile(path.join(location, 'pkg/native.go'), 'package application\n// Synthetic inventory; never compiled.\n');
  const manifest = JSON.parse(await readFile(path.join(root, 'config/wails-patch.json'), 'utf8'));
  const inventory = wailsTreeInventory(location);
  manifest.patchedTree = { sha256: inventory.sha256, count: inventory.count, size: inventory.size };
  await writeFile(path.join(directory, 'config/wails-patch.json'), JSON.stringify(manifest));
  return directory;
}

test('Wails source approval checks the whole tree and cannot be forged or waive an unrelated replacement', async (t) => {
  const directory = await fixture(t);
  const approved = verifyWailsPatch(directory);
  assert.equal(isVerifiedWailsPatch(approved), true);
  assert.equal(isVerifiedWailsPatch({ ...approved }), false);
  assert.equal(isVerifiedWailsPatch(ensureWailsPatch(directory, { executable: 'must-not-run-for-a-verified-existing-tree' })), true);
  const tailcat = verifyTailcatPatch(root, policy.reviewedGoPins);
  const graph = Object.entries({ ...policy.reviewedGoPins, [wails]: 'v3.1.0' }).map(([name, version]) => `${name} ${version}${name === wails ? ` => ${target}` : name === tailcat.module ? ` => ${tailcat.path}` : ''}`).join('\n');
  assertReviewedGoGraph(graph, policy.reviewedGoPins, [tailcat, approved]);
  assert.throws(() => assertReviewedGoGraph(graph, policy.reviewedGoPins, [tailcat, { ...approved }]), /Unreviewed/);
  assert.throws(() => assertReviewedGoGraph(`${graph}\nexample.org/extra v1.0.0 => ./extra`, policy.reviewedGoPins, [tailcat, approved]), /Unreviewed/);
  const info = 'tool: go1.27.4\n\tmod\tgithub.com/wailsapp/wails/v3\tv3.1.0\th1:synthetic';
  assertToolBuildInfo(info, wails, 'v3.1.0', graph, '1.27.4', [tailcat, approved]);
  assert.throws(() => assertToolBuildInfo(`${info}\n\t=>\t${target}\t(devel)`, wails, 'v3.1.0', graph, '1.27.4', [tailcat, approved]), /Installed tool.*replacement/);
});

test('Wails verifier rejects byte changes, missing files, extra files and source directory links', async (t) => {
  const directory = await fixture(t); const source = path.join(directory, target, 'pkg/native.go'); const original = await readFile(source);
  await writeFile(source, Buffer.concat([original, Buffer.from('// unreviewed\n')])); assert.throws(() => verifyWailsPatch(directory), /changed or unsafe/);
  await rm(source); assert.throws(() => verifyWailsPatch(directory), /changed or unsafe/);
  await writeFile(source, original); await writeFile(path.join(directory, target, 'extra.go'), 'package application\n'); assert.throws(() => verifyWailsPatch(directory), /changed or unsafe/);
  await rm(path.join(directory, target, 'extra.go')); await rm(path.join(directory, target, 'pkg'), { recursive: true });
  await symlink(path.join(root, '.local/modules/wails-v3/pkg'), path.join(directory, target, 'pkg'), process.platform === 'win32' ? 'junction' : 'dir');
  assert.throws(() => verifyWailsPatch(directory), /changed or unsafe/);
});

test('Wails definition refuses altered patch bytes, replacement spelling and checksum authority', async (t) => {
  const directory = await fixture(t); const patch = path.join(directory, 'third_party/patches/wails-v3-native.patch'); const bytes = await readFile(patch);
  await writeFile(patch, Buffer.concat([bytes, Buffer.from('\n')])); assert.throws(() => verifyWailsDefinition(directory), /changed or unsafe/); await writeFile(patch, bytes);
  const mod = path.join(directory, 'go.mod'); const original = await readFile(mod, 'utf8');
  await writeFile(mod, original.replace(target, '../outside')); assert.throws(() => verifyWailsDefinition(directory), /changed or unsafe/); await writeFile(mod, original);
  const sum = path.join(directory, 'go.sum'); await writeFile(sum, (await readFile(sum, 'utf8')).replace(/^github.com\/wailsapp\/wails\/v3 v3\.1\.0 h1:[^\n]+\n/m, '')); assert.throws(() => verifyWailsDefinition(directory), /changed or unsafe/);
});

test('exact patch application refuses fuzz, traversal and shifted hunk positions', async (t) => {
  const directory = await fixture(t); const location = path.join(directory, target); const filename = path.join(location, 'sample.go'); await writeFile(filename, 'package sample\n// old\n');
  const patch = Buffer.from('--- a/sample.go\n+++ b/sample.go\n@@ -1,2 +1,2 @@\n package sample\n-// old\n+// reviewed\n--- /dev/null\n+++ b/pkg/new/deep.go\n@@ -0,0 +1 @@\n+package sample\n');
  applyWailsTextPatch(location, patch); assert.equal(await readFile(filename, 'utf8'), 'package sample\n// reviewed\n'); assert.equal(await readFile(path.join(location, 'pkg/new/deep.go'), 'utf8'), 'package sample\n');
  assert.throws(() => applyWailsTextPatch(location, patch), /changed or unsafe/);
  for (const value of ['--- /dev/null\n+++ b/../outside.go\n@@ -0,0 +1 @@\n+package bad\n', '--- a/sample.go\n+++ b/sample.go\n@@ -1,2 +2,2 @@\n package sample\n-// reviewed\n+// unreviewed\n']) assert.throws(() => applyWailsTextPatch(location, Buffer.from(value)), /changed or unsafe/);
});

test('official tools keep all committed requirements/sums and remove exactly the private replacements', async (t) => {
  const directory = await fixture(t); const destination = prepareOfficialToolModule(directory); const mod = await readFile(path.join(destination, 'go.mod'), 'utf8'); const original = await readFile(path.join(directory, 'go.mod'), 'utf8');
  assert.equal(mod, original.replace(/^module [^\r\n]+/m, 'module whatsup-official-tools').replace(/^replace [^\r\n]+\r?\n?/gm, ''));
  assert.doesNotMatch(mod, /^\s*replace\b/m); assert.equal(await readFile(path.join(destination, 'go.sum'), 'utf8'), await readFile(path.join(directory, 'go.sum'), 'utf8'));
});

test('replacement scans use verified upstream versions and fail closed on transport/malformed results', async (t) => {
  const directory = await fixture(t); const approvals = [verifyTailcatPatch(root, policy.reviewedGoPins), verifyWailsPatch(directory)]; const queries = [];
  const result = await replacementAdvisories(approvals, async (url, options) => { assert.equal(url, 'https://api.osv.dev/v1/query'); assert.equal(options.redirect, 'error'); queries.push(JSON.parse(options.body)); return new Response('{}'); });
  assert.equal(result.every((entry) => entry.passed), true); assert.deepEqual(queries.map((q) => [q.package.name, q.version]), approvals.map((a) => [a.module, a.version]));
  const advisory = await replacementAdvisories(approvals, async () => new Response('{"vulns":[{"id":"GO-2099-0001"}]}')); assert.equal(advisory[0].passed, false);
  await assert.rejects(() => replacementAdvisories(approvals, async () => new Response('', { status: 503 })), /query failed/);
  await assert.rejects(() => replacementAdvisories(approvals, async () => new Response('not-json')), /Malformed/);
  await assert.rejects(() => replacementAdvisories([approvals[0], { ...approvals[1] }], async () => { throw new Error('must not fetch'); }), /verified source approvals/);
});

test('review evidence requires each version-aware advisory gate exactly once', () => {
  const scans = ['root npm audit', 'frontend npm audit', 'govulncheck', 'Wails CLI binary', 'govulncheck binary',
    'github.com/tailscale/tailcat upstream version advisories', 'github.com/wailsapp/wails/v3 upstream version advisories'].map((name) => ({ name, passed: true }));
  assert.equal(validSecurityScans(scans), true);
  assert.equal(validSecurityScans(scans.slice(0, 5)), false);
  assert.equal(validSecurityScans([...scans.slice(0, 6), scans[5]]), false);
  assert.equal(validSecurityScans([...scans.slice(0, 6), { name: scans[6].name }]), false);
  assert.equal(validSecurityScans(scans.map((scan) => ({ ...scan, passed: false }))), true);
});

test('explicit checksum retention never overwrites conflicts or repairs frozen setup', async (t) => {
  const first = 'github.com/wailsapp/wails/v3 v3.1.0 h1:'+Buffer.alloc(32, 1).toString('base64');
  const changed = first.replace(Buffer.alloc(32, 1).toString('base64'), Buffer.alloc(32, 2).toString('base64'));
  assert.equal(mergeUpstreamChecksumRows(first+'\n', [first]), first+'\n');
  assert.throws(() => mergeUpstreamChecksumRows(first+'\n', [changed]), /changed or unsafe/);
  assert.throws(() => mergeUpstreamChecksumRows('', [first.replace('github.com/wailsapp/wails/v3', 'example.org/unreviewed')]), /changed or unsafe/);
  const directory = await fixture(t);
  await cp(path.join(root, 'config/tailcat-patch.json'), path.join(directory, 'config/tailcat-patch.json'));
  await cp(path.join(root, 'third_party/tailcat'), path.join(directory, 'third_party/tailcat'), { recursive: true });
  const filename = path.join(directory, 'go.sum');
  const pruned = (await readFile(filename, 'utf8')).split('\n').filter((line) => !/^github.com\/(?:wailsapp\/wails\/v3|tailscale\/tailcat) /.test(line)).join('\n');
  await writeFile(filename, pruned);
  assert.throws(() => verifyWailsDefinition(directory), /changed or unsafe/);
  assert.throws(() => ensureWailsPatch(directory), /changed or unsafe/);
  assert.throws(() => retainReplacementSums(directory, policy.reviewedGoPins, { executable: process.execPath }), /checksum resolution failed/);
  assert.equal(await readFile(filename, 'utf8'), pruned);
});
