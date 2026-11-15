import { createHash } from 'node:crypto';
import { chmod, lstat, mkdir, readFile, readdir, rename, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { atLeast, readJson, within, writeJson } from './workflow.mjs';

const hash = (bytes) => createHash('sha256').update(bytes).digest('hex');
const platform = `${process.platform}-${process.arch}`;

export function validateToolAsset(name, definition, target = platform) {
  const asset = definition?.[target];
  if (!['nsis', 'nfpm', 'cyclonedx-gomod'].includes(name) || !/^\d+\.\d+(?:\.\d+)?$/.test(definition?.version ?? '')
      || !asset || !/^[\w./-]+$/.test(asset.executable)
      || asset.executable.split('/').some((part) => part === '..') || path.posix.isAbsolute(asset.executable)) {
    throw new Error(`Missing or invalid pinned packaging tool: ${name}/${target}`);
  }
  if (definition.kind === 'source') {
    const expected = name === 'nfpm' ? ['github.com/goreleaser/nfpm/v2', '.']
      : ['github.com/CycloneDX/cyclonedx-gomod', 'github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod'];
    if (definition.module !== expected[0] || definition.package !== expected[1]) throw new Error('Unexpected packaging source tool');
    return asset;
  }
  if (!/^[a-f0-9]{64}$/.test(asset.sha256)) throw new Error('Pinned archive SHA-256 missing');
  const url = new URL(asset.url);
  if (url.protocol !== 'https:' || url.username || url.password || url.search || url.hash
      || !['github.com', 'downloads.sourceforge.net'].includes(url.hostname) || !url.pathname.includes(definition.version)) {
    throw new Error('Packaging tools require a fixed official HTTPS archive and SHA-256');
  }
  return asset;
}

async function treeDigest(directory) {
  const entries = [];
  async function visit(current) {
    for (const entry of await readdir(current, { withFileTypes: true })) {
      if (entry.name === '.tool-install.json' && current === directory) continue;
      const filename = path.join(current, entry.name);
      if (entry.isSymbolicLink()) throw new Error('A packaging tool cannot contain symlinks');
      if (entry.isDirectory()) await visit(filename);
      else {
        const info = await lstat(filename);
        if (!info.isFile()) throw new Error('A packaging tool must contain regular files');
        entries.push([path.relative(directory, filename).split(path.sep).join('/'), await readFile(filename)]);
      }
    }
  }
  await visit(directory);
  const digest = createHash('sha256');
  for (const [name, bytes] of entries.sort(([a], [b]) => a.localeCompare(b, 'en'))) digest.update(name + '\0' + bytes.length + '\0').update(bytes);
  return digest.digest('hex');
}

function location(root, name, definition) {
  return path.join(root, '.local/tools', platform, 'packaging', `${name}-${definition.version}${definition.kind === 'source' ? '-source' : ''}`);
}

async function lockDigest(root, definition) {
  const digest = createHash('sha256').update(JSON.stringify(definition));
  const directory = path.join(root, 'tools/packaging');
  const expected = ['go.mod', 'go.sum', 'main.go'];
  for (const parent of [path.join(root, 'tools'), directory]) {
    const info = await lstat(parent);
    if (!info.isDirectory() || info.isSymbolicLink()) throw new Error('Packaging source cannot contain directory links');
  }
  if (JSON.stringify((await readdir(directory)).sort()) !== JSON.stringify(expected)) throw new Error('The frozen packaging wrapper inventory changed');
  for (const name of expected) {
    const filename = path.join(directory, name);
    const info = await lstat(filename);
    if (!info.isFile() || info.isSymbolicLink() || info.size > 1024 * 1024) throw new Error('Packaging source must be bounded regular files without links');
    digest.update(name + '\0').update((await readFile(filename, 'utf8')).replaceAll('\r\n', '\n'));
  }
  const subset = await verifyArWriterSubset(root);
  digest.update('config/nfpm-ar-writer.json\0').update((await readFile(path.join(root, 'config/nfpm-ar-writer.json'), 'utf8')).replaceAll('\r\n', '\n'));
  for (const name of Object.keys(subset.filesSHA256).sort()) digest.update(name + '\0').update(await readFile(path.join(root, 'third_party/ar-writer', name)));
  return digest.digest('hex');
}

export async function verifyArWriterSubset(root) {
  const directory = path.join(root, 'third_party/ar-writer');
  const manifest = await readJson(path.join(root, 'config/nfpm-ar-writer.json'));
  const expected = ['COPYING', 'common.go', 'go.mod', 'writer.go'];
  if (manifest.schemaVersion !== 1 || manifest.module !== 'github.com/blakesmith/ar'
      || manifest.upstreamVersion !== 'v0.0.0-20190502131153-809d4375e1fb'
      || manifest.upstreamModuleSum !== 'h1:m935MPodAbYS46DG4pJSv7WO+VECIWUQ7OJYSoTrMh4='
      || JSON.stringify(Object.keys(manifest.filesSHA256 ?? {}).sort()) !== JSON.stringify(expected)
      || JSON.stringify((await readdir(directory)).sort()) !== JSON.stringify(expected)) throw new Error('The reviewed ar writer-only inventory changed');
  const directoryInfo = await lstat(directory);
  if (!directoryInfo.isDirectory() || directoryInfo.isSymbolicLink()) throw new Error('The ar writer subset cannot be a link');
  for (const name of expected) {
    const filename = path.join(directory, name);
    const info = await lstat(filename);
    if (!info.isFile() || info.isSymbolicLink() || info.size > 16384 || hash(await readFile(filename)) !== manifest.filesSHA256[name]) throw new Error('The reviewed ar writer-only bytes changed');
  }
  return manifest;
}

export async function sourceToolIdentity({ root, name }) {
  const definition = (await readJson(path.join(root, 'config/packaging-tools.json'))).tools?.[name];
  if (definition?.kind !== 'source') throw new Error('Expected a frozen source-built packaging tool');
  validateToolAsset(name, definition, name === 'nfpm' ? 'linux-x64' : 'win32-x64');
  return { module: definition.module, version: definition.version, lockSHA256: await lockDigest(root, definition) };
}

function inspectSourceTool(root, executable, definition, run, minimumGo) {
  if (typeof run !== 'function') throw new Error('Source tool inspection requires the trusted runner');
  const info = run('go', ['version', '-m', executable], { capture: true });
  const compilerGo = /:\s+go(\d+\.\d+\.\d+)/.exec(info)?.[1];
  if (!compilerGo || !atLeast(compilerGo, minimumGo)) throw new Error('Packaging tool uses an outdated Go compiler');
  const graph = run('go', ['list', '-m', '-f', '{{.Path}} {{.Version}}{{with .Replace}} => {{.Path}} {{.Version}}{{end}}', 'all'],
    { cwd: path.join(root, 'tools/packaging'), env: { GOWORK: 'off' }, capture: true, timeout: 300000 });
  for (const line of graph.split(/\r?\n/).filter((line) => line.includes('=>'))) {
    if (!/^github\.com\/blakesmith\/ar v0\.0\.0-20190502131153-809d4375e1fb => \.\.\/\.\.\/third_party\/ar-writer\s*$/.test(line)) throw new Error('Unreviewed packaging tool replacement');
  }
  let previousModule = '';
  for (const line of info.split(/\r?\n/)) {
    if (line.trimStart().startsWith('=>') && (previousModule !== 'github.com/blakesmith/ar'
        || !/^\s*=>\s+\.\.\/\.\.\/third_party\/ar-writer\s+\(devel\)\s*$/.test(line))) throw new Error('Unreviewed packaging binary replacement');
    const module = /^\s*(?:mod|dep)\s+(\S+)/.exec(line);
    if (module) previousModule = module[1];
  }
  const selected = new Map(graph.split(/\r?\n/).map((line) => line.trim().split(/\s+/)));
  let found = false;
  for (const line of info.split(/\r?\n/)) {
    const match = /^\s*(mod|dep)\s+(\S+)\s+(\S+)/.exec(line);
    if (!match) continue;
    if (match[2] === 'whatsup-buildtools' && match[3] === '(devel)' && definition.package === '.') continue;
    if (selected.get(match[2]) !== match[3]) throw new Error('Packaging binary dependencies differ from the frozen source graph');
    if (match[2] === definition.module && match[3] === `v${definition.version}`) found = true;
  }
  if (!found) throw new Error('Packaging binary is missing its exact reviewed module version');
  return compilerGo;
}

export async function packagingTool({ root, name, run }) {
  const definition = (await readJson(path.join(root, 'config/packaging-tools.json'))).tools?.[name];
  const asset = validateToolAsset(name, definition);
  const lockSHA256 = definition.kind === 'source' ? await lockDigest(root, definition) : null;
  const directory = location(root, name, definition) + (lockSHA256 ? `-${lockSHA256.slice(0, 12)}` : '');
  let receipt;
  try { receipt = await readJson(path.join(directory, '.tool-install.json')); }
  catch { throw new Error(`Pinned ${name} is missing; run npm run setup`); }
  if ((definition.kind !== 'source' && receipt.archiveSHA256 !== asset.sha256) || receipt.version !== definition.version
      || receipt.treeSHA256 !== await treeDigest(directory)) throw new Error(`Pinned ${name} files changed; inspect and rerun setup`);
  const executable = path.join(directory, asset.executable);
  if (!within(directory, executable) || !(await lstat(executable)).isFile()) throw new Error('Pinned packaging executable is invalid');
  if (definition.kind === 'source') {
    if (receipt.lockSHA256 !== await lockDigest(root, definition) || receipt.binarySHA256 !== hash(await readFile(executable))) throw new Error('Source tool lock or binary changed');
    const compilerGo = inspectSourceTool(root, executable, definition, run, (await readJson(path.join(root, 'config/toolchain.json'))).minimumGo);
    if (compilerGo !== receipt.compilerGo) throw new Error('Source tool compiler evidence changed');
    return { executable, version: definition.version, kind: 'source', lockSHA256: receipt.lockSHA256, binarySHA256: receipt.binarySHA256, compilerGo };
  }
  return { executable, version: definition.version, kind: 'archive', archiveSHA256: asset.sha256 };
}

export async function installPackagingTools({ root, run }) {
  const config = await readJson(path.join(root, 'config/packaging-tools.json'));
  for (const name of [process.platform === 'win32' ? 'nsis' : 'nfpm', 'cyclonedx-gomod']) {
    const definition = config.tools?.[name];
    const asset = validateToolAsset(name, definition);
    try { await packagingTool({ root, name, run }); continue; } catch { /* Install only the pinned inputs below. */ }
    const lockSHA256 = definition.kind === 'source' ? await lockDigest(root, definition) : null;
    const directory = location(root, name, definition) + (lockSHA256 ? `-${lockSHA256.slice(0, 12)}` : '');
    const temporary = `${directory}.download`;
    if (!within(path.join(root, '.local/tools'), temporary)) throw new Error('Tool staging escapes the local tool directory');
    await mkdir(path.dirname(directory), { recursive: true });
    try { await lstat(directory); throw new Error(`Existing ${name} tree failed integrity validation; set it aside before setup`); }
    catch (error) { if (error.code !== 'ENOENT') throw error; }
    try { await lstat(temporary); throw new Error('A previous packaging-tool staging directory needs inspection'); }
    catch (error) { if (error.code !== 'ENOENT') throw error; }
    await mkdir(temporary, { mode: 0o700 });
    try {
      if (definition.kind === 'source') {
        const locked = await lockDigest(root, definition);
        const cwd = path.join(root, 'tools/packaging');
        run('go', ['mod', 'download'], { cwd, env: { GOWORK: 'off' }, timeout: 300000 });
        run('go', ['mod', 'verify'], { cwd, env: { GOWORK: 'off' }, timeout: 300000 });
        const executable = path.join(temporary, asset.executable);
        run('go', ['build', '-mod=readonly', '-trimpath', '-buildvcs=false', '-ldflags=-s -w', '-o', executable, definition.package],
          { cwd, env: { GOWORK: 'off', CGO_ENABLED: '0' }, timeout: 600000 });
        if (locked !== await lockDigest(root, definition)) throw new Error('Packaging source build changed the frozen graph');
        const compilerGo = inspectSourceTool(root, executable, definition, run, (await readJson(path.join(root, 'config/toolchain.json'))).minimumGo);
        await writeJson(path.join(temporary, '.tool-install.json'), { kind: 'source', version: definition.version, lockSHA256: locked,
          binarySHA256: hash(await readFile(executable)), compilerGo, treeSHA256: await treeDigest(temporary) });
        await rename(temporary, directory);
        console.log(`Built pinned ${name} ${definition.version} from frozen sources with Go ${compilerGo}.`);
        continue;
      }
      const response = await fetch(asset.url, { signal: AbortSignal.timeout(300000) });
      if (!response.ok) throw new Error(`Pinned ${name} download failed with HTTP ${response.status}`);
      const chunks = []; let size = 0;
      for await (const chunk of response.body) {
        size += chunk.length;
        if (size > 128 * 1024 * 1024) throw new Error('Pinned tool archive exceeds its size bound');
        chunks.push(chunk);
      }
      const archive = Buffer.concat(chunks);
      if (hash(archive) !== asset.sha256) throw new Error(`Pinned ${name} archive SHA-256 mismatch`);
      const archivePath = path.join(temporary, asset.url.endsWith('.zip') ? 'archive.zip' : 'archive.tar.gz');
      await writeFile(archivePath, archive, { flag: 'wx', mode: 0o600 });
      const entries = run('tar', ['-tf', archivePath], { capture: true }).split(/\r?\n/).filter(Boolean);
      if (!entries.length || entries.length > 20000 || entries.some((entry) => /[\x00-\x1f\\:]/.test(entry)
          || entry.startsWith('/') || entry.split('/').includes('..'))) throw new Error('Unsafe packaging archive paths');
      const verbose = run('tar', ['-tvf', archivePath], { capture: true });
      if (verbose.split(/\r?\n/).filter(Boolean).some((line) => !/^[d-]/.test(line))) throw new Error('Packaging archives must not contain links or special files');
      const extracted = path.join(temporary, 'extracted');
      await mkdir(extracted, { mode: 0o700 });
      run('tar', ['-xf', archivePath, '-C', extracted]);
      const executable = path.join(extracted, asset.executable);
      if (!(await lstat(executable)).isFile()) throw new Error('Pinned tool executable missing from archive');
      if (process.platform !== 'win32') await chmod(executable, 0o700);
      await writeJson(path.join(extracted, '.tool-install.json'), {
        version: definition.version, archiveSHA256: asset.sha256, treeSHA256: await treeDigest(extracted),
      });
      await rename(extracted, directory);
      console.log(`Installed pinned ${name} ${definition.version} with verified archive and extracted-tree hashes.`);
    } finally { await rm(temporary, { recursive: true, force: true }); }
  }
}
