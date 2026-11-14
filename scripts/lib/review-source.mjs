import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { lstat, readFile } from 'node:fs/promises';
import path from 'node:path';
import { within } from './workflow.mjs';

function git(root, args) {
  const result = spawnSync('git', args, { cwd: root, shell: false, encoding: 'utf8', windowsHide: true, maxBuffer: 8 * 1024 * 1024 });
  if (result.error || result.status !== 0) return null;
  return result.stdout;
}

function checkedNames(files) {
  if (!Array.isArray(files) || !files.length || files.length > 20000) throw new Error('Review source inventory is missing or oversized');
  const selected = [];
  for (const name of files) {
    if (typeof name !== 'string' || name.length > 1024 || /[\\:\x00-\x1f]/.test(name)
        || name.startsWith('/') || name.split('/').some((part) => !part || part === '.' || part === '..')
        || /^(?:\.git|\.local|node_modules|artifacts|frontend\/(?:node_modules|dist|bindings)|build\/(?:bin|tools))(?:\/|$)/.test(name)) {
      throw new Error('Review source inventory contains an unsafe or generated path');
    }
    selected.push(name);
  }
  if (new Set(selected).size !== selected.length) throw new Error('Review source inventory contains duplicate paths');
  return selected.sort();
}

export async function digestReviewFiles(root, files) {
  const names = checkedNames(files);
  const hash = createHash('sha256');
  let total = 0;
  for (const name of names) {
    const filename = path.join(root, ...name.split('/'));
    if (!within(root, filename)) throw new Error('Review source escaped the workspace');
    for (let ancestor = path.dirname(filename); within(root, ancestor); ancestor = path.dirname(ancestor)) {
      const info = await lstat(ancestor);
      if (!info.isDirectory() || info.isSymbolicLink()) throw new Error('Review source cannot traverse symlinks');
    }
    const info = await lstat(filename);
    if (!info.isFile() || info.isSymbolicLink() || info.size > 8 * 1024 * 1024) throw new Error('Review source must be bounded regular files');
    total += info.size;
    if (total > 64 * 1024 * 1024) throw new Error('Review source exceeds its bounded inventory');
    const bytes = await readFile(filename);
    hash.update(name + '\0' + bytes.length + '\0');
    hash.update(bytes);
  }
  return hash.digest('hex');
}

export async function verifyDerivedReviewSource(root, metadata) {
  if (metadata?.schemaVersion !== 1 || !/^(?:[a-f0-9]{40}|[a-f0-9]{64})$/.test(metadata.sourceCommit)
      || typeof metadata.sourceDirty !== 'boolean' || !/^[a-f0-9]{64}$/.test(metadata.sourceDigest)) {
    throw new Error('Derived review source metadata is invalid');
  }
  const files = checkedNames(metadata.files);
  const sourceDigest = await digestReviewFiles(root, files);
  if (sourceDigest !== metadata.sourceDigest) throw new Error('Derived review source differs from the authoritative workspace');
  return { schemaVersion: 1, sourceCommit: metadata.sourceCommit, sourceDirty: metadata.sourceDirty, sourceDigest, files };
}

export async function captureReviewSource(root) {
  const top = git(root, ['rev-parse', '--show-toplevel'])?.trim();
  const normalize = (value) => process.platform === 'win32' ? path.resolve(value).toLowerCase() : path.resolve(value);
  if (top && normalize(top) === normalize(root)) {
    const sourceCommit = git(root, ['rev-parse', '--verify', 'HEAD'])?.trim();
    const status = git(root, ['status', '--porcelain=v1', '--untracked-files=all']);
    const output = git(root, ['ls-files', '--cached', '--others', '--exclude-standard', '-z']);
    if (!sourceCommit || !/^(?:[a-f0-9]{40}|[a-f0-9]{64})$/.test(sourceCommit) || status === null || output === null) {
      throw new Error('A local source commit is required for a versioned development build');
    }
    // Generated bindings are build outputs, recreated from the included Go source.
    const files = [...new Set(output.split('\0').filter((name) => name && !name.startsWith('frontend/bindings/')))];
    const sourceDigest = await digestReviewFiles(root, files);
    return { schemaVersion: 1, sourceCommit, sourceDirty: status.trim().length > 0, sourceDigest, files: files.sort() };
  }
  let metadata;
  try { metadata = JSON.parse(await readFile(path.join(root, '.local/review-source.json'), 'utf8')); }
  catch { throw new Error('A Git checkout or verified WSL source metadata is required for a development review build'); }
  return verifyDerivedReviewSource(root, metadata);
}
