import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import test from 'node:test';
import { digestReviewFiles, verifyDerivedReviewSource } from '../lib/review-source.mjs';

test('all source-bound WSL commands refresh the derived provenance handoff', async () => {
  const bridge = await readFile(new URL('../wsl-run.sh', import.meta.url), 'utf8');
  const guard = bridge.match(/if (\[\[[^\n]+\]\]); then\n\s+mkdir -p -m 700 "\$snapshot\/\.local"/);
  assert.ok(guard, 'The source-metadata copy must remain a guarded handoff');
  for (const task of ['build-review', 'test-app', 'check', 'package']) {
    assert.match(guard[1], new RegExp(`\\$task = ${task}(?: |\\])`), `${task} must not retain metadata from an earlier build`);
  }
});

test('derived source provenance rejects changed bytes and unsafe inventory paths', async (t) => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'whatsup-source-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  await mkdir(path.join(root, 'internal'));
  await writeFile(path.join(root, 'main.go'), 'package main\n');
  await writeFile(path.join(root, 'internal/data.go'), 'package internal\n');
  const files = ['main.go', 'internal/data.go'];
  const sourceDigest = await digestReviewFiles(root, files);
  const metadata = { schemaVersion: 1, sourceCommit: 'a'.repeat(40), sourceDirty: true, sourceDigest, files };
  assert.equal((await verifyDerivedReviewSource(root, metadata)).sourceDirty, true);
  assert.equal(await digestReviewFiles(root, [...files].reverse()), sourceDigest);
  for (const unsafe of ['../outside', '/etc/passwd', 'C:/outside', '.local/private', 'frontend/bindings/generated.ts', 'build/bin/whatsup', 'main.go\n']) {
    await assert.rejects(verifyDerivedReviewSource(root, { ...metadata, files: [unsafe] }), /unsafe|generated/);
  }
  await assert.rejects(verifyDerivedReviewSource(root, { ...metadata, sourceDirty: 'false' }), /invalid/);
  await writeFile(path.join(root, 'main.go'), 'package changed\n');
  await assert.rejects(verifyDerivedReviewSource(root, metadata), /differs/);
});
