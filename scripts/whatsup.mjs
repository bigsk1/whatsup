import { spawnSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { cp, mkdir, readdir, readFile, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createHash } from 'node:crypto';
import { setTimeout as delay } from 'node:timers/promises';
import { archiveReviewBuild } from './lib/review-build.mjs';
import { captureReviewSource } from './lib/review-source.mjs';
import { verifyTailcatPatch } from './lib/tailcat-patch.mjs';
import { ensureWailsPatch, prepareOfficialToolModule, retainReplacementSums, verifyWailsDefinition, verifyWailsPatch } from './lib/wails-patch.mjs';
import { replacementAdvisories } from './lib/replacement-advisories.mjs';
import { installPackagingTools, packagingTool } from './lib/packaging-tools.mjs';
import { evaluateNpmAudit } from './lib/npm-advisories.mjs';
import { arWriterAdvisories } from './lib/ar-writer-advisories.mjs';
import { createPreviewSBOM, nativeInstaller, previewArtifactName } from './lib/preview-package.mjs';
import { releasePreflight, releaseCollect, releasePublish } from './lib/release-ci.mjs';
import { runComponentScope } from './lib/ci-scope.mjs';
import {
  assertNodeRuntime, assertReviewedGoGraph, assertToolBuildInfo, atLeast, checksums, collectArtifacts, collectSourceFiles,
  latestLtsNode, parseVersion, privateToolEnvironment, readJson, requireApp, requireFrontendScripts, requireFullReadiness,
  stableNpmVersion, validatePackageName, validateReviewedGoPins, validSecurityScans, within, writeJson,
} from './lib/workflow.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const frontendDirectory = path.join(root, 'frontend');
const configFile = path.join(root, 'config/toolchain.json');
const policy = await readJson(configFile);
const command = process.argv[2] ?? 'doctor';
const flags = process.argv.slice(3);
const toolDirectory = path.join(root, '.local/tools', `${process.platform}-${process.arch}`);
// Supported by the reviewed Tailcat/Tailscale revision. Own redacted local
// diagnostics are independent from these upstream collectors/uploaders.
const privacyTags = 'ts_omit_logtail,ts_omit_netlog,ts_omit_clientmetrics,ts_omit_usermetrics,ts_omit_runtimemetrics,ts_omit_useproxy,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_ssh';
// Hosted Windows runners take about four times longer than a developer
// machine; the messaging package alone nears Go's default 10m binary limit.
// Hosted runners also share one slow disk: concurrent fsync-heavy storage
// packages push wall-clock-bounded transactions past their limits there.
const goTestFlags = ['-timeout=30m', ...(process.env.GITHUB_ACTIONS === 'true' ? ['-p=1'] : [])];
const goTestRunnerTimeout = 60 * 60 * 1000;
const commands = new Set([
  'doctor', 'tooling-test', 'setup', 'dev', 'test-app', 'test-credentials', 'native-check', 'wsl', 'build', 'build-review', 'check', 'update', 'package', 'verify-release',
  // Reviewed Wails Taskfiles delegate to these same cross-platform implementations.
  'native-build', 'native-package', 'frontend-dev', 'frontend-build', 'generate-bindings', 'run-native',
  'release-preflight', 'release-collect', 'release-publish', 'ci-scope',
]);

function run(executable, args, { cwd = root, capture = false, timeout = 120000, env = {}, mutableGo = false, acceptedStatuses = [0] } = {}) {
  verifyTailcatPatch(root, policy.reviewedGoPins);
  verifyWailsDefinition(root);
  const toolchainOnly = path.basename(executable).replace(/\.exe$/i, '') === 'go' && ['version', 'env', 'telemetry'].includes(args[0]);
  const nodeOnly = path.resolve(executable) === path.resolve(process.execPath);
  if (!toolchainOnly && !nodeOnly) verifyWailsPatch(root);
  const childEnvironment = privateToolEnvironment({ ...process.env, GOTOOLCHAIN: 'local', GOFLAGS: `${mutableGo ? '' : '-mod=readonly '}-tags=${privacyTags}`, ...env });
  for (const name of Object.keys(childEnvironment)) if (name.toLowerCase() === 'path') delete childEnvironment[name];
  childEnvironment[process.platform === 'win32' ? 'Path' : 'PATH'] = [
    toolDirectory, path.dirname(process.execPath), process.env.PATH ?? process.env.Path ?? '',
  ].join(path.delimiter);
  const result = spawnSync(executable, args, {
    cwd, shell: false, encoding: 'utf8', windowsHide: true, timeout,
    env: childEnvironment, stdio: capture ? 'pipe' : 'inherit', maxBuffer: 32 * 1024 * 1024,
  });
  if (result.error) throw new Error(`${path.basename(executable)}: ${result.error.message}`);
  if (!acceptedStatuses.includes(result.status)) {
    // Do not echo child output into exception strings; dependencies may print secrets.
    throw Object.assign(new Error(`${path.basename(executable)} failed (exit ${result.status ?? 'unknown'}). Review its output locally.`), { exitCode: result.status });
  }
  return result.stdout?.trim() ?? '';
}

function npm(args, options) {
  const cli = process.env.npm_execpath
    ?? path.join(path.dirname(process.execPath), 'node_modules/npm/bin/npm-cli.js');
  if (!existsSync(cli)) throw new Error('Cannot locate npm CLI. Run this command through npm run.');
  return run(process.execPath, [cli, ...args], options);
}

async function frozenNpmInstall(options = {}) {
  // Fresh files can still be held briefly by Windows indexers/antivirus after a
  // previous npm process exits. Retry only the two observed filesystem errors;
  // registry, integrity, dependency, audit and other failures remain failures.
  const transientLocks = new Set([-4082, -4051, 4294963214, 4294963245]);
  for (let attempt = 0; attempt < 3; attempt++) {
    try { npm(['ci', '--ignore-scripts'], options); return; }
    catch (error) {
      if (process.platform !== 'win32' || !transientLocks.has(error.exitCode) || attempt === 2) throw error;
      console.log('Windows dependency files are temporarily locked; retrying the frozen install.');
      await delay(2000 * (attempt + 1));
    }
  }
}

function requireGo() {
  // GOTELEMETRY is a read-only report, not an effective environment opt-out.
  // Use Go's supported persistent control before any other Go/tool command.
  run('go', ['telemetry', 'off'], { capture: true });
  const telemetry = run('go', ['telemetry'], { capture: true });
  if (telemetry !== 'off') throw new Error('Go local telemetry collection and upload must both be off');
  const output = run('go', ['version'], { capture: true });
  const version = /\bgo(\d+\.\d+\.\d+)\b/.exec(output)?.[1];
  if (!version || !atLeast(version, policy.minimumGo)) {
    throw new Error(`Go ${policy.minimumGo} or a newer supported patched release is required. Install from https://go.dev/dl/`);
  }
  return version;
}

async function getJson(url) {
  const response = await fetch(url, {
    signal: AbortSignal.timeout(30000),
    headers: { accept: 'application/json', 'user-agent': 'WhatsUp-development-workflow' }, redirect: 'error',
  });
  if (!response.ok) throw new Error(`Dependency service returned HTTP ${response.status}: ${new URL(url).hostname}`);
  return response.json();
}

function cliPath(name) {
  return path.join(toolDirectory, `${name}${process.platform === 'win32' ? '.exe' : ''}`);
}

function requirePinnedTool(name, module, version) {
  parseVersion(version);
  const filename = cliPath(name);
  if (!existsSync(filename)) throw new Error(`Pinned ${name} missing for ${process.platform}-${process.arch}; run npm run setup in this platform's Node environment`);
  const info = run('go', ['version', '-m', filename], { capture: true });
  const graph = run('go', ['list', '-m', '-f', '{{.Path}} {{.Version}}{{with .Replace}} => {{.Path}} {{.Version}}{{end}}', 'all'], { capture: true, timeout: 300000 });
  assertToolBuildInfo(info, module, version, graph, policy.minimumGo, patchApprovals());
  return filename;
}

function patchApprovals() {
  return [verifyTailcatPatch(root, policy.reviewedGoPins), verifyWailsPatch(root)];
}

function patchEvidence() {
  return patchApprovals().map(({ module, version, patchVersion, manifestSHA256, treeSHA256 }) => ({ module, version, patchVersion, manifestSHA256, ...(treeSHA256 ? { treeSHA256 } : {}) }));
}

function materializeWails() {
  const goRoot = run('go', ['env', 'GOROOT'], { capture: true });
  return ensureWailsPatch(root, { executable: path.join(goRoot, 'bin', process.platform === 'win32' ? 'go.exe' : 'go'),
    env: privateToolEnvironment({ ...process.env, GOTOOLCHAIN: 'local' }) });
}

function wailsPath() {
  return requirePinnedTool('wails3', 'github.com/wailsapp/wails/v3', policy.wailsVersion);
}

function requireNativePlatform() {
  if (!['win32', 'linux'].includes(process.platform)) throw new Error('WhatsUp currently supports native Windows and Linux development only');
  const target = process.env.GOOS;
  if (target && target !== (process.platform === 'win32' ? 'windows' : 'linux')) {
    throw new Error('Use the native platform development environment; cross-platform packaging is not configured');
  }
  const nativeArch = process.arch === 'x64' ? 'amd64' : process.arch;
  if (process.env.GOARCH && process.env.GOARCH !== nativeArch) {
    throw new Error('Cross-architecture builds are not configured; use the native platform architecture');
  }
}

async function doctor() {
  const report = {
    recordedAt: new Date().toISOString(), platform: process.platform, arch: process.arch,
    node: { version: process.versions.node, executable: process.execPath, minimum: policy.minimumNode,
      ok: atLeast(process.versions.node, policy.minimumNode) },
    go: { ok: false, minimum: policy.minimumGo },
    applicationReady: policy.applicationReady, transportIntegrationVerified: policy.transportIntegrationVerified === true,
    wailsPinned: policy.wailsVersion, platformTools: toolDirectory,
  };
  try { report.go = { ...report.go, ok: true, version: requireGo(), root: run('go', ['env', 'GOROOT'], { capture: true }), telemetry: run('go', ['telemetry'], { capture: true }) }; }
  catch (error) { report.go.error = error.message; }
  try { const patch = verifyWailsPatch(root); report.wailsPatch = { ok: true, patchVersion: patch.patchVersion, treeSHA256: patch.treeSHA256 }; }
  catch (error) { report.wailsPatch = { ok: false, error: error.message }; }
  try { await requireApp(root); report.appSources = true; }
  catch { report.appSources = false; }
  if (flags.includes('--network')) {
    report.network = await Promise.all([
      ['npm registry', 'https://registry.npmjs.org/svelte/latest'],
      ['Go module proxy', 'https://proxy.golang.org/github.com/tailscale/tailcat/@latest'],
      ['GitHub releases', 'https://api.github.com/repos/wailsapp/wails/releases/latest'],
    ].map(async ([service, url]) => {
      try { await getJson(url); return { service, ok: true }; }
      catch (error) { return { service, ok: false, error: error.cause?.code ?? error.message }; }
    }));
  }
  const directory = path.join(root, '.local');
  await mkdir(directory, { recursive: true });
  await writeJson(path.join(directory, `doctor-${process.platform}-${process.arch}.json`), report);
  console.log(JSON.stringify(report, null, 2));
  if (!report.node.ok || !report.go.ok || !report.wailsPatch.ok || report.network?.some((x) => !x.ok)) process.exitCode = 1;
}

async function toolingTest() {
  const files = (await readdir(path.join(root, 'scripts/tests')))
    .filter((x) => x.endsWith('.test.mjs')).sort().map((x) => path.join(root, 'scripts/tests', x));
  if (!files.length) throw new Error('Workflow tests are missing');
  run(process.execPath, ['--test', ...files]);
}

async function installTool(module, version, name) {
  parseVersion(version);
  await mkdir(toolDirectory, { recursive: true });
  // Go tool directives pin the CLI AND its transitive graph in go.mod/go.sum.
  // Installing module@version would bypass patched dependencies selected there.
  const officialTools = prepareOfficialToolModule(root);
  const lockedMod = await readFile(path.join(officialTools, 'go.mod'));
  const lockedSum = await readFile(path.join(officialTools, 'go.sum'));
  run('go', ['mod', 'download'], { cwd: officialTools, env: { GOWORK: 'off' }, timeout: 300000 });
  run('go', ['mod', 'verify'], { cwd: officialTools, env: { GOWORK: 'off' } });
  run('go', ['install', module], { cwd: officialTools, env: { GOBIN: toolDirectory, GOWORK: 'off' }, timeout: 600000 });
  if (!lockedMod.equals(await readFile(path.join(officialTools, 'go.mod'))) || !lockedSum.equals(await readFile(path.join(officialTools, 'go.sum')))) {
    throw new Error('Official CLI compilation changed the mechanically derived frozen graph');
  }
  const packageModule = name === 'wails3' ? 'github.com/wailsapp/wails/v3' : 'golang.org/x/vuln';
  requirePinnedTool(name, packageModule, version);
}

function assertTransportPins() {
  validateReviewedGoPins(policy.reviewedGoPins);
  const graph = run('go', ['list', '-m', '-f', '{{.Path}} {{.Version}}{{with .Replace}} => {{.Path}} {{.Version}}{{end}}', 'all'], { capture: true, timeout: 300000 });
  assertReviewedGoGraph(graph, policy.reviewedGoPins, patchApprovals());
}

async function frozenInstall() {
  materializeWails();
  const lockFiles = ['package-lock.json', 'go.mod', 'go.sum', 'frontend/package.json', 'frontend/package-lock.json'];
  const before = await Promise.all(lockFiles.map((file) => readFile(path.join(root, file))));
  await frozenNpmInstall();
  await frozenNpmInstall({ cwd: frontendDirectory, timeout: 300000 });
  run('go', ['mod', 'download'], { timeout: 300000 });
  run('go', ['mod', 'verify']);
  assertTransportPins();
  for (let index = 0; index < lockFiles.length; index++) {
    const after = await readFile(path.join(root, lockFiles[index]));
    if (!before[index].equals(after)) throw new Error(`Frozen install changed ${lockFiles[index]}; resolve the graph during npm run update`);
  }
}

async function setup() {
  await requireApp(root);
  requireNativePlatform();
  requireGo();
  if (!policy.govulncheckVersion) throw new Error('Pin a reviewed current govulncheckVersion before installing app tools');
  await frozenInstall();
  await installTool('github.com/wailsapp/wails/v3/cmd/wails3', policy.wailsVersion, 'wails3');
  await installTool('golang.org/x/vuln/cmd/govulncheck', policy.govulncheckVersion, 'govulncheck');
  await installPackagingTools({ root, run });
  const playwright = path.join(frontendDirectory, 'node_modules/@playwright/test/cli.js');
  if (!existsSync(playwright)) throw new Error('The committed frontend graph must include the pinned Playwright test CLI');
  // Explicitly reviewed browser installation, not arbitrary npm lifecycle scripts.
  run(process.execPath, [playwright, 'install', 'chromium'], { cwd: frontendDirectory, timeout: 600000 });
  console.log('Frozen dependencies, pinned native/packaging/SBOM tools and pinned Playwright Chromium installed. Npm lifecycle scripts remained disabled.');
}

async function npmAdvisoryScan(frontend) {
  const cwd = frontend ? frontendDirectory : root;
  const raw = npm(['audit', '--json', '--audit-level=low'], { cwd, capture: true, acceptedStatuses: [0, 1], timeout: 120000 });
  let report;
  try { report = JSON.parse(raw); } catch { throw new Error('npm audit did not return valid JSON'); }
  await mkdir(path.join(root, '.local'), { recursive: true });
  await writeJson(path.join(root, '.local', `npm-audit-${frontend ? 'frontend' : 'root'}-${process.platform}-${process.arch}.json`), report);
  const result = evaluateNpmAudit(report, { frontend, exception: await readJson(path.join(root, 'config/advisory-exception.json')),
    lock: await readJson(path.join(cwd, 'package-lock.json')) });
  if (result.exceptions.length) console.log(`Reviewed frontend tooling advisory exception: ${result.exceptions[0].advisory}; expires ${result.exceptions[0].expiresOn}. Raw audit retained locally.`);
  return result;
}

async function frontendScript(name) {
  const manifest = await readJson(path.join(frontendDirectory, 'package.json'));
  requireFrontendScripts(manifest, [name]);
  npm(['run', name], { cwd: frontendDirectory, timeout: name === 'dev' ? 0 : 600000 });
}

async function checkGoFormatting() {
  const files = await collectSourceFiles(root, '.go');
  if (!files.length || !files.some((file) => file.endsWith('_test.go'))) throw new Error('Go application source and meaningful unit tests are required');
  for (let offset = 0; offset < files.length; offset += 50) {
    if (run('gofmt', ['-l', ...files.slice(offset, offset + 50)], { capture: true })) {
      throw new Error('Go formatting check failed; run gofmt on the changed application sources');
    }
  }
}

function raceAvailable() {
  if (run('go', ['env', 'CGO_ENABLED'], { capture: true }) !== '1') return false;
  const compiler = run('go', ['env', 'CC'], { capture: true });
  // Never invoke a shell to interpret a multiword CC string.
  if (!compiler || compiler.includes(' ')) return false;
  const result = spawnSync(compiler, ['--version'], { shell: false, windowsHide: true, stdio: 'ignore', timeout: 10000 });
  return !result.error && result.status === 0;
}

async function implementedCheck() {
  // Optional provenance keeps source-only/no-Git component checks available.
  // Development snapshots claim matching evidence only when this digest exists.
  let sourceBefore = null;
  try { sourceBefore = await captureReviewSource(root); } catch { /* no verified source metadata */ }
  await requireApp(root);
  requireNativePlatform();
  requireGo();
  materializeWails();
  const scanner = requirePinnedTool('govulncheck', 'golang.org/x/vuln', policy.govulncheckVersion);
  const wails = wailsPath();
  const manifest = await readJson(path.join(frontendDirectory, 'package.json'));
  requireFrontendScripts(manifest, ['format:check', 'check', 'test', 'test:e2e', 'build']);
  if (!(await collectSourceFiles(path.join(frontendDirectory, 'src'), '.test.ts')).length) throw new Error('Frontend component/domain unit tests are required');
  await frozenInstall();
  await toolingTest();
  await checkGoFormatting();
  await generateBindings();
  // Build real frontend assets before loading the root Go package's embed.
  for (const script of ['format:check', 'check', 'test', 'test:e2e', 'build']) await frontendScript(script);
  run('go', ['vet', './...'], { timeout: 300000 });
  const race = raceAvailable();
  if (!race && process.platform !== 'win32') throw new Error('Linux application tests require CGO and a compatible C compiler for the race detector');
  run('go', ['test', '-mod=readonly', ...(race ? ['-race'] : []), '-count=1', ...goTestFlags, './...'], { timeout: goTestRunnerTimeout });
  // The reviewed replacement is a nested module, so ./... does not include its
  // narrowly scoped offline regression tests. Compile it with this locked graph.
  run('go', ['test', '-mod=readonly', ...(race ? ['-race'] : []), '-count=1', ...goTestFlags, 'github.com/tailscale/tailcat'], { timeout: goTestRunnerTimeout });
  run('go', ['test', '-mod=readonly', ...(race ? ['-race'] : []), '-count=1', ...goTestFlags, '-run', '^TestWhatsUp',
    'github.com/wailsapp/wails/v3/pkg/application', 'github.com/wailsapp/wails/v3/internal/assetserver/webview',
    ...(process.platform === 'win32' ? ['github.com/wailsapp/wails/v3/internal/webview2/pkg/edge'] : [])], { timeout: goTestRunnerTimeout });
  if (!race) console.log('Windows Go unit tests passed WITHOUT the race detector: no configured CGO C compiler. Ubuntu race validation remains required.');
  const scans = [];
  for (const [name, scan] of [
    ['root npm audit', () => npmAdvisoryScan(false)],
    ['frontend npm audit', () => npmAdvisoryScan(true)],
    ['govulncheck', () => run(scanner, ['-test', './...'], { timeout: 600000 })],
    ['Wails CLI binary', () => run(scanner, ['-mode=binary', wails], { timeout: 600000 })],
    ['govulncheck binary', () => run(scanner, ['-mode=binary', scanner], { timeout: 600000 })],
  ]) {
    try { const result = await scan(); scans.push({ name, passed: true, ...(result?.exceptions ? { exceptions: result.exceptions } : {}) }); }
    catch (error) { scans.push({ name, passed: false, error: error.message }); }
  }
  try {
    const replacements = await replacementAdvisories(patchApprovals());
    for (const entry of replacements) scans.push({ name: `${entry.module} upstream version advisories`, ...entry });
  } catch (error) {
    for (const approval of patchApprovals()) scans.push({ name: `${approval.module} upstream version advisories`, module: approval.module, version: approval.version, passed: false, error: error.message });
  }
  assertTransportPins();
  let sourceAfter = null;
  try { sourceAfter = await captureReviewSource(root); } catch { /* evidence remains unbound */ }
  if (sourceBefore && (!sourceAfter || sourceAfter.sourceDigest !== sourceBefore.sourceDigest || sourceAfter.sourceCommit !== sourceBefore.sourceCommit)) {
    throw new Error('Source changed during component checks; rerun before relying on the result');
  }
  const report = path.join(root, '.local', `validation-${process.platform}-${process.arch}.json`);
  await writeJson(report, {
    recordedAt: new Date().toISOString(), platform: process.platform, arch: process.arch,
    scope: 'implemented components only', functionalChecksPassed: true, goRaceEnabled: race,
    source: sourceBefore && sourceAfter ? { commit: sourceAfter.sourceCommit, dirty: sourceAfter.sourceDirty, digestSHA256: sourceAfter.sourceDigest } : null,
    scans, patches: patchEvidence(), applicationReady: false, transportIntegrationVerified: false,
  });
  if (scans.some((scan) => !scan.passed)) throw new Error(`Security scans failed: ${scans.filter((scan) => !scan.passed).map((scan) => scan.name).join(', ')}. Only the exact documented unexpired development advisory can be excepted. Local report: ${report}`);
  console.log('Implemented-component checks completed. These do not establish native two-machine messaging, complete transport security verification, or release readiness.');
}

async function applicationCheck() {
  await requireApp(root);
  await requireFullReadiness(root, policy);
  // Full checks/package require current provenance before doing expensive work.
  // A stale derived snapshot must not spend minutes testing without evidence.
  const sourceBefore = await captureReviewSource(root);
  await implementedCheck();
  // Native OS credential/tray/playback acceptance is recorded separately. CI
  // runners have no interactive Secret Service session or graphical desktop.
  const race = raceAvailable();
  if (!race && process.platform !== 'win32') throw new Error('Linux preview checks require the race detector');
  run('go', ['test', '-mod=readonly', ...(race ? ['-race'] : []), '-count=1', ...goTestFlags, `-tags=integration,${privacyTags}`, './tests/integration/...'], { timeout: goTestRunnerTimeout });
  const nativeBinary = await nativeBuild({ staged: true });
  const source = await captureReviewSource(root);
  const report = await readJson(path.join(root, '.local', `validation-${process.platform}-${process.arch}.json`));
  if (sourceBefore.sourceDigest !== source.sourceDigest || sourceBefore.sourceCommit !== source.sourceCommit
      || !report.functionalChecksPassed || !validSecurityScans(report.scans) || !report.scans.every((scan) => scan.passed)
      || report.source?.digestSHA256 !== source.sourceDigest) throw new Error('The completed checks do not match the native build source');
  await writeJson(path.join(root, '.local', `check-${process.platform}-${process.arch}.json`), {
    recordedAt: new Date().toISOString(), source, functionalChecksPassed: true, integrationChecksPassed: true,
    nativeBuildPassed: true, goRaceEnabled: race, scans: report.scans, unsignedPreview: true,
    nativeBinary, nativeBinarySHA256: createHash('sha256').update(await readFile(nativeBinary)).digest('hex'),
    nativeUIAcceptance: 'not-run-by-this-command', authenticode: false, inAppUpdatesEnabled: false,
  });
  console.log('Unsigned-preview checks and native build passed. Native desktop acceptance and Authenticode remain separate; in-app updates stay disabled.');
}

async function credentialCheck() {
  await requireApp(root);
  requireNativePlatform();
  requireGo();
  const race = raceAvailable();
  run('go', ['test', '-mod=readonly', ...(race ? ['-race'] : []), '-count=1',
    `-tags=oscredentials,${privacyTags}`, '-run=^TestRealOSCredential', '-v', './tests/integration'], { timeout: 120000 });
  console.log('Real OS credential persistence, app passphrase and lost-key recovery passed with disposable validation credentials. No production identity was used.');
}

async function wslCheck() {
  if (process.platform !== 'win32') throw new Error('The WSL bridge runs from the authoritative Windows workspace');
  const requested = flags[0] ?? 'doctor';
  const task = ({ 'test:app': 'test-app', 'test:credentials': 'test-credentials', 'test:native': 'native-check', 'build:review': 'build-review', test: 'tooling-test' })[requested] ?? requested;
  if (!['doctor', 'setup', 'tooling-test', 'test-app', 'test-credentials', 'native-check', 'build', 'build-review', 'check', 'package'].includes(task)) {
    throw new Error('WSL supports doctor, setup, test, test:app, test:credentials, test:native, build or build:review; use Ubuntu Desktop for native UI testing');
  }
  if (['build-review', 'test-app', 'check', 'package'].includes(task)) {
    await mkdir(path.join(root, '.local'), { recursive: true });
    await writeJson(path.join(root, '.local/review-source.json'), await captureReviewSource(root));
  }
  const linuxRoot = run('wsl.exe', ['-d', 'Ubuntu', '--exec', 'wslpath', '-a', root.replaceAll('\\', '/')], { capture: true });
  if (!linuxRoot.startsWith('/')) throw new Error('Cannot resolve the Windows source directory in WSL');
  const workspaceID = createHash('sha256').update(root.toLowerCase()).digest('hex').slice(0, 16);
  run('wsl.exe', ['-d', 'Ubuntu', '--exec', 'bash', `${linuxRoot}/scripts/wsl-run.sh`,
    linuxRoot, workspaceID, task, ...flags.slice(1)], { timeout: 0 });
}

function cleanGitCommit() {
  try {
    const commit = run('git', ['rev-parse', '--verify', 'HEAD'], { capture: true });
    if (!/^[0-9a-f]{40,64}$/.test(commit)) throw new Error('missing commit');
    if (run('git', ['status', '--porcelain'], { capture: true })) throw new Error('dirty checkout');
    return commit;
  } catch {
    throw new Error('This command requires an existing clean Git checkout with a commit. Save/review the current work and initialize Git deliberately; the workflow will not initialize, commit, reset, or discard files.');
  }
}

async function update() {
  await requireApp(root);
  requireGo();
  cleanGitCommit();
  validateReviewedGoPins(policy.reviewedGoPins);
  const latestNode = latestLtsNode(await getJson('https://nodejs.org/dist/index.json'));
  assertNodeRuntime(process.versions.node, latestNode);
  const nodeMajor = parseVersion(latestNode)[0];
  const nodeEngine = `>=${latestNode} <${nodeMajor + 1}`;
  policy.frontendMajorConstraints ??= {};
  policy.frontendMajorConstraints['@types/node'] = nodeMajor;
  const releases = await getJson('https://go.dev/dl/?mode=json');
  const latestGo = releases.find((entry) => entry.stable)?.version?.replace(/^go/, '');
  if (!latestGo) throw new Error('Could not verify the latest stable Go release');
  parseVersion(latestGo);
  if (!atLeast(requireGo(), latestGo)) throw new Error(`Install current patched Go ${latestGo} before updating`);
  const wails = await getJson('https://api.github.com/repos/wailsapp/wails/releases/latest');
  parseVersion(wails.tag_name);
  const scanner = await getJson('https://proxy.golang.org/golang.org/x/vuln/@latest');
  parseVersion(scanner.Version);
  const frontendFile = path.join(frontendDirectory, 'package.json');
  const frontend = await readJson(frontendFile);
  const candidates = [];
  for (const group of ['dependencies', 'devDependencies', 'optionalDependencies']) {
    for (const name of Object.keys(frontend[group] ?? {}).sort()) {
      validatePackageName(name);
      const metadata = await getJson(`https://registry.npmjs.org/${encodeURIComponent(name)}`);
      const constraint = policy.frontendMajorConstraints?.[name];
      if (constraint !== undefined && (!Number.isInteger(constraint) || constraint < 1)) throw new Error(`Invalid reviewed major constraint: ${name}`);
      const version = name === '@wailsio/runtime' ? wails.tag_name.slice(1) : stableNpmVersion(metadata, constraint);
      if (!metadata.versions?.[version]) throw new Error(`Matching runtime/dependency version is not published: ${name}`);
      candidates.push({ name, group, version });
    }
  }
  // Complete discovery before edits. Any subsequent failure leaves reviewable work.
  for (const candidate of candidates) frontend[candidate.group][candidate.name] = candidate.version;
  frontend.engines = { ...frontend.engines, node: nodeEngine };
  await writeJson(frontendFile, frontend);
  const workspaceFile = path.join(root, 'package.json');
  const workspace = await readJson(workspaceFile);
  workspace.engines = { ...workspace.engines, node: nodeEngine };
  await writeJson(workspaceFile, workspace);
  await writeFile(path.join(root, '.nvmrc'), `${latestNode}\n`, 'utf8');
  policy.minimumNode = latestNode;
  policy.minimumGo = latestGo;
  policy.wailsVersion = wails.tag_name;
  policy.govulncheckVersion = scanner.Version;
  policy.observedOn = new Date().toISOString().slice(0, 10);
  policy.observedTimezone = 'UTC';
  await writeJson(configFile, policy);
  npm(['install', '--package-lock-only', '--ignore-scripts'], { timeout: 300000 });
  npm(['install', '--package-lock-only', '--ignore-scripts'], { cwd: frontendDirectory, timeout: 300000 });
  run('go', ['mod', 'edit', `-go=${latestGo}`], { mutableGo: true });
  run('go', ['get', '-u', './...'], { timeout: 600000, mutableGo: true });
  run('go', ['get', `github.com/wailsapp/wails/v3@${policy.wailsVersion}`,
    ...Object.entries(policy.reviewedGoPins).map(([module, version]) => `${module}@${version}`)], { timeout: 600000, mutableGo: true });
  run('go', ['get', '-tool', `github.com/wailsapp/wails/v3/cmd/wails3@${policy.wailsVersion}`,
    `golang.org/x/vuln/cmd/govulncheck@${policy.govulncheckVersion}`], { timeout: 600000, mutableGo: true });
  const updateGoRoot = run('go', ['env', 'GOROOT'], { capture: true });
  run('go', ['mod', 'tidy'], { timeout: 300000, mutableGo: true });
  retainReplacementSums(root, policy.reviewedGoPins, { executable: path.join(updateGoRoot, 'bin', process.platform === 'win32' ? 'go.exe' : 'go'), env: privateToolEnvironment(process.env) });
  assertTransportPins();
  await setup();
  await applicationCheck();
  console.log('Dependency candidates passed full checks. Review the diff and copied component sources before committing. No files were committed, pushed, published, or deployed.');
}

async function generateBindings() {
  await requireApp(root);
  requireGo();
  const dist = path.join(frontendDirectory, 'dist');
  const marker = path.join(dist, '.whatsup-bindings-bootstrap');
  let ownsMarker = false;
  if (!existsSync(path.join(dist, 'index.html'))) {
    await mkdir(dist, { recursive: true });
    // Go embed analysis needs a nonempty directory before Vite's first build.
    // This ignored empty marker is never an application asset or passing build.
    if (!within(dist, marker)) throw new Error('Unsafe bindings bootstrap path');
    if (existsSync(marker)) throw new Error('Remove the stale ignored bindings-bootstrap marker before retrying');
    await writeFile(marker, '', { flag: 'wx' });
    ownsMarker = true;
  }
  try {
    run(wailsPath(), ['generate', 'bindings', '-f', `-mod=readonly -tags=${privacyTags}`, '-clean=true', '-ts', '-i'], { timeout: 300000 });
    // Keep generated public bindings reproducible across platform checkouts and
    // the upstream generator's optional blank EOF line. Preserve their content.
    for (const filename of await collectSourceFiles(path.join(frontendDirectory, 'bindings'), '.ts')) {
      const before = await readFile(filename, 'utf8');
      const normalized = before.replaceAll('\r\n', '\n').trimEnd() + '\n';
      if (normalized !== before) await writeFile(filename, normalized);
    }
  } finally {
    if (ownsMarker) await rm(marker, { force: true });
  }
}

async function nativeBuild({ staged = false } = {}) {
  await requireApp(root);
  requireNativePlatform();
  requireGo();
  const cli = wailsPath();
  // The dev entry point installs before starting Vite. Hot reload must not
  // delete/reinstall modules under that running server. Production candidates
  // still perform a complete frozen install on every build.
  if (process.env.WHATSUP_DEV === 'true') {
    if (!existsSync(path.join(frontendDirectory, 'node_modules/.package-lock.json'))) throw new Error('Development dependencies are missing; run npm run setup before starting dev');
    run('go', ['mod', 'verify']);
    assertTransportPins();
  } else {
    await frozenInstall();
  }
  await generateBindings();
  await frontendScript(process.env.WHATSUP_DEV === 'true' ? 'build:dev' : 'build');
  if (!existsSync(path.join(frontendDirectory, 'dist/index.html'))) throw new Error('The frontend build did not produce real bundled application assets');
  const outputDirectory = staged ? path.join(root, '.local/native-package', `${process.platform}-${process.arch}`) : path.join(root, 'build/bin');
  await mkdir(outputDirectory, { recursive: true });
  const windows = process.platform === 'win32';
  const output = path.join(outputDirectory, windows ? 'whatsup.exe' : 'whatsup');
  const syso = path.join(root, `wails_windows_${process.arch === 'x64' ? 'amd64' : process.arch}.syso`);
  if (windows) run(cli, ['generate', 'syso', '-arch', process.arch === 'x64' ? 'amd64' : process.arch,
    '-icon', 'build/windows/icon.ico', '-manifest', 'build/windows/wails.exe.manifest',
    '-info', 'build/windows/info.json', '-out', syso]);
  try {
    const production = process.env.WHATSUP_DEV !== 'true';
    run('go', ['build', '-mod=readonly', '-trimpath', '-buildvcs=false',
      '-tags', `${production ? 'production,' : ''}${privacyTags}`,
      ...(production ? ['-ldflags', windows ? '-s -w -H windowsgui' : '-s -w'] : []),
      '-o', output, '.'], { timeout: 600000, env: windows ? { CGO_ENABLED: '0' } : { CGO_ENABLED: '1' } });
  } finally {
    if (windows) await rm(syso, { force: true });
  }
  patchApprovals();
  console.log(`Native development candidate: ${output}. This unsigned build is not approved for distribution.`);
  return output;
}

async function build() {
  await requireApp(root);
  requireNativePlatform();
  requireGo();
  run(wailsPath(), ['build'], { timeout: 600000 });
}

async function buildNativeProbe() {
  requireNativePlatform();
  if (process.platform !== 'linux') throw new Error('The native WebKit smoke check is Linux-only');
  requireGo();
  await mkdir(toolDirectory, { recursive: true });
  const executable = path.join(toolDirectory, 'whatsup-webview-check');
  run('go', ['build', '-mod=readonly', '-trimpath', '-buildvcs=false', '-ldflags=-s -w',
    '-o', executable, './cmd/whatsup-webview-check'], { timeout: 300000, env: { CGO_ENABLED: '1' } });
  return executable;
}

async function nativeCheck() {
  const built = await buildNativeProbe();
  let executable = built;
  const installed = '/opt/whatsup-development/whatsup-webview-check';
  if (existsSync(installed)) {
    const digest = (bytes) => createHash('sha256').update(bytes).digest('hex');
    if (digest(await readFile(installed)) === digest(await readFile(built))) executable = installed;
  }
  // The installed path supplies the app-specific policy context on restricted
  // systems. Other developer hosts can use a disposable virtual display.
  if (process.env.WSL_DISTRO_NAME || (!process.env.DISPLAY && !process.env.WAYLAND_DISPLAY)) {
    run('xvfb-run', ['-a', executable], { timeout: 35000, env: { GDK_BACKEND: 'x11', WAYLAND_DISPLAY: '' } });
  } else run(executable, [], { timeout: 35000 });
  console.log('A real sandboxed WebKit process loaded local HTML and executed JavaScript. Visible GPU/UI and storage QA remain separate.');
}

async function buildReview() {
  if (flags.length && (flags.length !== 2 || flags[0] !== '--review-id')) throw new Error('Use build:review with optional --review-id <ID>');
  const source = await captureReviewSource(root);
  const version = (await readJson(path.join(frontendDirectory, 'package.json'))).version;
  await build();
  const probe = process.platform === 'linux' ? await buildNativeProbe() : null;
  const after = await captureReviewSource(root);
  if (after.sourceCommit !== source.sourceCommit || after.sourceDirty !== source.sourceDirty || after.sourceDigest !== source.sourceDigest) {
    throw new Error('Source changed during the development build; no review snapshot was archived');
  }
  let validation = {};
  try {
    const report = await readJson(path.join(root, '.local', `validation-${process.platform}-${process.arch}.json`));
    if (report.source?.digestSHA256 === source.sourceDigest && report.source.commit === source.sourceCommit
        && validSecurityScans(report.scans)) {
      validation = {
        componentChecks: report.functionalChecksPassed === true ? 'passed' : 'not-run-for-this-snapshot',
        advisoryGate: report.scans.every((entry) => entry.passed) ? 'passed' : 'failed',
      };
    }
  } catch { /* Do not infer validation from a missing or stale report. */ }
  const result = await archiveReviewBuild({ root, version, platform: process.platform, arch: process.arch,
    sourceCommit: source.sourceCommit, sourceDirty: source.sourceDirty, sourceDigest: source.sourceDigest,
    toolchain: { go: requireGo(), node: process.versions.node, wails: policy.wailsVersion }, patches: patchEvidence(),
    reviewID: flags[1], validation });
  let probeRelativeDirectory;
  if (probe) {
    const directory = path.join(path.dirname(result.directory), `native-check-linux-${process.arch}`);
    await mkdir(directory, { mode: 0o700 });
    const bytes = await readFile(probe);
    const sha256 = createHash('sha256').update(bytes).digest('hex');
    await writeFile(path.join(directory, 'whatsup-webview-check'), bytes, { flag: 'wx', mode: 0o700 });
    const manifest = { schemaVersion: 1, kind: 'linux-webview-check', version, reviewID: result.reviewID,
      sourceDigestSHA256: source.sourceDigest, files: [{ name: 'whatsup-webview-check', size: bytes.length, sha256 }] };
    const manifestBytes = JSON.stringify(manifest, null, 2) + '\n';
    await writeFile(path.join(directory, 'probe-manifest.json'), manifestBytes, { flag: 'wx', mode: 0o600 });
    await writeFile(path.join(directory, 'SHA256SUMS'), `${sha256}  whatsup-webview-check\n${createHash('sha256').update(manifestBytes).digest('hex')}  probe-manifest.json\n`, { flag: 'wx', mode: 0o600 });
    probeRelativeDirectory = path.relative(root, directory).split(path.sep).join('/');
  }
  await writeJson(path.join(root, '.local', `review-build-${process.platform}-${process.arch}.json`), {
    reviewID: result.reviewID, version, platform: process.platform, arch: process.arch,
    relativeDirectory: path.relative(root, result.directory).split(path.sep).join('/'),
    sourceDigest: source.sourceDigest, sha256: result.manifest.files[0].sha256,
    ...(probeRelativeDirectory ? { probeRelativeDirectory } : {}),
  });
  console.log(`Preserved development review build: ${result.directory}\nThis is an unsigned test executable; release/package gates remain closed.`);
}

async function nativePackage() {
  await requireFullReadiness(root, policy);
  requireNativePlatform();
  if (process.env.GITHUB_ACTIONS === 'true') cleanGitCommit();
  const source = await captureReviewSource(root);
  const receipt = await readJson(path.join(root, '.local', `check-${process.platform}-${process.arch}.json`));
  if (!receipt.functionalChecksPassed || !receipt.integrationChecksPassed || !receipt.nativeBuildPassed
      || !validSecurityScans(receipt.scans) || !receipt.scans.every((scan) => scan.passed)
      || receipt.source?.sourceDigest !== source.sourceDigest || receipt.source?.sourceCommit !== source.sourceCommit
      || !Number.isFinite(Date.parse(receipt.recordedAt)) || Date.parse(receipt.recordedAt) > Date.now()
      || Date.now() - Date.parse(receipt.recordedAt) > 2 * 60 * 60 * 1000
      || receipt.scans.some((scan) => scan.exceptions?.some((exception) => !Number.isFinite(Date.parse(exception.expiresOn)) || Date.now() >= Date.parse(exception.expiresOn)))) {
    throw new Error('Native packaging requires fresh source-bound checks; run npm run package');
  }
  const version = (await readJson(path.join(root, 'package.json'))).version;
  previewArtifactName(version);
  const nativeBinary = path.join(root, '.local/native-package', `${process.platform}-${process.arch}`, process.platform === 'win32' ? 'whatsup.exe' : 'whatsup');
  if (receipt.nativeBinary !== nativeBinary || receipt.nativeBinarySHA256 !== createHash('sha256').update(await readFile(nativeBinary)).digest('hex')) {
    throw new Error('The checked native package executable changed');
  }
  const destination = path.join(root, 'artifacts', version, `${process.platform}-${process.arch}`);
  if (!within(path.join(root, 'artifacts'), destination)) throw new Error('Artifact destination escapes the project');
  if (existsSync(destination)) throw new Error('This versioned package output already exists; preserve or set it aside before retrying');
  await mkdir(destination, { recursive: true });
  try {
    const toolScans = [];
    const upstreamAr = await arWriterAdvisories(root);
    await writeJson(path.join(root, '.local', `packaging-ar-upstream-${process.platform}-${process.arch}.json`), upstreamAr);
    const scanner = requirePinnedTool('govulncheck', 'golang.org/x/vuln', policy.govulncheckVersion);
    for (const name of ['cyclonedx-gomod', ...(process.platform === 'linux' ? ['nfpm'] : [])]) {
      const tool = await packagingTool({ root, name, run });
      run(scanner, ['-mode=binary', tool.executable], { timeout: 600000 });
      toolScans.push({ name, version: tool.version, binarySHA256: tool.binarySHA256, passed: true, upstreamAr });
    }
    if (process.platform === 'linux') await buildNativeProbe();
    const installer = await nativeInstaller({ root, destination, version, binary: nativeBinary, run });
    const sbom = await createPreviewSBOM({ root, destination, version, binary: nativeBinary, source, patches: patchEvidence(), run });
    const compiler = await packagingTool({ root, name: process.platform === 'win32' ? 'nsis' : 'nfpm', run });
    const manifest = {
      schemaVersion: 1, version, platform: process.platform, arch: process.arch,
      sourceCommit: source.sourceCommit, sourceDigestSHA256: source.sourceDigest, sourceDirty: source.sourceDirty,
      unsignedPreview: true, signed: false, authenticode: false, inAppUpdatesEnabled: false, releaseEligible: false,
      go: requireGo(), node: process.versions.node, wails: policy.wailsVersion,
      checks: { functional: 'passed', integration: 'passed', nativeBuild: 'passed', goRaceEnabled: receipt.goRaceEnabled,
        nativeUIAcceptance: 'not-run-by-this-command', recordedAt: receipt.recordedAt, scans: receipt.scans },
      patches: patchEvidence(), packagingToolScans: toolScans,
      packaging: { name: process.platform === 'win32' ? 'nsis' : 'nfpm', version: compiler.version, kind: compiler.kind,
        ...(compiler.kind === 'source' ? { lockSHA256: compiler.lockSHA256, binarySHA256: compiler.binarySHA256,
          compilerGo: compiler.compilerGo, scanPassed: true } : { archiveSHA256: compiler.archiveSHA256 }) }, sbom,
      files: [{ name: installer.name, size: installer.size, sha256: installer.sha256 }],
    };
    await writeJson(path.join(destination, 'build-manifest.json'), manifest);
    const checksumFiles = await Promise.all([installer.name, 'sbom.cdx.json', 'build-manifest.json'].map(async (name) => ({
      name, sha256: createHash('sha256').update(await readFile(path.join(destination, name))).digest('hex'),
    })));
    await writeFile(path.join(destination, 'SHA256SUMS'), checksumFiles.map(({ name, sha256 }) => `${sha256}  ${name}`).join('\n') + '\n', 'utf8');
    const after = await captureReviewSource(root);
    if (after.sourceDigest !== source.sourceDigest || after.sourceCommit !== source.sourceCommit) throw new Error('Source changed during packaging');
    console.log(`Unsigned preview installer, SBOM and SHA256SUMS: ${destination}. No upload, publication or unsigned updater was performed.`);
  } catch (error) {
    // Keep failed output for diagnosis, with no plausible successful manifest.
    await writeFile(path.join(destination, 'PACKAGE_FAILED'), 'Packaging failed. Do not distribute this directory.\n', 'utf8');
    throw error;
  }
}

async function packageApp() {
  await requireApp(root);
  await requireFullReadiness(root, policy);
  await applicationCheck();
  await nativePackage();
}

try {
  if (!commands.has(command)) throw new Error(`Unknown command: ${command}`);
  const allowedFlags = command === 'doctor' ? ['--network'] : [];
  if (!['wsl', 'build-review'].includes(command) && flags.some((flag) => !allowedFlags.includes(flag))) throw new Error('Unsupported command option');
  assertNodeRuntime(process.versions.node, policy.minimumNode);
  verifyTailcatPatch(root, policy.reviewedGoPins);
  verifyWailsDefinition(root);
  if (command === 'doctor') await doctor();
  if (command === 'tooling-test') await toolingTest();
  if (command === 'ci-scope') await runComponentScope(root);
  if (command === 'setup') await setup();
  if (command === 'dev') {
    await requireApp(root);
    requireNativePlatform();
    requireGo();
    await frozenInstall();
    run(wailsPath(), ['dev', '-config', 'build/config.yml'], { timeout: 0 });
  }
  if (command === 'test-app') await implementedCheck();
  if (command === 'test-credentials') await credentialCheck();
  if (command === 'native-check') await nativeCheck();
  if (command === 'wsl') await wslCheck();
  if (command === 'build') await build();
  if (command === 'build-review') await buildReview();
  if (command === 'check') await applicationCheck();
  if (command === 'update') await update();
  if (command === 'package') await packageApp();
  if (command === 'native-build') await nativeBuild();
  if (command === 'native-package') await nativePackage();
  if (command === 'generate-bindings') await generateBindings();
  if (command === 'frontend-build') await frontendScript('build');
  if (command === 'frontend-dev') {
    const manifest = await readJson(path.join(frontendDirectory, 'package.json'));
    requireFrontendScripts(manifest, ['dev']);
    npm(['run', 'dev', '--', '--port', process.env.WAILS_VITE_PORT ?? '9245', '--strictPort'], { cwd: frontendDirectory, timeout: 0 });
  }
  if (command === 'run-native') {
    requireNativePlatform();
    run(path.join(root, 'build/bin', process.platform === 'win32' ? 'whatsup.exe' : 'whatsup'), [], { timeout: 0 });
  }
  if (command === 'release-preflight') await releasePreflight(root);
  if (command === 'release-collect') await releaseCollect(root);
  if (command === 'release-publish') await releasePublish(root);
  if (command === 'verify-release') throw new Error('Release verification is unavailable until SBOM, platform signing, and signed-update verification are implemented. No release is approved.');
} catch (error) {
  console.error(`WhatsUp: ${error.message}`);
  process.exitCode = 1;
}
