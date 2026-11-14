import { createHash } from 'node:crypto';
import { lstat, readdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { isApprovedReplacement } from './tailcat-patch.mjs';

export function parseVersion(value) {
  const match = /^(?:v|go)?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.exec(value);
  if (!match || match[0] !== value) throw new Error(`Expected a stable numeric version; received ${JSON.stringify(value)}`);
  return match.slice(1).map(Number);
}

export function atLeast(actual, minimum) {
  const a = parseVersion(actual);
  const b = parseVersion(minimum);
  for (let i = 0; i < 3; i++) {
    if (a[i] !== b[i]) return a[i] > b[i];
  }
  return true;
}

export function validatePackageName(name) {
  if (/[\r\n\0]/.test(name) || !/^(?:@[a-z0-9._-]+\/)?[a-z0-9][a-z0-9._-]*$/.test(name)) {
    throw new Error('Invalid dependency package name');
  }
  return name;
}

export function within(root, candidate) {
  const relative = path.relative(path.resolve(root), path.resolve(candidate));
  return relative !== '' && !path.isAbsolute(relative) && relative !== '..'
    && !relative.startsWith(`..${path.sep}`);
}

export function safeArtifactName(name) {
  if (/[\r\n\0]/.test(name) || !/^[a-zA-Z0-9][a-zA-Z0-9._-]*$/.test(name)
      || /\.\./.test(name)
      || !/\.(?:exe|msi|msix|deb|rpm|AppImage|zip|tar\.gz)$/.test(name)) {
    throw new Error('Unsafe or unsupported artifact name');
  }
  return name;
}

export async function collectArtifacts(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    if (!entry.isFile()) continue;
    if (!/\.(?:exe|msi|msix|deb|rpm|AppImage|zip|tar\.gz)$/.test(entry.name)) continue;
    safeArtifactName(entry.name);
    const filename = path.join(directory, entry.name);
    const stat = await lstat(filename);
    if (!stat.isFile() || stat.isSymbolicLink()) throw new Error('Artifact must be a regular file');
    files.push({ name: entry.name, filename, size: stat.size });
  }
  if (files.length === 0) throw new Error('No native package artifacts were produced');
  return files.sort((a, b) => a.name.localeCompare(b.name, 'en'));
}

export async function checksums(files) {
  return (await Promise.all(files.map(async (file) => {
    safeArtifactName(file.name);
    const bytes = await readFile(file.filename);
    return `${createHash('sha256').update(bytes).digest('hex')}  ${file.name}`;
  }))).join('\n') + '\n';
}

export async function writeJson(filename, value) {
  await writeFile(filename, `${JSON.stringify(value, null, 2)}\n`, { encoding: 'utf8', mode: 0o600 });
}

export async function readJson(filename) {
  return JSON.parse(await readFile(filename, 'utf8'));
}

export async function requireApp(root) {
  for (const filename of ['go.mod', 'go.sum', 'frontend/package.json', 'frontend/package-lock.json']) {
    try { await lstat(path.join(root, filename)); }
    catch {
      throw new Error(`Application prerequisite missing: ${filename}. Restore the committed application manifests and lockfiles before running this command.`);
    }
  }
}

const ignoredSourceDirectories = new Set(['.git', '.local', 'node_modules', 'dist', 'artifacts', 'coverage', 'vendor', 'bin']);

// Walk actual checkout sources, including untracked work in a new local project.
// Never traverse symlinks, dependency trees, caches, or generated build outputs.
export async function collectSourceFiles(directory, extension) {
  const files = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const filename = path.join(directory, entry.name);
    if (entry.isSymbolicLink()) continue;
    if (entry.isDirectory()) {
      if (!ignoredSourceDirectories.has(entry.name)) files.push(...await collectSourceFiles(filename, extension));
    } else if (entry.isFile() && entry.name.endsWith(extension)) files.push(filename);
  }
  return files.sort();
}

export function requireFrontendScripts(manifest, names) {
  for (const name of names) {
    if (typeof manifest.scripts?.[name] !== 'string' || !manifest.scripts[name].trim()) {
      throw new Error(`Required frontend gate missing: ${name}`);
    }
  }
}

export async function requireFullReadiness(root, policy) {
  if (policy.applicationReady !== true) {
    throw new Error('Full application checks and packaging remain blocked: product/security gates are incomplete. Use npm run test:app for the implemented components. See README.md and SECURITY.md for the supported scope.');
  }
  if (policy.transportIntegrationVerified !== true) {
    throw new Error('Real direct, forced-relay, path-change, restart, and adversarial transport integration must be verified before full checks or packaging.');
  }
  const directory = path.join(root, 'tests/integration');
  let files;
  try { files = await collectSourceFiles(directory, '.go'); }
  catch { throw new Error('Real transport/pairing integration test sources are required'); }
  const tests = files.filter((file) => file.endsWith('_test.go'));
  if (!tests.length) throw new Error('Real transport/pairing integration test sources are required');
}

