import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { createReadStream } from 'node:fs';
import { appendFile, cp, lstat, mkdir, open, readFile, readdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { captureReviewSource } from './review-source.mjs';
import { sourceToolIdentity } from './packaging-tools.mjs';
import { validateArAdvisoryEvidence } from './ar-writer-advisories.mjs';
import { atLeast, parseVersion, readJson, validSecurityScans, within, writeJson } from './workflow.mjs';

const platforms = ['win32-x64', 'linux-x64'];
const sha256 = /^[a-f0-9]{64}$/;
const commitID = /^[a-f0-9]{40}$/;

export function assertReleaseContext({ version, ref, event, actions, dirty, commit, tagCommit, repository }) {
  parseVersion(version);
  if (actions !== 'true' || !['push', 'workflow_dispatch'].includes(event)
      || ref !== `refs/tags/v${version}` || dirty !== false || !commitID.test(commit)
      || tagCommit !== commit || !/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository ?? '')) {
    throw new Error('Release CI requires a clean checkout of the exact application version tag; branches, dirty sources and mismatched tags are refused');
  }
  return { version, tag: `v${version}`, commit, repository };
}

function git(root, args) {
  const result = spawnSync('git', args, { cwd: root, shell: false, windowsHide: true, encoding: 'utf8' });
  if (result.error || result.status !== 0) throw new Error('Release tag verification failed');
  return result.stdout.trim();
}

export async function releasePreflight(root) {
  const { version } = await readJson(path.join(root, 'package.json'));
  const source = await captureReviewSource(root);
  const context = assertReleaseContext({ version, ref: process.env.GITHUB_REF, event: process.env.GITHUB_EVENT_NAME,
    actions: process.env.GITHUB_ACTIONS, dirty: source.sourceDirty, commit: source.sourceCommit,
    tagCommit: git(root, ['rev-parse', '--verify', `refs/tags/v${version}^{commit}`]), repository: process.env.GITHUB_REPOSITORY });
  const result = { ...context, sourceDigestSHA256: source.sourceDigest };
  await mkdir(path.join(root, '.local'), { recursive: true });
  await writeJson(path.join(root, '.local/release-context.json'), result);
  if (process.env.GITHUB_OUTPUT) await appendFile(process.env.GITHUB_OUTPUT, `version=${version}\n`);
  return result;
}

export function parseReleaseChecksums(text) {
  const result = new Map();
  if (typeof text !== 'string' || text.length > 16_384) throw new Error('Package checksums are invalid');
  for (const line of text.trimEnd().split('\n')) {
    const match = /^([a-f0-9]{64})  ([A-Za-z0-9][A-Za-z0-9._-]{0,159})$/.exec(line);
    if (!match || match[2].includes('..') || result.has(match[2])) throw new Error('Package checksums contain an unsafe or duplicate name');
    result.set(match[2], match[1]);
  }
  return result;
}

async function boundedFile(filename, maxBytes) {
  const info = await lstat(filename);
  if (!info.isFile() || info.isSymbolicLink() || info.size < 1 || info.size > maxBytes) throw new Error('Release input must be a bounded regular file');
  return info;
}

async function digestFile(filename) {
  await boundedFile(filename, 1024 ** 3);
  const hash = createHash('sha256');
  for await (const bytes of createReadStream(filename)) hash.update(bytes);
  return hash.digest('hex');
}

export function releaseNames(version, platform) {
  parseVersion(version);
  if (!platforms.includes(platform)) throw new Error('Unsupported release platform');
  return {
    installer: platform === 'win32-x64' ? `WhatsUp-${version}-windows-x64-setup.exe` : `whatsup_${version}_amd64.deb`,
    sbom: `whatsup_${version}_${platform}.sbom.cdx.json`,
    manifest: `whatsup_${version}_${platform}.build-manifest.json`,
  };
}

