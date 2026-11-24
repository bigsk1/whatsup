import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const read = (relative) => readFile(new URL(`../../${relative}`, import.meta.url), 'utf8');

test('the scheduled advisory scan keeps its write token away from dependency code', async () => {
  const workflow = (await read('.github/workflows/advisories.yml')).replaceAll('\r\n', '\n');
  assert.match(workflow, /^on:\n  schedule:\n(?:    #.*\n)*    - cron: '\d+ \d+ \* \* \*'\n/m);
  assert.match(workflow, /^permissions:\n  contents: read\n/m);
  assert.doesNotMatch(workflow, /pull_request_target|continue-on-error|if: always\(\)|secrets\./);

  const jobs = workflow.split(/^  (?:scan|report):$/m);
  assert.equal(jobs.length, 3);
  const [, scan, report] = jobs;
  // The scan installs and builds dependencies, so it may hold no write scope.
  assert.doesNotMatch(scan, /: write|permissions:/);
  assert.match(scan, /persist-credentials: false/);
  assert.match(scan, /run: npm run scan:advisories/);
  assert.match(scan, /ubuntu-24\.04/);
  assert.match(scan, /windows-latest/);
  // The reporter can write issues and nothing else, and runs no project code.
  assert.match(report, /needs: scan/);
  assert.match(report, /if: failure\(\) && github\.event_name == 'schedule'/);
  assert.deepEqual(report.match(/^\s+\w[\w-]*: write$/gm)?.map((line) => line.trim()), ['issues: write']);
  assert.doesNotMatch(report, /uses:|npm |node |checkout/);
});

test('the scheduled scan and the component checks share one list of advisory gates', async () => {
  const manifest = JSON.parse(await read('package.json'));
  assert.equal(manifest.scripts['scan:advisories'], 'node scripts/whatsup.mjs advisory-scan');
  const runner = await read('scripts/whatsup.mjs');
  assert.match(runner, /if \(command === 'advisory-scan'\) await advisoryScan\(\);/);
  // One definition of the gates, called from both places: a gate added to the
  // component checks cannot be missing from the scheduled scan.
  assert.equal(runner.split("['root npm audit'").length - 1, 1);
  assert.equal(runner.split('replacementAdvisories(patchApprovals())').length - 1, 1);
  assert.equal(runner.split('await advisoryScans(scanner, wails)').length - 1, 2);
});

test('the scan generates bindings before it builds, and WSL returns its receipt', async () => {
  const runner = await read('scripts/whatsup.mjs');
  const body = runner.slice(runner.indexOf('async function advisoryScan()'), runner.indexOf('async function frontendScript('));
  // The WSL snapshot excludes frontend/bindings, so a build without this
  // step only works where an earlier command happened to leave them behind.
  const generate = body.indexOf('await generateBindings();');
  const build = body.indexOf("await frontendScript('build');");
  assert.ok(generate > 0 && build > generate, 'bindings must be generated before the frontend build');
  assert.ok((await read('scripts/wsl-excludes.txt')).split(/\r?\n/).includes('/frontend/bindings/'));
  // The receipt named in a failure must reach the Windows workspace.
  assert.ok(body.includes('advisory-scan-${process.platform}-${process.arch}.json'), 'the scan must write its receipt');
  const copied = /^for report in ([^;]*); do$/m.exec(await read('scripts/wsl-run.sh'))?.[1].split(' ');
  assert.ok(copied?.includes('advisory-scan-linux-x64.json'), 'WSL must copy the scan receipt back');
});
