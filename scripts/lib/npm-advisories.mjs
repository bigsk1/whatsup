import { createHash } from 'node:crypto';

const approvedAdvisory = 'https://github.com/advisories/GHSA-fc5q-39xc-vjpf';
const approvedPackage = 'picomatch';
const approvedVersion = '4.0.7';
const approvedScope = 'frontend build and test tooling only';
const severities = ['info', 'low', 'moderate', 'high', 'critical'];
const reviewedPackages = [
  '@sveltejs/vite-plugin-svelte', 'fdir', 'picomatch', 'svelte-check', 'tinyglobby', 'vite', 'vitest',
];
const isRecord = (value) => value != null && typeof value === 'object' && !Array.isArray(value);
const sameNames = (actual, expected) => Array.isArray(actual) && Array.isArray(expected)
  && actual.every((value) => typeof value === 'string') && expected.every((value) => typeof value === 'string')
  && JSON.stringify([...actual].sort()) === JSON.stringify([...expected].sort());

function utcDate(value) {
  if (typeof value !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(value)) return NaN;
  const parsed = new Date(value + 'T00:00:00Z');
  return Number.isFinite(parsed.getTime()) && parsed.toISOString().slice(0, 10) === value ? parsed.getTime() : NaN;
}

function lockPackagesDigest(lock) {
  if (lock?.lockfileVersion !== 3 || !isRecord(lock.packages) || !isRecord(lock.packages[''])) return '';
  // Pin the entire install graph, including edges, integrity and dev/peer
  // flags. The application version is unrelated to this dependency graph.
  const { version: _appVersion, ...rootPackage } = lock.packages[''];
  return createHash('sha256').update(JSON.stringify({ ...lock.packages, '': rootPackage })).digest('hex');
}

// Raw findings remain in the scan receipt. Only this exact frontend audit
// graph is accepted temporarily; every unknown finding still fails the gate.
export function evaluateNpmAudit(report, { exception, lock, now = new Date(), frontend = false }) {
  if (report?.auditReportVersion !== 2 || !isRecord(report.vulnerabilities) || report.error
      || !isRecord(report.metadata?.vulnerabilities)) throw new Error('Invalid or failed npm audit response');
  const found = Object.keys(report.vulnerabilities);
  const counts = report.metadata.vulnerabilities;
  const matchCounts = (high) => counts.total === high && severities.every((severity) => counts[severity] === (severity === 'high' ? high : 0));
  if (!found.length) {
    if (!matchCounts(0)) throw new Error('Inconsistent npm audit totals');
    return { passed: true, exceptions: [] };
  }

  const fail = () => { throw new Error('npm audit found an unapproved or expired advisory; inspect the raw local audit report'); };
  const expiration = utcDate(exception?.expiresOn);
  const reviewed = utcDate(exception?.reviewedOn);
  const time = now instanceof Date ? now.getTime() : NaN;
  if (!frontend || exception?.schemaVersion !== 1 || exception.advisory !== approvedAdvisory
      || exception.package !== approvedPackage || exception.version !== approvedVersion || exception.severity !== 'high'
      || exception.scope !== approvedScope || !isRecord(exception.findings) || !isRecord(exception.detail)
      || !sameNames(found, reviewedPackages) || !sameNames(Object.keys(exception.findings), reviewedPackages)
      || !matchCounts(reviewedPackages.length) || !/^[a-f0-9]{64}$/.test(exception.lockPackagesSHA256)
      || exception.lockPackagesSHA256 !== lockPackagesDigest(lock) || !Number.isFinite(expiration) || !Number.isFinite(reviewed)
      || expiration - reviewed > 31 * 86400000 || expiration <= reviewed || !Number.isFinite(time) || time >= expiration || time < reviewed) fail();

  for (const name of reviewedPackages) {
    const item = report.vulnerabilities[name];
    const expected = exception.findings[name];
    const node = `node_modules/${name}`;
    const fix = item?.fixAvailable;
    if (!isRecord(item) || !isRecord(expected) || item.name !== name || item.severity !== exception.severity
        || typeof expected.direct !== 'boolean' || item.isDirect !== expected.direct || item.range !== expected.range
        || !sameNames(item.nodes, [node]) || !sameNames(item.effects, expected.effects)
        || lock.packages[node]?.version !== expected.version || !isRecord(fix) || fix.isSemVerMajor !== true
        // npm can pick different major-downgrade suggestions for the same
        // locked graph. Allow only the explicitly reviewed alternatives.
        || !Array.isArray(expected.downgrades) || !expected.downgrades.some((candidate) =>
          Array.isArray(candidate) && candidate.length === 2 && fix.name === candidate[0] && fix.version === candidate[1])) fail();
    if (name !== approvedPackage) {
      if (!sameNames(item.via, expected.via)) fail();
      continue;
    }
    const detail = item.via?.[0];
    if (expected.version !== approvedVersion || !Array.isArray(item.via) || item.via.length !== 1 || !isRecord(detail)
        || detail.url !== approvedAdvisory || detail.name !== approvedPackage || detail.dependency !== approvedPackage
        || detail.severity !== exception.severity || detail.range !== exception.detail.range
        || !sameNames(detail.cwe, exception.detail.cwe) || detail.cvss?.score !== exception.detail.score
        || detail.cvss?.vectorString !== exception.detail.vector) fail();
  }

  return { passed: true, exceptions: [{ advisory: exception.advisory, package: exception.package, version: exception.version,
    scope: exception.scope, expiresOn: exception.expiresOn }] };
}
