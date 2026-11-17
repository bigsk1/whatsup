import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { classifyComponentPaths, componentScopeForEvent, runComponentScope } from '../lib/ci-scope.mjs';
import { within } from '../lib/workflow.mjs';

const zero = '0'.repeat(40);
const missingCommit = 'f'.repeat(40);
const documentation = [
  'README.md', 'SECURITY.md', 'CHANGELOG.md',
  'docs/privacy.md', 'docs/linux-support.md', 'docs/arch-installation.md', 'docs/versioning.md',
  'docs/images/preview.png', 'docs/images/preview-2.jpg',
  'docs/images/preview_3.jpeg', 'docs/images/preview.4.webp',
];

function expectDecision(value, runComponents) {
  assert.equal(value.runComponents, runComponents);
  assert.equal(typeof value.reason, 'string');
  assert.ok(value.reason.length > 0);
  assert.equal(Number.isSafeInteger(value.changedPathCount), true);
  assert.ok(value.changedPathCount >= 0);
}

async function repository(t) {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'whatsup-ci-scope-'));
  t.after(async () => {
    assert.equal(within(os.tmpdir(), directory), true);
    assert.match(path.basename(directory), /^whatsup-ci-scope-/);
    await rm(directory, { recursive: true, force: true });
  });
  const environment = Object.fromEntries(Object.entries(process.env).filter(([name]) => !name.startsWith('GIT_')));
  Object.assign(environment, {
    GIT_CONFIG_NOSYSTEM: '1',
    GIT_CONFIG_GLOBAL: path.join(directory, '.unused-global-config'),
    GIT_TERMINAL_PROMPT: '0',
  });
  function git(...args) {
    const result = spawnSync('git', [
      '-c', 'init.templateDir=',
      '-c', 'init.defaultBranch=main',
      '-c', `core.hooksPath=${path.join(directory, '.disabled-hooks')}`,
      '-c', 'user.name=Synthetic CI fixture',
      '-c', 'user.email=ci-fixture@example.invalid',
      '-c', 'commit.gpgsign=false',
      ...args,
    ], { cwd: directory, env: environment, shell: false, encoding: 'utf8', windowsHide: true });
    assert.equal(result.status, 0, result.error?.message ?? result.stderr);
    return result.stdout.trim();
  }
  async function write(filename, content) {
    const target = path.join(directory, filename);
    await mkdir(path.dirname(target), { recursive: true });
    await writeFile(target, content);
  }
  function commit(message) {
    git('add', '--all');
    git('commit', '--quiet', '--message', message);
    return git('rev-parse', 'HEAD');
  }
  git('init', '--quiet');
  await write('README.md', '# Synthetic documentation\n');
  await write('docs/privacy.md', '# Synthetic privacy guide\n');
  await write('docs/images/synthetic.png', 'Synthetic screenshot fixture\n');
  await write('frontend/src/example.ts', 'export const synthetic = 1;\n');
  const initial = commit('Synthetic baseline');
  return { directory, git, write, commit, initial };
}

function push(repo, before, after, ref = 'refs/heads/main') {
  return componentScopeForEvent({ root: repo.directory, eventName: 'push', ref, event: { before, after } });
}

test('only the explicit public documents and direct raster screenshot paths skip components', () => {
  for (const filename of documentation) expectDecision(classifyComponentPaths([filename]), false);
  expectDecision(classifyComponentPaths(documentation), false);
});

test('source, packaging, unknown Markdown and unusual image paths still need components', () => {
  for (const filename of [
    'frontend/src/example.ts', 'internal/example.go', 'go.mod', 'package-lock.json',
    '.github/workflows/components.yml', 'scripts/lib/ci-scope.mjs', 'LICENSE',
    'THIRD_PARTY_NOTICES.md', 'docs/new-guide.md', 'readme.md', '.local/docs/README.md',
    'docs/images/helper.go', 'docs/images/package.json', 'docs/images/README.md',
    'docs/images/nested/preview.png', 'docs/images/preview.svg', 'docs/images/preview.gif',
    'docs/images/preview.PNG', 'docs/images/preview@2.png', 'docs/images/.hidden.png',
    'docs/images/../preview.png', 'docs\\images\\preview.png', '../README.md', '/README.md',
    'docs/images/line\nbreak.png',
  ]) expectDecision(classifyComponentPaths([filename]), true);
  expectDecision(classifyComponentPaths(['README.md', 'frontend/src/example.ts']), true);
  expectDecision(classifyComponentPaths([]), true);
});

test('a real documentation-only push skips components', async (t) => {
  const repo = await repository(t);
  await repo.write('README.md', '# Changed synthetic documentation\n');
  const after = repo.commit('Synthetic documentation change');
  expectDecision(push(repo, repo.initial, after), false);
});

test('a multi-commit push includes source changes before its final documentation commit', async (t) => {
  const repo = await repository(t);
  await repo.write('README.md', '# First documentation change\n');
  repo.commit('Synthetic first documentation change');
  await repo.write('frontend/src/example.ts', 'export const synthetic = 2;\n');
  const sourceChange = repo.commit('Synthetic source change');
  await repo.write('docs/privacy.md', '# Last documentation change\n');
  const after = repo.commit('Synthetic last documentation change');
  expectDecision(push(repo, sourceChange, after), false);
  expectDecision(push(repo, repo.initial, after), true);
});

