import assert from 'node:assert/strict';
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import test from 'node:test';
import { digestReviewFiles, verifyDerivedReviewSource } from '../lib/review-source.mjs';

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
