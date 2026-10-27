import { readFileSync, realpathSync } from 'node:fs';
import { pathToFileURL } from 'node:url';

const severities = ['info', 'low', 'moderate', 'high', 'critical'];

export function checkAudit(report, status) {
  if (![0, 1].includes(status)) throw new Error(`npm audit failed with exit ${status}`);
  if (!report || report.error || report.auditReportVersion !== 2 ||
      !report.vulnerabilities || typeof report.vulnerabilities !== 'object' || Array.isArray(report.vulnerabilities)) {
    throw new Error('Incomplete or unsuccessful npm audit response');
  }
  const counts = report.metadata?.vulnerabilities;
  if (!counts || ![...severities, 'total'].every(key => Number.isSafeInteger(counts[key]) && counts[key] >= 0)) {
    throw new Error('Missing or invalid npm audit severity counts');
  }
  const actual = Object.fromEntries(severities.map(key => [key, 0]));
  for (const [name, vulnerability] of Object.entries(report.vulnerabilities)) {
    if (!vulnerability || vulnerability.name !== name || !severities.includes(vulnerability.severity) ||
        !Array.isArray(vulnerability.via) || !Array.isArray(vulnerability.nodes)) {
      throw new Error('Invalid npm audit vulnerability record');
    }
    actual[vulnerability.severity] += 1;
  }
  if (!severities.every(key => counts[key] === actual[key]) ||
      counts.total !== severities.reduce((total, key) => total + actual[key], 0)) {
    throw new Error('Inconsistent npm audit severity counts');
  }
  if (counts.high || counts.critical) throw new Error(`Dependency audit blocked: ${counts.high} high, ${counts.critical} critical`);
  if (status !== 0) throw new Error('npm audit failed without a matching vulnerability result');
  return counts;
}

if (process.argv[1] && import.meta.url === pathToFileURL(realpathSync(process.argv[1])).href) {
  try {
    const counts = checkAudit(JSON.parse(readFileSync(process.argv[2], 'utf8')), Number(process.argv[3]));
    console.log(`Dependency audit passed: ${counts.total} lower-severity findings, no high/critical findings.`);
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
