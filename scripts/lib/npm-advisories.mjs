const severities = ['info', 'low', 'moderate', 'high', 'critical'];
const isRecord = (value) => value != null && typeof value === 'object' && !Array.isArray(value);

// No advisory is excepted: any finding fails the gate, whatever its severity
// or the package's role. The raw report stays in the local scan receipt. A
// future exception needs its own review and must not be added here quietly.
export function evaluateNpmAudit(report) {
  if (report?.auditReportVersion !== 2 || !isRecord(report.vulnerabilities) || report.error
      || !isRecord(report.metadata?.vulnerabilities)) throw new Error('Invalid or failed npm audit response');
  const counts = report.metadata.vulnerabilities;
  const clean = counts.total === 0 && severities.every((severity) => counts[severity] === 0);
  if (Object.keys(report.vulnerabilities).length) {
    throw new Error('npm audit found an advisory and no exception is approved; inspect the raw local audit report');
  }
  if (!clean) throw new Error('Inconsistent npm audit totals');
  return { passed: true, exceptions: [] };
}
