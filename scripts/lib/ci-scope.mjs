import { spawnSync } from 'node:child_process';
import { appendFile, readFile } from 'node:fs/promises';

const documentation = new Set([
  'README.md', 'SECURITY.md', 'CHANGELOG.md',
  'docs/privacy.md', 'docs/linux-support.md', 'docs/arch-installation.md', 'docs/versioning.md',
]);
const screenshot = /^docs\/images\/[A-Za-z0-9][A-Za-z0-9._-]*\.(?:png|jpe?g|webp)$/;
const commitSHA = /^(?!0{40}$)[a-f0-9]{40}$/i;

function validCommit(value) {
  return typeof value === 'string' && commitSHA.test(value);
}

function full(reason, changedPathCount = 0) {
  return { runComponents: true, reason, changedPathCount };
}

export function classifyComponentPaths(paths) {
  if (!Array.isArray(paths) || paths.length === 0) return full('no-changed-paths');
  if (!paths.every((name) => typeof name === 'string'
    && (documentation.has(name) || screenshot.test(name)))) {
    return full('non-documentation-change', paths.length);
  }
  return { runComponents: false, reason: 'documentation-only', changedPathCount: paths.length };
}

export function componentScopeForEvent({ root, eventName, ref, event }) {
  // Manual runs and tags always retain all checks. Unknown contexts do too.
  if (typeof ref !== 'string' || ref.startsWith('refs/tags/')) return full('manual-or-unknown-context');
  let revisions;
  if (eventName === 'push') {
    if (!ref.startsWith('refs/heads/') || event?.created || event?.deleted
      || !validCommit(event?.before) || !validCommit(event?.after)) {
      return full('incomplete-push-context');
    }
    // Cover the entire push, including code changes before its final commit.
    revisions = [event.before, event.after];
  } else if (eventName === 'pull_request') {
    const base = event?.pull_request?.base?.sha;
    const head = event?.pull_request?.head?.sha;
    if (!validCommit(base) || !validCommit(head)) return full('incomplete-pull-request-context');
    // Exclude changes made only on the target branch since the PR diverged.
    revisions = [`${base}...${head}`];
  } else {
    return full('manual-or-unknown-context');
  }

  try {
    const result = spawnSync('git', ['diff', '--no-ext-diff', '--no-textconv', '--no-renames', '--name-only', '-z', ...revisions, '--'], {
      cwd: root, maxBuffer: 2 * 1024 * 1024,
      timeout: 15000, windowsHide: true,
    });
    if (result.error || result.status !== 0 || !Buffer.isBuffer(result.stdout)) return full('comparison-unavailable');
    if (result.stdout.length === 0) return full('no-changed-paths');
    if (result.stdout.at(-1) !== 0) return full('comparison-unavailable');
    // NUL separation handles unusual filenames without Git's quoting rules.
    // Invalid UTF-8 or missing history falls back to all checks, never a skip.
    const names = new TextDecoder('utf-8', { fatal: true }).decode(result.stdout).split('\0');
    names.pop();
    return classifyComponentPaths(names);
  } catch {
    return full('comparison-unavailable');
  }
}

export async function runComponentScope(root, environment = process.env) {
  let result = full('missing-event-context');
  if (environment.GITHUB_ACTIONS === 'true') {
    let event;
    try {
      event = JSON.parse(await readFile(environment.GITHUB_EVENT_PATH, 'utf8'));
    } catch {
      // Keep all checks when an event payload cannot be read or parsed.
    }
    result = componentScopeForEvent({
      root, eventName: environment.GITHUB_EVENT_NAME, ref: environment.GITHUB_REF, event,
    });
  }
  if (environment.GITHUB_OUTPUT) {
    await appendFile(environment.GITHUB_OUTPUT, `run_components=${result.runComponents}\n`, 'utf8');
  }
  console.log(result.runComponents
    ? `Running full component checks (${result.reason}).`
    : `Skipping native component checks: ${result.changedPathCount} allowlisted documentation path(s). Workflow tests still run separately.`);
  return result;
}