export async function verifyReleasePackage(directory, context, platform) {
  const names = releaseNames(context.version, platform);
  const allowed = [names.installer, 'sbom.cdx.json', 'build-manifest.json', 'SHA256SUMS'];
  const info = await lstat(directory);
  if (!info.isDirectory() || info.isSymbolicLink()) throw new Error('Release input cannot be a symlink');
  const entries = await readdir(directory);
  if (entries.length !== allowed.length || entries.some((name) => !allowed.includes(name))) throw new Error('Release package is missing files or contains unexpected files');
  await boundedFile(path.join(directory, 'SHA256SUMS'), 16_384);
  const sums = parseReleaseChecksums(await readFile(path.join(directory, 'SHA256SUMS'), 'utf8'));
  if (sums.size !== 3 || allowed.filter((name) => name !== 'SHA256SUMS').some((name) => !sums.has(name))) throw new Error('Release checksums do not cover the exact package');
  for (const [name, digest] of sums) {
    if (await digestFile(path.join(directory, name)) !== digest) throw new Error('Release input checksum mismatch');
  }
  await boundedFile(path.join(directory, 'build-manifest.json'), 2 ** 20);
  const manifest = await readJson(path.join(directory, 'build-manifest.json'));
  if (manifest.version !== context.version || manifest.sourceCommit !== context.commit
      || manifest.sourceDigestSHA256 !== context.sourceDigestSHA256 || manifest.sourceDirty !== false
      || manifest.unsignedPreview !== true || `${manifest.platform}-${manifest.arch}` !== platform
      || !sha256.test(manifest.sourceDigestSHA256)) throw new Error('Package provenance does not match this clean tagged workflow');
  const checks = manifest.checks;
  const checkedAt = Date.parse(checks?.recordedAt ?? '');
  const now = Date.now();
  if (manifest.schemaVersion !== 1 || manifest.signed !== false || manifest.authenticode !== false
      || manifest.inAppUpdatesEnabled !== false || manifest.releaseEligible !== false
      || checks?.functional !== 'passed' || checks?.integration !== 'passed' || checks?.nativeBuild !== 'passed'
      || !validSecurityScans(checks.scans) || checks.scans.some((scan) => scan.passed !== true)
      || checks.goRaceEnabled !== true
      || checks.nativeUIAcceptance !== 'not-run-by-this-command'
      || !Number.isFinite(checkedAt) || checkedAt > now + 5 * 60_000 || now - checkedAt > 24 * 60 * 60_000) {
    throw new Error('Package checks are missing, failed, stale or inconsistent with an unsigned preview');
  }
  for (const scan of checks.scans) {
    if (scan.exceptions === undefined) continue;
    if (!Array.isArray(scan.exceptions)) throw new Error('Advisory exception evidence is malformed');
    if (!scan.exceptions.length) continue;
    const exception = context.advisoryException;
    const accepted = scan.exceptions[0];
    if (scan.name !== 'frontend npm audit' || scan.exceptions.length !== 1
        || !exception || accepted?.advisory !== exception.advisory || accepted.package !== exception.package
        || accepted.version !== exception.version || accepted.scope !== exception.scope || accepted.expiresOn !== exception.expiresOn
        || !Number.isFinite(Date.parse(exception.expiresOn + 'T00:00:00Z')) || now >= Date.parse(exception.expiresOn + 'T00:00:00Z')) {
      throw new Error('Package uses an unapproved or expired advisory exception');
    }
  }
  const installerInfo = await boundedFile(path.join(directory, names.installer), 1024 ** 3);
  if (!Array.isArray(manifest.files) || manifest.files.length !== 1 || manifest.files[0]?.name !== names.installer
      || manifest.files[0]?.sha256 !== sums.get(names.installer) || manifest.files[0]?.size !== installerInfo.size) {
    throw new Error('Manifest does not identify the exact installer');
  }
  const compilerName = platform === 'win32-x64' ? 'nsis' : 'nfpm';
  const sourceEntries = [];
  for (const [entry, name] of [[manifest.packaging, compilerName], [manifest.sbom, 'cyclonedx-gomod']]) {
    const pin = context.packagingTools?.tools?.[name];
    if (entry?.name !== name || entry.version !== pin?.version) throw new Error('Package tool evidence does not match the pin');
    if (pin.kind === 'source') {
      const identity = context.sourceTools?.[name];
      if (entry.kind !== 'source' || !identity || entry.lockSHA256 !== identity.lockSHA256
          || !sha256.test(entry.binarySHA256 ?? '') || entry.scanPassed !== true
          || typeof entry.compilerGo !== 'string' || !atLeast(entry.compilerGo, context.minimumGo)) {
        throw new Error('Source-built tool evidence does not match its frozen graph and binary scan');
      }
      sourceEntries.push(entry);
    } else if (entry.kind !== 'archive' || !sha256.test(entry.archiveSHA256 ?? '')
        || entry.archiveSHA256 !== pin?.[platform]?.sha256) throw new Error('Package tool evidence does not match the pinned archive');
  }
  if (!Array.isArray(manifest.packagingToolScans) || manifest.packagingToolScans.length !== sourceEntries.length
      || new Set(manifest.packagingToolScans.map((entry) => entry?.name)).size !== sourceEntries.length
      || sourceEntries.some((tool) => !manifest.packagingToolScans.some((scan) => scan?.name === tool.name
        && scan.version === tool.version && scan.binarySHA256 === tool.binarySHA256 && scan.passed === true))) {
    throw new Error('Packaging tool scans are missing or do not cover the emitted binaries');
  }
  for (const scan of manifest.packagingToolScans) validateArAdvisoryEvidence(scan.upstreamAr, context.arWriterPolicy);
  if (manifest.sbom.file !== 'sbom.cdx.json' || manifest.sbom.format !== 'CycloneDX JSON') throw new Error('Package SBOM evidence is invalid');
  await boundedFile(path.join(directory, 'sbom.cdx.json'), 32 * 2 ** 20);
  const sbom = await readJson(path.join(directory, 'sbom.cdx.json'));
  if (sbom.bomFormat !== 'CycloneDX' || !Array.isArray(sbom.components) || sbom.components.length === 0) throw new Error('Package SBOM is missing or invalid');
  for (const [name, value] of [['whatsup:source:commit', context.commit], ['whatsup:source:sha256', context.sourceDigestSHA256], ['whatsup:source:dirty', 'false']]) {
    const entries = sbom.metadata?.properties?.filter((entry) => entry?.name === name);
    if (!Array.isArray(entries) || entries.length !== 1 || entries[0].value !== value) throw new Error('SBOM provenance does not match the tagged package');
  }
  const handle = await open(path.join(directory, names.installer), 'r');
  try {
    const prefix = Buffer.alloc(8);
    await handle.read(prefix, 0, 8, 0);
    if (platform === 'win32-x64' ? prefix.subarray(0, 2).toString() !== 'MZ' : prefix.toString() !== '!<arch>\n') throw new Error('Installer format does not match its platform');
  } finally { await handle.close(); }
  return { names, manifest };
}