export function validateReviewedGoPins(pins) {
  const modules = ['github.com/tailscale/tailcat', 'tailscale.com', 'github.com/tailscale/wireguard-go'];
  for (const module of modules) {
    const value = pins?.[module];
    if (typeof value !== 'string' || /[\r\n\0]/.test(value) || !/^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(value)
        || /(?:latest|main|master)/.test(value)) {
      throw new Error(`Reviewed immutable Go dependency pin missing or invalid: ${module}`);
    }
  }
  return modules;
}

function selectedGoGraph(output, approvedReplacement) {
  const rows = output.split(/\r?\n/).filter((line) => line.trim()).map((line) => line.trim().split(/\s+/));
  for (const row of rows) {
    if (row.includes('=>') && !(row.length === 4 && row[2] === '=>'
        && isApprovedReplacement(row[0], row[1], row[3], approvedReplacement))) {
      throw new Error('Unreviewed selected graph replacement is not permitted for pinned tools or transport');
    }
  }
  return rows;
}

export function assertReviewedGoGraph(output, pins, approvedReplacement) {
  for (const module of validateReviewedGoPins(pins)) {
    const line = output.split(/\r?\n/).map((value) => value.trim()).find((value) => value.split(/\s+/)[0] === module);
    if (line !== `${module} ${pins[module]}`
        && !(line === `${module} ${pins[module]} => ${approvedReplacement?.path}`
          && isApprovedReplacement(module, pins[module], approvedReplacement?.path, approvedReplacement))) {
      throw new Error(`Reviewed transport dependency moved or was replaced: ${module}. Review upstream separately before changing its pin.`);
    }
  }
  selectedGoGraph(output, approvedReplacement);
}

export function stableNpmVersion(metadata, major) {
  const versions = Object.keys(metadata.versions ?? {}).filter((version) => {
    try { const parsed = parseVersion(version); return major === undefined || parsed[0] === major; }
    catch { return false; }
  });
  const candidate = metadata['dist-tags']?.latest;
  if (major === undefined && candidate) {
    parseVersion(candidate);
    return candidate;
  }
  versions.sort((left, right) => {
    const a = parseVersion(left), b = parseVersion(right);
    return b[0] - a[0] || b[1] - a[1] || b[2] - a[2];
  });
  if (!versions.length) throw new Error('No current stable version satisfies the reviewed compatibility constraint');
  return versions[0];
}

// Normalize casing so inherited uppercase NPM_CONFIG_* values cannot defeat
// the workflow's default policy, including on Windows's case-insensitive env.
// Explicit `npm audit` still performs the required advisory query.
export function privateToolEnvironment(environment) {
  const controlled = new Set(['npm_config_update_notifier', 'npm_config_audit', 'npm_config_fund']);
  const result = Object.fromEntries(Object.entries(environment).filter(([name]) => !controlled.has(name.toLowerCase())));
  return { ...result, npm_config_update_notifier: 'false', npm_config_audit: 'false', npm_config_fund: 'false' };
}

export function latestLtsNode(releases) {
  const candidates = releases.filter((release) => {
    if (!release.lts) return false;
    try { parseVersion(release.version); return true; } catch { return false; }
  }).map((release) => release.version.replace(/^v/, ''));
  candidates.sort((left, right) => {
    const a = parseVersion(left), b = parseVersion(right);
    return b[0] - a[0] || b[1] - a[1] || b[2] - a[2];
  });
  if (!candidates.length) throw new Error('Could not verify a supported stable Node LTS release');
  return candidates[0];
}

export function assertNodeRuntime(actual, required) {
  if (parseVersion(actual)[0] !== parseVersion(required)[0] || !atLeast(actual, required)) {
    throw new Error(`Use NVM to select this project's Node ${required} LTS or a newer patch in the same major before continuing`);
  }
}

export function assertToolBuildInfo(info, module, version, graph, minimumGo, approvedReplacement) {
  const compiler = /\bgo(\d+\.\d+\.\d+)\b/.exec(info)?.[1];
  if (!compiler || !atLeast(compiler, minimumGo)) throw new Error('Installed tool uses an outdated Go compiler; run npm run setup');
  const rows = info.split(/\r?\n/).map((line) => line.trim().split('\t'));
  const main = rows.find((row) => row[0] === 'mod');
  if (main?.[1] !== module || main?.[2] !== version) throw new Error('Installed tool version does not match the reviewed pin; run npm run setup');
  if (rows.some((row) => row[0] === '=>')) throw new Error('Installed tool has an unreviewed module replacement');
  // The selected root graph may include the verified app-private Tailcat fork.
  // Actual tool binaries must still contain no replacement: a path in buildinfo
  // cannot prove which local bytes were compiled into an old tool binary.
  const selected = new Map(selectedGoGraph(graph, approvedReplacement).map((row) => [row[0], row[1]]));
  for (const row of rows.filter((row) => row[0] === 'mod' || row[0] === 'dep')) {
    if (selected.get(row[1]) !== row[2]) throw new Error(`Installed tool dependency differs from the locked graph: ${row[1]}. Run npm run setup`);
  }
}
