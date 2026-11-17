import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { lstat, mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { within } from '../lib/workflow.mjs';

const root = fileURLToPath(new URL('../../', import.meta.url));
const publicDocuments = [
  'README.md', 'SECURITY.md', 'THIRD_PARTY_NOTICES.md', 'CHANGELOG.md',
  'docs/privacy.md', 'docs/linux-support.md', 'docs/arch-installation.md', 'docs/versioning.md',
];
const privateDocuments = [
  'AGENTS.md', 'design-qa.md', 'session-notes.md',
  'docs/architecture.md', 'docs/development.md', 'docs/status.md',
  'docs/native-testing.md', 'docs/pairing-protocol.md', 'docs/storage.md',
  'docs/transport-review.md', 'docs/first-commit.md',
  'docs/references/research.md', 'docs/future-planning.md',
  '.local/docs/private.md', 'notes/session.md', 'dev-notes/review.md',
];

test('adding all files keeps private documentation ignored and retains the public allowlist', async (t) => {
  const version = spawnSync('git', ['--version'], { encoding: 'utf8', windowsHide: true });
  if (version.error?.code === 'ENOENT') return t.skip('Git is required to verify ignore behavior');
  assert.equal(version.status, 0, version.stderr || version.error?.message);
  const directory = await mkdtemp(path.join(os.tmpdir(), 'whatsup-public-docs-'));
  t.after(async () => {
    assert.equal(within(os.tmpdir(), directory), true);
    assert.match(path.basename(directory), /^whatsup-public-docs-/);
    await rm(directory, { recursive: true, force: true });
  });
  await writeFile(path.join(directory, '.gitignore'), await readFile(path.join(root, '.gitignore')));
  for (const filename of [...publicDocuments, ...privateDocuments]) {
    await mkdir(path.dirname(path.join(directory, filename)), { recursive: true });
    await writeFile(path.join(directory, filename), '# Synthetic documentation\n');
  }
  const environment = Object.fromEntries(Object.entries(process.env).filter(([name]) => !name.startsWith('GIT_')));
  environment.GIT_CONFIG_NOSYSTEM = '1';
  environment.GIT_CONFIG_GLOBAL = path.join(directory, '.unused-global-config');
  environment.GIT_TERMINAL_PROMPT = '0';
  function git(args, input) {
    const result = spawnSync('git', ['-c', 'init.templateDir=', '-c', `core.hooksPath=${path.join(directory, '.disabled-hooks')}`, ...args], {
      cwd: directory, env: environment, encoding: 'utf8', windowsHide: true, input,
    });
    assert.equal(result.status, 0, result.stderr || result.error?.message);
    return result.stdout;
  }
  git(['init', '--quiet']);
  git(['add', '--all']);
  const trackedMarkdown = git(['ls-files', '-z']).split('\0').filter((name) => name.endsWith('.md')).sort();
  assert.deepEqual(trackedMarkdown, [...publicDocuments].sort());
  const ignored = git(['check-ignore', '--no-index', '--stdin', '-z'], `${privateDocuments.join('\0')}\0`)
    .split('\0').filter(Boolean).sort();
  assert.deepEqual(ignored, [...privateDocuments].sort());

  const wslExcludes = (await readFile(path.join(root, 'scripts/wsl-excludes.txt'), 'utf8')).split(/\r?\n/);
  for (const excluded of ['.local/', '/AGENTS.md', '/design-qa.md']) {
    assert.equal(wslExcludes.includes(excluded), true, `WSL snapshot must exclude ${excluded}`);
  }
});

function markdownTargets(markdown) {
  const prose = markdown.replace(/^\s*```[^\n]*\n[\s\S]*?^\s*```\s*$/gm, '');
  return [
    ...prose.matchAll(/!?\[[^\]\n]*\]\(\s*(?:<([^>\n]+)>|([^\s)]+))(?:\s+["'][^\n]*["'])?\s*\)/g),
    ...prose.matchAll(/^\s{0,3}\[[^\]\n]+\]:\s*(?:<([^>\n]+)>|(\S+))/gm),
  ].map((match) => match[1] ?? match[2]);
}

test('public documentation links stay usable without local notes and omit session anecdotes', async () => {
  const anecdote = /\b(?:the\s+)?owner\s+(?:said|asked|requested|reported|confirmed|verified|tested|passed|approved|accepted|recorded|prefers?|treats?|permits?|allows?)\b/i;
  for (const document of publicDocuments) {
    let markdown;
    try { markdown = await readFile(path.join(root, document), 'utf8'); }
    catch (error) {
      if (document === 'CHANGELOG.md' && error.code === 'ENOENT') continue;
      throw error;
    }
    assert.doesNotMatch(markdown, anecdote, `${document} must describe product behavior rather than session decisions`);
    for (const target of markdownTargets(markdown)) {
      if (/^(?:https?:|mailto:|#)/i.test(target)) continue;
      assert.doesNotMatch(target, /^(?:[a-z][a-z\d+.-]*:|\/\/)/i, `${document}: unsupported public link ${target}`);
      const pathname = decodeURIComponent(target.split(/[?#]/, 1)[0]);
      const resolved = path.resolve(path.dirname(path.join(root, document)), pathname);
      assert.equal(within(root, resolved), true, `${document}: link escapes the source tree: ${target}`);
      const relative = path.relative(root, resolved).split(path.sep).join('/');
      assert.doesNotMatch(relative, /^(?:\.local|\.git|artifacts|node_modules)(?:\/|$)|^build\/bin(?:\/|$)/, `${document}: link requires private or generated content: ${target}`);
      if (relative.toLowerCase().endsWith('.md')) {
        assert.equal(publicDocuments.includes(relative), true, `${document}: link requires private documentation: ${target}`);
      }
      await assert.doesNotReject(lstat(resolved), `${document}: missing link target ${target}`);
    }
  }
});
