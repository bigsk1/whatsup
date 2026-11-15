import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { arWriterAdvisories, validateArAdvisoryEvidence } from '../lib/ar-writer-advisories.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const module = 'github.com/blakesmith/ar';
const version = 'v0.0.0-20190502131153-809d4375e1fb';
const id = 'GO-2026-6905';
const advisory = () => ({ id, database_specific: { review_status: 'REVIEWED' }, affected: [
  { package: { ecosystem: 'Go', name: module }, ecosystem_specific: { imports: [{ path: module, symbols: ['Reader.Read'] }] } },
] });
const policy = async () => JSON.parse(await readFile(path.join(root, 'config/nfpm-ar-writer.json'), 'utf8'));
const evidence = () => ({ module, version, passed: true, ids: [id], codeRemoved: [{ id, symbols: ['Reader.Read'] }],
  raw: { query: { vulns: [advisory()] }, official: advisory() } });

test('only the exact classified removed-reader finding can pass the upstream ar gate', async () => {
  const manifest = await policy();
  assert.equal(validateArAdvisoryEvidence(evidence(), manifest), true);
  for (const mutate of [
    (value) => { value.raw.query.vulns.push({ ...advisory(), id: 'GO-2026-9999' }); },
    (value) => { value.raw.query.vulns[0].affected[0].ecosystem_specific.imports[0].symbols = ['Writer.Write']; },
    (value) => { value.raw.official.affected[0].ecosystem_specific.imports[0].symbols.push('Writer.WriteHeader'); },
    (value) => { value.raw.official.affected[0].ecosystem_specific = {}; },
    (value) => { value.raw.official.aliases = ['CVE-2026-unknown']; },
    (value) => { value.raw.query.vulns[0].aliases = ['GHSA-unknown']; },
    (value) => { value.raw.query.vulns[0].affected.push(value.raw.query.vulns[0].affected[0]); },
    (value) => { value.raw.query.vulns[0].withdrawn = '2026-11-15T00:00:00Z'; },
    (value) => { value.raw.query.vulns[0].database_specific.review_status = 'UNREVIEWED'; },
    (value) => { value.ids = []; },
    (value) => { value.codeRemoved = []; },
    (value) => { value.passed = false; },
    (value) => { value.raw.query.error = 'service failed'; },
    (value) => { value.raw.query.message = 'service failed'; },
    (value) => { value.raw.query.next_page_token = 'unresolved page'; },
    (value) => { value.raw.query.vulns[0].details = 'x'.repeat(128 << 10); },
  ]) {
    const value = evidence(); mutate(value);
    assert.throws(() => validateArAdvisoryEvidence(value, manifest));
  }
});

test('ar queries are version-aware, bounded, nonredirecting and fail closed on network errors', async () => {
  const calls = [];
  const fetcher = async (url, options) => {
    calls.push({ url, options });
    assert.equal(options.redirect, 'error');
    return Response.json(url === 'https://api.osv.dev/v1/query' ? { vulns: [advisory()] } : advisory());
  };
  const result = await arWriterAdvisories(root, fetcher);
  assert.equal(result.passed, true);
  assert.deepEqual(JSON.parse(calls[0].options.body), { package: { ecosystem: 'Go', name: module }, version });
  assert.equal(calls[1].url, 'https://vuln.go.dev/ID/GO-2026-6905.json');
  for (const response of [
    new Response('unavailable', { status: 503 }),
    new Response('{invalid json'),
    Response.json({ vulns: { malformed: true } }),
    new Response('x'.repeat((2 << 20) + 1)),
  ]) await assert.rejects(arWriterAdvisories(root, async () => response));
  await assert.rejects(arWriterAdvisories(root, async () => { throw new Error('network unavailable'); }));
});
