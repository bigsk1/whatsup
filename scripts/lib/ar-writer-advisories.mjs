import { verifyArWriterSubset } from './packaging-tools.mjs';

const moduleName = 'github.com/blakesmith/ar';
const upstreamVersion = 'v0.0.0-20190502131153-809d4375e1fb';
const knownID = 'GO-2026-6905';
const officialSource = `https://vuln.go.dev/ID/${knownID}.json`;
const fail = () => { throw new Error('The retained ar writer has an unknown, changed or unclassified upstream advisory'); };

function checkedPolicy(manifest) {
  const policy = manifest?.removedAdvisory;
  if (manifest?.module !== moduleName || manifest?.upstreamVersion !== upstreamVersion
      || policy?.id !== knownID || policy.officialSource !== officialSource || policy.package !== moduleName
      || JSON.stringify(policy.symbols) !== JSON.stringify(['Reader.Read']) || JSON.stringify(policy.aliases) !== '[]') fail();
  return policy;
}

function classify(item, policy) {
  if (!item || item.id !== policy.id || item.withdrawn || JSON.stringify(item.aliases ?? []) !== JSON.stringify(policy.aliases)
      || item.database_specific?.review_status !== 'REVIEWED' || !Array.isArray(item.affected) || item.affected.length !== 1) fail();
  const affected = item.affected[0];
  const imports = affected.ecosystem_specific?.imports;
  if (affected.package?.ecosystem !== 'Go' || affected.package?.name !== moduleName
      || !Array.isArray(imports) || imports.length !== 1 || imports[0].path !== moduleName
      || JSON.stringify(imports[0].symbols) !== JSON.stringify(policy.symbols)) fail();
}

export function validateArAdvisoryEvidence(evidence, manifest) {
  const policy = checkedPolicy(manifest);
  if (Buffer.byteLength(JSON.stringify(evidence) ?? '', 'utf8') > (128 << 10)) fail();
  if (evidence?.module !== moduleName || evidence.version !== upstreamVersion || evidence.passed !== true
      || !evidence.raw || !evidence.raw.query || typeof evidence.raw.query !== 'object' || Array.isArray(evidence.raw.query)
      || Object.keys(evidence.raw.query).some((key) => key !== 'vulns')
      || evidence.raw.query.error || (evidence.raw.query.vulns !== undefined && !Array.isArray(evidence.raw.query.vulns))
      || (evidence.raw.query.vulns?.length ?? 0) > 256) fail();
  classify(evidence.raw.official, policy);
  const ids = [];
  for (const item of evidence.raw.query.vulns ?? []) {
    classify(item, policy);
    if (ids.includes(item.id)) fail();
    ids.push(item.id);
  }
  ids.sort();
  if (JSON.stringify(evidence.ids) !== JSON.stringify(ids)
      || JSON.stringify(evidence.codeRemoved) !== JSON.stringify([{ id: policy.id, symbols: policy.symbols }])) fail();
  return true;
}

async function boundedJson(response) {
  if (!response.ok || response.redirected || Number(response.headers.get('content-length') ?? 0) > (2 << 20)) throw new Error('Version-aware ar advisory query failed');
  const reader = response.body?.getReader();
  if (!reader) throw new Error('Ar advisory response is missing');
  const chunks = []; let size = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.length;
      if (size > (2 << 20)) throw new Error('Ar advisory response exceeds its size bound');
      chunks.push(value);
    }
  } finally { await reader.cancel(); }
  try { return JSON.parse(Buffer.concat(chunks).toString('utf8')); }
  catch { throw new Error('Malformed ar advisory response'); }
}

export async function arWriterAdvisories(root, fetcher = fetch) {
  // The permission to classify a removed-reader finding comes from the exact
  // reviewed subset, never a caller-supplied module label or generic exception.
  const manifest = await verifyArWriterSubset(root);
  const policy = checkedPolicy(manifest);
  const query = await boundedJson(await fetcher('https://api.osv.dev/v1/query', {
    method: 'POST', redirect: 'error', signal: AbortSignal.timeout(30000),
    headers: { 'content-type': 'application/json', accept: 'application/json', 'user-agent': 'WhatsUp-build-workflow' },
    body: JSON.stringify({ package: { ecosystem: 'Go', name: moduleName }, version: upstreamVersion }),
  }));
  const official = await boundedJson(await fetcher(officialSource, {
    redirect: 'error', signal: AbortSignal.timeout(30000), headers: { accept: 'application/json', 'user-agent': 'WhatsUp-build-workflow' },
  }));
  const evidence = { module: moduleName, version: upstreamVersion, passed: true,
    ids: (query?.vulns ?? []).map((item) => item?.id).sort(),
    codeRemoved: [{ id: policy.id, symbols: policy.symbols }], raw: { query, official } };
  validateArAdvisoryEvidence(evidence, manifest);
  return evidence;
}
