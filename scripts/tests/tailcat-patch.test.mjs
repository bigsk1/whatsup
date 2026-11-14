import assert from 'node:assert/strict';
import { cp, mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { isVerifiedTailcatPatch, verifyTailcatPatch } from '../lib/tailcat-patch.mjs';
import { assertReviewedGoGraph, assertToolBuildInfo, within } from '../lib/workflow.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const policy = JSON.parse(await readFile(path.join(root, 'config/toolchain.json'), 'utf8'));
const pins = policy.reviewedGoPins;
const module = 'github.com/tailscale/tailcat';
const target = './third_party/tailcat';
const graph = Object.entries(pins).map(([name, version]) => `${name} ${version}${name === module ? ` => ${target}` : ''}`).join('\n');

async function fixture(t, { link = false } = {}) {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'whatsup-tailcat-patch-'));
  t.after(async () => {
    assert.ok(within(os.tmpdir(), directory));
    await rm(directory, { recursive: true, force: true });
  });
  await mkdir(path.join(directory, 'config'));
  await mkdir(path.join(directory, 'third_party'));
  await cp(path.join(root, 'config/tailcat-patch.json'), path.join(directory, 'config/tailcat-patch.json'));
  await cp(path.join(root, 'go.mod'), path.join(directory, 'go.mod'));
  if (link) await symlink(path.join(root, 'third_party/tailcat'), path.join(directory, 'third_party/tailcat'), process.platform === 'win32' ? 'junction' : 'dir');
  else await cp(path.join(root, 'third_party/tailcat'), path.join(directory, 'third_party/tailcat'), { recursive: true });
  return directory;
}

test('reviewed Tailcat replacement requires actual verified file inventory, not a forged approval', () => {
  const approval = verifyTailcatPatch(root, pins);
  assert.equal(isVerifiedTailcatPatch(approval), true);
  assert.equal(isVerifiedTailcatPatch({ ...approval }), false);
  assert.equal(isVerifiedTailcatPatch(null), false);
  assertReviewedGoGraph(graph, pins, approval);
  assert.throws(() => assertReviewedGoGraph(graph, pins), /moved or was replaced/);
  assert.throws(() => assertReviewedGoGraph(graph, pins, { ...approval }), /moved or was replaced/);
  for (const changed of [target + '/../elsewhere', path.resolve(root, 'third_party/tailcat'), './unreviewed']) {
    assert.throws(() => assertReviewedGoGraph(graph.replace(target, changed), pins, approval), /moved or was replaced/);
  }
  assert.throws(() => assertReviewedGoGraph(`${graph}\nexample.org/hidden v1.0.0 => ./other`, pins, approval), /Unreviewed selected graph replacement/);
});

test('Tailcat verifier rejects changed, absent and additional source files', async (t) => {
  const directory = await fixture(t);
  const source = path.join(directory, 'third_party/tailcat/tailcat.go');
  const original = await readFile(source);
  await writeFile(source, Buffer.concat([original, Buffer.from('\n// unreviewed change\n')]));
  assert.throws(() => verifyTailcatPatch(directory, pins), /missing, changed or unsafe/);
  await writeFile(source, original);
  await rm(source);
  assert.throws(() => verifyTailcatPatch(directory, pins), /missing, changed or unsafe/);
  await writeFile(source, original);
  await writeFile(path.join(directory, 'third_party/tailcat/unlisted.go'), 'package tailcat\n');
  assert.throws(() => verifyTailcatPatch(directory, pins), /missing, changed or unsafe/);
});

test('Tailcat verifier rejects traversal, mismatched provenance and unreviewed root replacement', async (t) => {
  const directory = await fixture(t);
  const filename = path.join(directory, 'config/tailcat-patch.json');
  const original = JSON.parse(await readFile(filename, 'utf8'));
  for (const changed of [
    { ...original, upstreamRevision: 'a'.repeat(40) },
    { ...original, version: 'v9.9.9' },
    { ...original, replacement: './third_party/tailcat/../elsewhere' },
    { ...original, files: [{ ...original.files[0], path: '../outside.go' }, ...original.files.slice(1)] },
    { ...original, files: [...original.files, original.files[0]] },
  ]) {
    await writeFile(filename, JSON.stringify(changed));
    assert.throws(() => verifyTailcatPatch(directory, pins), /missing, changed or unsafe/);
  }
  await writeFile(filename, JSON.stringify(original));
  const goMod = path.join(directory, 'go.mod');
  const mod = await readFile(goMod, 'utf8');
  for (const changed of [mod.replace(target, './unreviewed'), `${mod}\nreplace example.org/other => ../outside\n`, mod.replace(`replace ${module} => ${target}`, '')]) {
    await writeFile(goMod, changed);
    assert.throws(() => verifyTailcatPatch(directory, pins), /missing, changed or unsafe/);
  }
});

test('Tailcat verifier refuses module directory symlinks or junctions', async (t) => {
  let directory;
  try { directory = await fixture(t, { link: true }); }
  catch (error) {
    if (['EPERM', 'EACCES'].includes(error.code)) { t.skip('Host does not allow test symlinks'); return; }
    throw error;
  }
  assert.throws(() => verifyTailcatPatch(directory, pins), /missing, changed or unsafe/);
});

test('Tailcat verifier refuses a source symlink even when its bytes match', async (t) => {
  const directory = await fixture(t);
  const source = path.join(directory, 'third_party/tailcat/tailcat.go');
  await rm(source);
  try { await symlink(path.join(root, 'third_party/tailcat/tailcat.go'), source, 'file'); }
  catch (error) {
    if (['EPERM', 'EACCES'].includes(error.code)) { t.skip('Host does not allow test file symlinks'); return; }
    throw error;
  }
  assert.throws(() => verifyTailcatPatch(directory, pins), /missing, changed or unsafe/);
});

test('verified app replacement cannot waive embedded CLI replacements or locked transitive versions', () => {
  const approval = verifyTailcatPatch(root, pins);
  const selected = `example.org/cli v3.1.0\ngithub.com/klauspost/compress v1.20.4\n${graph}`;
  const info = 'tool: go1.27.4\n\tmod\texample.org/cli\tv3.1.0\th1:synthetic\n\tdep\tgithub.com/klauspost/compress\tv1.20.4\th1:synthetic';
  const check = (binary, selectedGraph = selected) => assertToolBuildInfo(binary, 'example.org/cli', 'v3.1.0', selectedGraph, '1.27.4', approval);
  check(info);
  assert.throws(() => check(info.replace('v1.20.4', 'v1.18.7')), /differs from the locked graph/);
  assert.throws(() => check(`${info}\n\tdep\t${module}\t${pins[module]}\th1:synthetic\n\t=>\t${target}\t(devel)`), /Installed tool.*replacement/);
  assert.throws(() => check(info, selected.replace(target, '../outside')), /selected graph replacement/);
});

test('development subprocesses omit proxy, port-mapper and captive-portal hooks and recheck patch before work', async () => {
  const runner = await readFile(path.join(root, 'scripts/whatsup.mjs'), 'utf8');
  for (const tag of ['ts_omit_useproxy', 'ts_omit_portmapper', 'ts_omit_captiveportal', 'ts_omit_ssh']) {
    assert.ok(runner.includes(tag));
  }
  assert.match(runner, /function run\([^]*?verifyTailcatPatch\(root, policy.reviewedGoPins\);/);
});