test('PR scope uses the merge base rather than unrelated changes added to its base branch', async (t) => {
  const repo = await repository(t);
  repo.git('checkout', '--quiet', '-b', 'synthetic-topic');
  await repo.write('README.md', '# Synthetic PR documentation\n');
  const head = repo.commit('Synthetic PR documentation change');
  repo.git('checkout', '--quiet', 'main');
  await repo.write('frontend/src/example.ts', 'export const synthetic = 3;\n');
  const base = repo.commit('Synthetic unrelated base source change');
  expectDecision(componentScopeForEvent({
    root: repo.directory,
    eventName: 'pull_request',
    ref: 'refs/pull/1/merge',
    event: { pull_request: { base: { sha: base }, head: { sha: head } } },
  }), false);
});

test('renaming source into documentation and documentation into source requires components', async (t) => {
  const repo = await repository(t);
  repo.git('mv', 'frontend/src/example.ts', 'docs/images/renamed.png');
  const intoDocs = repo.commit('Synthetic source renamed to image path');
  expectDecision(push(repo, repo.initial, intoDocs), true);
  repo.git('mv', 'docs/images/renamed.png', 'frontend/src/renamed.ts');
  const intoSource = repo.commit('Synthetic image path renamed to source');
  expectDecision(push(repo, intoDocs, intoSource), true);
});

test('deleting or renaming an allowed document remains a documentation-only change', async (t) => {
  const repo = await repository(t);
  repo.git('mv', 'docs/images/synthetic.png', 'docs/images/renamed.png');
  const renamed = repo.commit('Synthetic screenshot rename');
  expectDecision(push(repo, repo.initial, renamed), false);
  repo.git('rm', 'README.md');
  const deleted = repo.commit('Synthetic README deletion');
  expectDecision(push(repo, renamed, deleted), false);
});

test('new source or unrecognized Markdown under docs cannot bypass component checks', async (t) => {
  const repo = await repository(t);
  await repo.write('docs/images/helper.go', 'package synthetic\n');
  const source = repo.commit('Synthetic source beneath docs images');
  expectDecision(push(repo, repo.initial, source), true);
  await repo.write('docs/new-guide.md', '# Unrecognized synthetic document\n');
  const unknownDocs = repo.commit('Synthetic unknown documentation');
  expectDecision(push(repo, source, unknownDocs), true);
});

test('manual runs and tag pushes always require components even for a documentation diff', async (t) => {
  const repo = await repository(t);
  await repo.write('README.md', '# Synthetic tagged documentation\n');
  const after = repo.commit('Synthetic documentation before tag');
  expectDecision(push(repo, repo.initial, after, 'refs/tags/v0.1.0'), true);
  expectDecision(componentScopeForEvent({
    root: repo.directory,
    eventName: 'workflow_dispatch',
    ref: 'refs/heads/main',
    event: { before: repo.initial, after },
  }), true);
});

test('new branches, deleted refs, missing commits and empty diffs never skip components', async (t) => {
  const repo = await repository(t);
  expectDecision(push(repo, zero, repo.initial), true);
  expectDecision(push(repo, repo.initial, zero), true);
  expectDecision(push(repo, missingCommit, repo.initial), true);
  expectDecision(push(repo, repo.initial, missingCommit), true);
  expectDecision(push(repo, repo.initial, repo.initial), true);
});

test('incomplete or unsupported event metadata cannot be classified as documentation only', async (t) => {
  const repo = await repository(t);
  for (const input of [
    { eventName: 'push', ref: 'refs/heads/main', event: {} },
    { eventName: 'push', ref: 'refs/heads/main', event: { before: '--invalid', after: repo.initial } },
    { eventName: 'pull_request', ref: 'refs/pull/1/merge', event: {} },
    { eventName: 'pull_request', ref: 'refs/pull/1/merge', event: { pull_request: { base: { sha: repo.initial } } } },
    { eventName: 'schedule', ref: 'refs/heads/main', event: {} },
  ]) expectDecision(componentScopeForEvent({ root: repo.directory, ...input }), true);
});

test('the CI wrapper reads real event files and writes conservative GitHub step outputs', async (t) => {
  const repo = await repository(t);
  await repo.write('README.md', '# Synthetic documentation for the CI wrapper\n');
  const after = repo.commit('Synthetic wrapper documentation change');
  const eventPath = path.join(repo.directory, 'synthetic-event.json');
  await writeFile(eventPath, JSON.stringify({ before: repo.initial, after }));
  const environment = {
    GITHUB_ACTIONS: 'true', GITHUB_EVENT_NAME: 'push', GITHUB_REF: 'refs/heads/main',
    GITHUB_EVENT_PATH: eventPath,
  };
  const docsOutput = path.join(repo.directory, 'docs-output');
  expectDecision(await runComponentScope(repo.directory, { ...environment, GITHUB_OUTPUT: docsOutput }), false);
  assert.equal(await readFile(docsOutput, 'utf8'), 'run_components=false\n');

  await writeFile(eventPath, '{ malformed event');
  for (const [name, overrides] of [
    ['malformed', {}],
    ['unreadable', { GITHUB_EVENT_PATH: path.join(repo.directory, 'missing-event.json') }],
    ['local', { GITHUB_ACTIONS: 'false' }],
  ]) {
    const output = path.join(repo.directory, name + '-output');
    expectDecision(await runComponentScope(repo.directory, { ...environment, ...overrides, GITHUB_OUTPUT: output }), true);
    assert.equal(await readFile(output, 'utf8'), 'run_components=true\n');
  }
});
