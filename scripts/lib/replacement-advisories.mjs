import { isVerifiedTailcatPatch } from './tailcat-patch.mjs';
import { isVerifiedWailsPatch } from './wails-patch.mjs';

// govulncheck does not attribute advisories to local replacement paths/versions.
// Keep its source scan and independently query the exact upstream identities.
export async function replacementAdvisories(approvals, fetcher = fetch) {
  if (!Array.isArray(approvals) || approvals.length !== 2
      || approvals.filter(isVerifiedTailcatPatch).length !== 1 || approvals.filter(isVerifiedWailsPatch).length !== 1) {
    throw new Error('Replacement advisory scans require both verified source approvals');
  }
  const results = [];
  for (const approval of approvals) {
    const response = await fetcher('https://api.osv.dev/v1/query', {
      method: 'POST', redirect: 'error', signal: AbortSignal.timeout(30000),
      headers: { 'content-type': 'application/json', accept: 'application/json', 'user-agent': 'WhatsUp-development-workflow' },
      body: JSON.stringify({ package: { ecosystem: 'Go', name: approval.module }, version: approval.version }),
    });
    if (!response.ok || response.redirected || Number(response.headers.get('content-length') ?? 0) > (2 << 20)) throw new Error('Version-aware replacement advisory query failed');
    const reader = response.body?.getReader();
    if (!reader) throw new Error('Version-aware replacement advisory response is missing');
    const chunks = []; let size = 0;
    try { for (;;) { const { value, done } = await reader.read(); if (done) break; size += value.length; if (size > (2 << 20)) throw new Error('Oversized replacement advisory response'); chunks.push(value); } }
    finally { await reader.cancel(); }
    let payload; try { payload = JSON.parse(Buffer.concat(chunks).toString('utf8')); } catch { throw new Error('Malformed replacement advisory response'); }
    if (payload === null || typeof payload !== 'object' || Array.isArray(payload) || payload.error || (payload.vulns !== undefined && !Array.isArray(payload.vulns))
      || (payload.vulns?.length ?? 0) > 256) throw new Error('Invalid replacement advisory result');
    const ids = (payload.vulns ?? []).map((item) => {
      if (typeof item?.id !== 'string' || !/^[A-Za-z0-9._-]{1,128}$/.test(item.id)) throw new Error('Invalid replacement advisory ID');
      return item.id;
    });
    results.push({ module: approval.module, version: approval.version, ids, passed: ids.length === 0 });
  }
  return results;
}
