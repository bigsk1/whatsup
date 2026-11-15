// The only accepted exception is the reviewed, expiring development-only chain.
// Raw audit results remain in the scan receipt; unknown advisories always fail.
export function evaluateNpmAudit(report, { exception, lock, now = new Date(), frontend = false }) {
  if (report?.auditReportVersion !== 2 || !report.vulnerabilities || report.error || !report.metadata?.vulnerabilities) throw new Error('Invalid or failed npm audit response');
  const vulnerabilities = Object.entries(report.vulnerabilities);
  if (!vulnerabilities.length) {
    if (report.metadata.vulnerabilities.total !== 0) throw new Error('Inconsistent npm audit totals');
    return { passed: true, exceptions: [] };
  }
  const fail = () => { throw new Error('npm audit found an unapproved or expired advisory; inspect the raw local audit report'); };
  const expiration = new Date(exception?.expiresOn + 'T00:00:00Z');
  const reviewed = new Date(exception?.reviewedOn + 'T00:00:00Z');
  if (!frontend || exception?.schemaVersion !== 1 || exception.advisory !== 'https://github.com/advisories/GHSA-gcqx-w48m-w2h4'
      || exception.package !== 'deepmerge' || exception.version !== '4.3.1' || exception.parent !== '@sveltejs/vite-plugin-svelte'
      || exception.parentVersion !== '7.3.1' || !/^\d{4}-\d{2}-\d{2}$/.test(exception.expiresOn)
      || !Number.isFinite(expiration.getTime()) || !Number.isFinite(reviewed.getTime())
      || expiration.toISOString().slice(0, 10) !== exception.expiresOn || reviewed.toISOString().slice(0, 10) !== exception.reviewedOn
      || expiration - reviewed > 31 * 86400000 || expiration <= reviewed
      || !Number.isFinite(now.getTime()) || now >= expiration || now < reviewed
      || vulnerabilities.length !== 2 || report.metadata.vulnerabilities.total !== 2) fail();
  for (const name of ['deepmerge', '@sveltejs/vite-plugin-svelte']) {
    const item = report.vulnerabilities[name];
    const installed = lock?.packages?.[`node_modules/${name}`];
    if (!item || item.name !== name || item.severity !== 'low' || item.fixAvailable !== false || installed?.dev !== true
        || installed.version !== (name === 'deepmerge' ? exception.version : exception.parentVersion)
        || JSON.stringify(item.nodes) !== JSON.stringify([`node_modules/${name}`])) fail();
    if (name === 'deepmerge') {
      if (!Array.isArray(item.via) || item.via.length !== 1 || item.via[0]?.url !== exception.advisory
          || item.via[0]?.severity !== 'low' || item.via[0]?.name !== 'deepmerge'
          || JSON.stringify(item.effects) !== JSON.stringify([exception.parent])) fail();
    } else if (JSON.stringify(item.via) !== JSON.stringify(['deepmerge']) || item.effects?.length !== 0) fail();
  }
  return { passed: true, exceptions: [{ advisory: exception.advisory, package: exception.package, version: exception.version,
    scope: exception.scope, expiresOn: exception.expiresOn }] };
}