export async function releaseCollect(root) {
  const context = await releasePreflight(root);
  context.packagingTools = await readJson(path.join(root, 'config/packaging-tools.json'));
  context.advisoryException = await readJson(path.join(root, 'config/advisory-exception.json'));
  context.minimumGo = (await readJson(path.join(root, 'config/toolchain.json'))).minimumGo;
  context.arWriterPolicy = await readJson(path.join(root, 'config/nfpm-ar-writer.json'));
  context.sourceTools = Object.fromEntries(await Promise.all(['nfpm', 'cyclonedx-gomod'].map(async (name) =>
    [name, await sourceToolIdentity({ root, name })])));
  const output = path.join(root, 'artifacts/release');
  await mkdir(output, { recursive: true });
  if ((await readdir(output)).length) throw new Error('Preserve existing release output before collecting another bundle');
  for (const platform of platforms) {
    const input = path.join(root, 'artifacts/release-input', platform);
    const { names } = await verifyReleasePackage(input, context, platform);
    for (const [from, to] of [[names.installer, names.installer], ['sbom.cdx.json', names.sbom], ['build-manifest.json', names.manifest]]) {
      if (!within(output, path.join(output, to))) throw new Error('Release output escaped its directory');
      await cp(path.join(input, from), path.join(output, to), { force: false, errorOnExist: true });
    }
  }
  const sums = [];
  for (const name of (await readdir(output)).sort()) sums.push(`${await digestFile(path.join(output, name))}  ${name}`);
  await writeFile(path.join(output, 'SHA256SUMS'), sums.join('\n') + '\n', { flag: 'wx' });
  return output;
}

export function changelogSection(markdown, version) {
  parseVersion(version);
  if (typeof markdown !== 'string' || markdown.length > 262_144) throw new Error('CHANGELOG.md is missing or too large');
  const lines = markdown.replace(/\r\n/g, '\n').split('\n');
  const heading = new RegExp(`^## ${version.replace(/\./g, '\\.')}(?: \\(.*\\))?$`);
  const start = lines.findIndex((line) => heading.test(line));
  if (start < 0) throw new Error(`CHANGELOG.md has no "## ${version}" section; add release notes before tagging`);
  const end = lines.findIndex((line, index) => index > start && line.startsWith('## '));
  const body = lines.slice(start + 1, end < 0 ? undefined : end).join('\n').trim();
  if (!/^- \S/m.test(body) || body.length > 16_384) throw new Error(`CHANGELOG.md section ${version} needs at least one bullet and must stay bounded`);
  return unwrapMarkdown(body);
}

// GitHub can render single newlines in release bodies as hard breaks.
function unwrapMarkdown(text) {
  const output = [];
  for (const line of text.split('\n')) {
    const trimmed = line.trim();
    const previous = output.at(-1);
    if (!trimmed || trimmed.startsWith('- ') || trimmed.startsWith('#') || !previous) output.push(trimmed);
    else output[output.length - 1] = `${previous} ${trimmed}`;
  }
  return output.join('\n');
}

