import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { parseVersion } from '../lib/workflow.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const text = (filename) => readFile(path.join(root, filename), 'utf8');
const json = async (filename) => JSON.parse(await text(filename));

test('one application version identifies the runtime, manifests and native metadata', async () => {
  const frontend = await json('frontend/package.json');
  const version = frontend.version;
  parseVersion(version);
  for (const filename of ['package.json', 'package-lock.json', 'frontend/package-lock.json']) {
    const manifest = await json(filename);
    assert.equal(manifest.version, version, `${filename} version drifted`);
    if (manifest.packages) assert.equal(manifest.packages[''].version, version, `${filename} root package drifted`);
  }

  const runtimeVersions = [
    ['internal/desktop/service.go', /const Version = "([^"]+)"/],
    ['frontend/src/lib/backend.ts', /version: '([^']+)'/],
    ['build/config.yml', /^  version: (.+)$/m],
    ['build/linux/nfpm/nfpm.yaml', /^version: (.+)$/m],
    ['build/windows/nsis/wails_tools.nsh', /!define INFO_PRODUCTVERSION "([^"]+)"/],
    ['build/windows/wails.exe.manifest', /<assemblyIdentity\b[^>]*\bversion="([^"]+)"/],
  ];
  for (const [filename, expression] of runtimeVersions) {
    assert.equal(expression.exec(await text(filename))?.[1], version, `${filename} version drifted`);
  }

  const windows = await json('build/windows/info.json');
  for (const value of [windows.fixed.file_version, windows.fixed.product_version,
    windows.info['0409'].FileVersion, windows.info['0409'].ProductVersion]) {
    assert.equal(value, version, 'Windows resource version drifted');
  }
  for (const filename of ['build/windows/msix/template.xml', 'build/windows/msix/app_manifest.xml']) {
    assert.equal(/\bVersion="([0-9.]+)"/.exec(await text(filename))?.[1], `${version}.0`, `${filename} version drifted`);
  }
});
