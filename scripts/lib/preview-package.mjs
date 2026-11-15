import { createHash } from 'node:crypto';
import { cp, mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { packagingTool } from './packaging-tools.mjs';
import { readJson, validatePackageName, within } from './workflow.mjs';

export function previewArtifactName(version, platform = process.platform, arch = process.arch) {
  if (!/^\d+\.\d+\.\d+$/.test(version) || arch !== 'x64') throw new Error('Preview installers currently support native x64 only');
  if (platform === 'win32') return `WhatsUp-${version}-windows-x64-setup.exe`;
  if (platform === 'linux') return `whatsup_${version}_amd64.deb`;
  throw new Error('Unsupported native installer platform');
}

export async function nativeInstaller({ root, destination, version, binary, run }) {
  const name = previewArtifactName(version);
  const output = path.join(destination, name);
  if (!within(root, destination) || typeof binary !== 'string' || !within(root, binary)) throw new Error('Installer inputs and destination must be inside the workspace');
  if (process.platform === 'win32') {
    const tool = await packagingTool({ root, name: 'nsis', run });
    const reported = run(tool.executable, ['/VERSION'], { capture: true });
    if (reported !== `v${tool.version}`) throw new Error('NSIS compiler version mismatch');
    run(tool.executable, ['/NOCONFIG', `/DAPP_VERSION=${version}`, `/DAPP_BINARY=${binary}`,
      `/DOUTPUT_FILE=${output}`, path.join(root, 'build/windows/nsis/project.nsi')], { timeout: 300000 });
  } else {
    const tool = await packagingTool({ root, name: 'nfpm', run });
    const reported = run(tool.executable, ['--version'], { capture: true });
    if (!reported.includes(tool.version)) throw new Error('nFPM compiler version mismatch');
    run(tool.executable, ['--config', 'build/linux/nfpm/nfpm.yaml', '--target', output],
      { timeout: 300000, env: { GOARCH: 'amd64', WHATSUP_BINARY: binary, WHATSUP_PROBE: path.join(root, '.local/tools/linux-x64/whatsup-webview-check'),
        SOURCE_DATE_EPOCH: process.env.SOURCE_DATE_EPOCH ?? '0' } });
  }
  const bytes = await readFile(output);
  if (process.platform === 'win32' ? bytes.subarray(0, 2).toString() !== 'MZ' : bytes.subarray(0, 8).toString() !== '!<arch>\n') {
    throw new Error('Installer compiler did not produce the expected native container');
  }
  return { name, filename: output, size: bytes.length, sha256: createHash('sha256').update(bytes).digest('hex') };
}

export function npmLockComponents(lock) {
  if (lock?.lockfileVersion !== 3 || !lock.packages || typeof lock.packages !== 'object' || Array.isArray(lock.packages)) throw new Error('SBOM requires a current frozen npm lock');
  const entries = Object.entries(lock.packages);
  if (entries.length > 10000) throw new Error('npm inventory exceeds its bound');
  const components = [];
  for (const [location, item] of entries.sort(([a], [b]) => a.localeCompare(b, 'en'))) {
    if (!location) continue;
    if (location.length > 1024 || !location.startsWith('node_modules/') || location.split('/').includes('..') || item.link) throw new Error('Unsupported npm lock location');
    const name = validatePackageName(item.name ?? location.split('node_modules/').at(-1));
    if (typeof item.version !== 'string' || item.version.length > 100 || !/^[\w.+-]+$/.test(item.version)) throw new Error('npm lock version invalid');
    const ref = `npm:${location}@${item.version}`;
    const component = { type: 'library', 'bom-ref': ref, name, version: item.version,
      purl: `pkg:npm/${name.startsWith('@') ? '%40' + name.slice(1) : name}@${item.version}`,
      scope: item.dev === true ? 'excluded' : 'required',
      properties: [{ name: 'whatsup:npm:installPath', value: location }, { name: 'whatsup:npm:developmentOnly', value: String(item.dev === true) }] };
    if (item.integrity) {
      const match = /^(sha256|sha384|sha512)-([A-Za-z0-9+/]+={0,2})$/.exec(item.integrity);
      if (!match) throw new Error('npm lock integrity is invalid');
      const bytes = Buffer.from(match[2], 'base64');
      if (bytes.length !== Number(match[1].slice(3)) / 8) throw new Error('npm lock digest length invalid');
      component.hashes = [{ alg: `SHA-${match[1].slice(3)}`, content: bytes.toString('hex') }];
    }
    if (typeof item.license === 'string' && item.license.length <= 256) component.licenses = [{ license: { name: item.license } }];
    components.push(component);
  }
  if (!components.some((entry) => entry.name === 'svelte')) throw new Error('Frontend dependency inventory is incomplete');
  return components;
}

export async function createPreviewSBOM({ root, destination, version, binary, source, patches, run }) {
  const tool = await packagingTool({ root, name: 'cyclonedx-gomod', run });
  const parent = path.join(root, '.local/sbom-input');
  await mkdir(parent, { recursive: true });
  const staging = await mkdtemp(path.join(parent, `${process.platform}-`));
  try {
    // Only the checked executable and exact frozen frontend lock are inventoried.
    // No directory discovery, user data, logs, personal files or remote license
    // fetching is permitted. The tool reads Go build info; it never runs the app.
    await mkdir(path.join(staging, 'binary'));
    if (typeof binary !== 'string' || !within(root, binary)) throw new Error('SBOM requires the checked native executable');
    await cp(binary,
      path.join(staging, 'binary', process.platform === 'win32' ? 'whatsup.exe' : 'whatsup'));
    const target = path.join(destination, 'sbom.cdx.json');
    run(tool.executable, ['bin', '-json', '-notimestamp', '-noserial', '-std', '-output-version', '1.6', '-version', version,
      '-output', target, path.join(staging, 'binary', process.platform === 'win32' ? 'whatsup.exe' : 'whatsup')], { timeout: 300000 });
    const sbom = await readJson(target);
    if (sbom.bomFormat !== 'CycloneDX' || !Array.isArray(sbom.components) || !sbom.components.length) throw new Error('SBOM generation produced no dependency inventory');
    const lockFile = path.join(root, 'frontend/package-lock.json');
    const lockBytes = await readFile(lockFile);
    if (lockBytes.length > 32 * 1024 * 1024) throw new Error('npm lock exceeds its inventory bound');
    const frontend = npmLockComponents(JSON.parse(lockBytes));
    sbom.components.push(...frontend);
    const names = new Set(sbom.components.map((entry) => entry.name));
    if (!names.has('svelte') || ![...names].some((name) => name.includes('wails')) || ![...names].some((name) => name.includes('tailcat'))) {
      throw new Error('SBOM is missing the frozen frontend or native module inventories');
    }
    sbom.metadata ??= {};
    sbom.metadata.properties ??= [];
    sbom.metadata.properties.push({ name: 'whatsup:source:sha256', value: source.sourceDigest },
      { name: 'whatsup:source:commit', value: source.sourceCommit }, { name: 'whatsup:source:dirty', value: String(source.sourceDirty) },
      ...patches.flatMap((patch) => [
        { name: `whatsup:patch:${patch.module}:version`, value: String(patch.patchVersion) },
        { name: `whatsup:patch:${patch.module}:manifestSHA256`, value: patch.manifestSHA256 },
        ...(patch.treeSHA256 ? [{ name: `whatsup:patch:${patch.module}:treeSHA256`, value: patch.treeSHA256 }] : []),
      ]));
    // Syft's directory source metadata otherwise exposes the local workspace.
    if (sbom.metadata.component) sbom.metadata.component.name = 'WhatsUp';
    if (sbom.metadata.component?.['bom-ref']) {
      const main = sbom.dependencies?.find((entry) => entry.ref === sbom.metadata.component['bom-ref']);
      if (main) main.dependsOn = [...new Set([...(main.dependsOn ?? []), ...frontend.map((entry) => entry['bom-ref'])])].sort();
    }
    const serialized = JSON.stringify(sbom, null, 2).replaceAll(staging.replaceAll('\\', '\\\\'), '[allowlisted-build-input]').replaceAll(staging, '[allowlisted-build-input]') + '\n';
    await writeFile(target, serialized, 'utf8');
    return { name: 'cyclonedx-gomod', version: tool.version, kind: tool.kind, lockSHA256: tool.lockSHA256,
      binarySHA256: tool.binarySHA256, compilerGo: tool.compilerGo, scanPassed: true, format: 'CycloneDX JSON', file: 'sbom.cdx.json' };
  } finally {
    if (!within(parent, staging)) throw new Error('SBOM cleanup escapes its staging directory');
    await rm(staging, { recursive: true, force: true });
  }
}