export function previewReleaseNotes(version, repository, changes) {
  const names = { windows: releaseNames(version, 'win32-x64').installer, linux: releaseNames(version, 'linux-x64').installer };
  if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(repository)) throw new Error('Invalid release repository');
  if (typeof changes !== 'string' || !changes.trim()) throw new Error('Release notes need the changelog section');
  const workflow = `${repository}/.github/workflows/release.yml`;
  return `## What's new

${changes.trim()}

## Downloads

| System | File |
| --- | --- |
| Windows 10 or 11 (x64) | \`${names.windows}\` |
| Ubuntu 24.04 (x64) | \`${names.linux}\` |

\`SHA256SUMS\` lists a checksum for every file, and each platform has a CycloneDX SBOM.

## Install

**Windows:** run the installer. WhatsUp needs Microsoft's WebView2 runtime, which Windows 10 and 11 normally include. The installer isn't code-signed yet, so SmartScreen may say "Windows protected your PC". Verify the download, then choose **More info** and **Run anyway**.

**Ubuntu:** install the package from the folder you downloaded it to, then open WhatsUp from the application menu:

\`\`\`bash
sudo apt install ./${names.linux}
\`\`\`

Quit WhatsUp from its tray before upgrading. Upgrades keep your identity, contacts and history, and uninstalling keeps your personal data. Before relying on a preview, export a recovery copy from **Settings → Security & recovery**.

## Verify your download

<details>
<summary>Checksums and build attestations</summary>

On Windows, compare the output with the installer's line in \`SHA256SUMS\`:

\`\`\`powershell
Get-FileHash .\\${names.windows} -Algorithm SHA256
\`\`\`

On Ubuntu, with the package and \`SHA256SUMS\` in the same folder:

\`\`\`bash
sha256sum --ignore-missing --check SHA256SUMS
\`\`\`

A checksum shows the bytes are unchanged but doesn't prove who built them. GitHub build attestations do. With the GitHub CLI installed:

\`\`\`bash
gh attestation verify ${names.windows} --repo ${repository} --signer-workflow ${workflow}
gh attestation verify ${names.linux} --repo ${repository} --signer-workflow ${workflow}
\`\`\`

</details>

## Good to know

- This is an unsigned preview with no independent security audit. See [SECURITY.md](https://github.com/${repository}/blob/v${version}/SECURITY.md).
- Attestations link each file to this repository's release workflow. They are not Windows code signing.
- In-app updates are off. Download new versions from this page.
- Both devices should run the same version.
`;
}

export function releaseArguments(context, notesPath, files) {
  if (files.length !== 7 || files.some((name) => typeof name !== 'string' || !path.isAbsolute(name))) throw new Error('A complete verified release bundle is required');
  return ['release', 'create', context.tag, ...files, '--repo', context.repository, '--verify-tag', '--latest',
    '--title', `${context.tag} (unsigned preview)`, '--notes-file', notesPath];
}

export async function releasePublish(root) {
  const context = await releasePreflight(root);
  const directory = path.join(root, 'artifacts/release');
  const expected = [...platforms.flatMap((platform) => Object.values(releaseNames(context.version, platform))), 'SHA256SUMS'].sort();
  const entries = (await readdir(directory)).sort();
  if (JSON.stringify(entries) !== JSON.stringify(expected)) throw new Error('Release bundle is incomplete or has unexpected files');
  await boundedFile(path.join(directory, 'SHA256SUMS'), 16_384);
  const sums = parseReleaseChecksums(await readFile(path.join(directory, 'SHA256SUMS'), 'utf8'));
  if (sums.size !== 6) throw new Error('Release bundle checksum inventory is incomplete');
  for (const name of expected.filter((name) => name !== 'SHA256SUMS')) {
    if (await digestFile(path.join(directory, name)) !== sums.get(name)) throw new Error('Release checksum mismatch');
  }
  const changes = changelogSection(await readFile(path.join(root, 'CHANGELOG.md'), 'utf8'), context.version);
  const notes = path.join(root, '.local/release-notes.md');
  await writeFile(notes, previewReleaseNotes(context.version, context.repository, changes));
  const args = releaseArguments(context, notes, expected.map((name) => path.join(directory, name)));
  const result = spawnSync('gh', args, { cwd: root, shell: false, windowsHide: true, stdio: 'inherit' });
  if (result.error || result.status !== 0) throw new Error('Release creation failed. An existing release for this tag is never overwritten; delete it deliberately before rerunning');
}
