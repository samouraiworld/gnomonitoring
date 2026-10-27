import assert from 'node:assert/strict';
import { chmodSync, copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import test from 'node:test';
import { checkAudit } from './check-npm-audit.mjs';

const repository = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const clean = () => ({
  auditReportVersion: 2,
  vulnerabilities: {},
  metadata: { vulnerabilities: { info: 0, low: 0, moderate: 0, high: 0, critical: 0, total: 0 } },
});
function finding(severity) {
  const report = clean();
  report.vulnerabilities.example = { name: 'example', severity, via: ['advisory'], nodes: ['node_modules/example'] };
  report.metadata.vulnerabilities[severity] = 1;
  report.metadata.vulnerabilities.total = 1;
  return report;
}

test('audit permits complete clean and lower-severity results', () => {
  assert.equal(checkAudit(clean(), 0).total, 0);
  for (const severity of ['info', 'low', 'moderate']) assert.equal(checkAudit(finding(severity), 0)[severity], 1);
});

test('audit blocks high/critical findings even if npm returns zero', () => {
  for (const severity of ['high', 'critical']) {
    for (const status of [0, 1]) assert.throws(() => checkAudit(finding(severity), status), /blocked/);
  }
});

test('audit fails closed on API errors, missing data, inconsistent counts and unexpected exits', () => {
  for (const report of [null, {}, { error: { code: 'EAUDITNOADVISORY' } }, { ...clean(), error: {} }]) {
    assert.throws(() => checkAudit(report, 0));
  }
  const missing = clean(); delete missing.metadata;
  const partial = clean(); delete partial.metadata.vulnerabilities.high;
  const inconsistent = finding('critical'); inconsistent.metadata.vulnerabilities.critical = 0;
  const invalid = clean(); invalid.metadata.vulnerabilities.total = -1;
  for (const report of [missing, partial, inconsistent, invalid]) assert.throws(() => checkAudit(report, 0));
  for (const status of [1, 2, 126, NaN]) assert.throws(() => checkAudit(clean(), status));
});

test('audit CLI rejects malformed JSON instead of treating it as zero findings', () => {
  const directory = mkdtempSync(join(tmpdir(), 'monitoring-audit-policy-'));
  try {
    const path = join(directory, 'report.json');
    writeFileSync(path, '<html>Registry unavailable</html>');
    const result = spawnSync(process.execPath, [join(repository, '.github/scripts/check-npm-audit.mjs'), path, '0']);
    assert.equal(result.status, 1);
    const alias = join(directory, 'validator.mjs');
    symlinkSync(join(repository, '.github/scripts/check-npm-audit.mjs'), alias);
    const viaAlias = spawnSync(process.execPath, [alias, path, '0']);
    assert.equal(viaAlias.status, 1, 'A symlink invocation must still run the validator');
  } finally { rmSync(directory, { recursive: true, force: true }); }
});

test('panel gate includes every dependency class despite project and environment omit settings', () => {
  const directory = mkdtempSync(join(tmpdir(), 'monitoring-npm-omit-policy-'));
  try {
    const scripts = join(directory, '.github/scripts');
    const panel = join(directory, 'panel');
    const binaries = join(directory, 'bin');
    for (const path of [scripts, panel, binaries]) mkdirSync(path, { recursive: true });
    for (const name of ['security-panel.sh', 'check-npm-audit.mjs']) copyFileSync(join(repository, '.github/scripts', name), join(scripts, name));
    writeFileSync(join(panel, '.npmrc'), 'omit[]=dev\nomit[]=optional\nomit[]=peer\n');
    const argvFile = join(directory, 'npm-argv.json');
    const npm = join(binaries, 'npm');
    writeFileSync(npm, `#!/usr/bin/env node\nconst fs = require('node:fs');\nconst args = process.argv.slice(2);\nfs.writeFileSync(process.env.NPM_ARGV_FILE, JSON.stringify(args));\nconst includesAll = ['--include=dev', '--include=optional', '--include=peer'].every(flag => args.includes(flag));\nconsole.log(JSON.stringify(includesAll ? ${JSON.stringify(finding('high'))} : ${JSON.stringify(clean())}));\nprocess.exitCode = includesAll ? 1 : 0;\n`);
    chmodSync(npm, 0o755);
    const env = { ...process.env, PATH: `${binaries}:${process.env.PATH}`, NODE_ENV: 'production', NPM_CONFIG_OMIT: 'dev', NPM_ARGV_FILE: argvFile };
    const hidden = spawnSync('npm', ['audit', '--json', '--audit-level=high'], { cwd: panel, env, encoding: 'utf8' });
    assert.equal(hidden.status, 0, 'Control reproduces the omission bypass');
    const result = spawnSync('bash', [join(scripts, 'security-panel.sh')], { env, encoding: 'utf8' });
    assert.equal(result.status, 1);
    assert.match(result.stderr, /Dependency audit blocked: 1 high/);
    const args = JSON.parse(readFileSync(argvFile, 'utf8'));
    for (const flag of ['--include=dev', '--include=optional', '--include=peer']) assert(args.includes(flag));
    assert.match(workflow, /npm ci --ignore-scripts --include=dev --include=optional --include=peer/);
  } finally { rmSync(directory, { recursive: true, force: true }); }
});

test('Go gate propagates called findings and scanner failures; inventory findings remain visible', () => {
  const directory = mkdtempSync(join(tmpdir(), 'monitoring-go-policy-'));
  const log = join(directory, 'calls');
  try {
    const scanner = join(directory, 'govulncheck');
    writeFileSync(scanner, '#!/usr/bin/env bash\nprintf "%s\\n" "$*" >> "$SCANNER_LOG"\nif [[ "$1 $2" == "-scan module" ]]; then\n  echo "module inventory: fixture advisory"\n  exit "$INVENTORY_STATUS"\nfi\necho "source analysis: fixture result"\nexit "$SOURCE_STATUS"\n');
    chmodSync(scanner, 0o755);
    const run = (inventory, source) => spawnSync('bash', [join(repository, '.github/scripts/security-go.sh'), 'backend'], {
      env: { ...process.env, PATH: `${directory}:${process.env.PATH}`, SCANNER_LOG: log, INVENTORY_STATUS: String(inventory), SOURCE_STATUS: String(source) },
      encoding: 'utf8',
    });
    assert.equal(run(0, 0).status, 0);
    assert.equal(run(3, 0).status, 0);
    assert.equal(run(0, 3).status, 3);
    assert.equal(run(3, 3).status, 3);
    assert.equal(run(1, 0).status, 1);
    assert.equal(run(126, 0).status, 126);
    assert.equal(run(0, 1).status, 1);
    const calls = readFileSync(log, 'utf8').trim().split('\n');
    assert(calls.every(call => ['-scan module', '-scan package -test ./...', '-test -show traces ./...'].includes(call)));
    assert(!calls.some(call => /json|sarif|openvex/.test(call)));
  } finally { rmSync(directory, { recursive: true, force: true }); }
});

const workflow = readFileSync(join(repository, '.github/workflows/security.yml'), 'utf8');
const publication = readFileSync(join(repository, '.github/workflows/Build_backend_image.yml'), 'utf8');

test('aggregate gate blocks failure, cancellation, skipped and missing prerequisites', () => {
  const code = workflow.match(/node -e '([^\n]+)'/)[1];
  const run = results => spawnSync(process.execPath, ['-e', code], { env: { ...process.env, SECURITY_RESULTS: JSON.stringify(results) } }).status;
  const success = { policy: { result: 'success' }, go: { result: 'success' }, panel: { result: 'success' }, images: { result: 'success' } };
  assert.equal(run(success), 0);
  for (const result of ['failure', 'cancelled', 'skipped', 'timed_out']) assert.equal(run({ ...success, go: { result } }), 1);
  const missing = { ...success }; delete missing.images;
  assert.equal(run(missing), 1);
});

test('security workflow scans PRs without secrets, exceptions or path skips, with immutable actions', () => {
  assert.match(workflow, /  pull_request:\n/);
  assert.match(workflow, /  merge_group:\n/);
  assert.match(workflow, /  workflow_call:\n/);
  assert.match(workflow, /name: Security gate/);
  assert.match(workflow, /if: \$\{\{ always\(\) \}\}/);
  assert.doesNotMatch(workflow, /secrets\.|continue-on-error|paths-ignore|ignore-policy|pull_request_target/);
  for (const source of [workflow, publication]) {
    for (const match of source.matchAll(/uses: ([^\n ]+)/g)) {
      assert(match[1].startsWith('./') || /@[0-9a-f]{40}$/.test(match[1]), `Mutable action: ${match[1]}`);
    }
    assert.match(source, /severity: HIGH,CRITICAL/);
    assert.match(source, /ignore-unfixed: 'false'/);
    assert.match(source, /exit-code: '1'/);
    assert.match(source, /TRIVY_IGNOREFILE: \/dev\/null/);
    assert.match(source, /trivy-config: \/dev\/null/);
    assert.doesNotMatch(source, /exit-code: ['"]?0|continue-on-error/);
  }
});

test('publication scans the built image before login and pushes only that image ID', () => {
  assert.match(publication, /needs: security/);
  assert.match(publication, /push: false/);
  assert.doesNotMatch(publication, /push: true/);
  assert(publication.indexOf('name: Block vulnerable image') < publication.indexOf('name: Log in'));
  assert(publication.indexOf('name: Log in') < publication.indexOf('name: Publish only'));
  assert.match(publication, /image-ref: \$\{\{ steps\.build\.outputs\.imageid \}\}/);
  assert.match(publication, /SCANNED_IMAGE_ID: \$\{\{ steps\.build\.outputs\.imageid \}\}/);
  assert.match(publication, /docker image tag "\$SCANNED_IMAGE_ID" "\$tag"/);
});
