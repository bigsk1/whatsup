import assert from 'node:assert/strict';
import test from 'node:test';
import { assertNodeRuntime, latestLtsNode, privateToolEnvironment } from '../lib/workflow.mjs';

test('tool privacy defaults survive inherited environment casing and preserve unrelated settings', () => {
  const original = {
    NPM_CONFIG_UPDATE_NOTIFIER: 'true', npm_config_AUDIT: 'true', NPM_CONFIG_FUND: 'true',
    npm_config_registry: 'https://registry.npmjs.org/', GOSUMDB: 'sum.golang.org',
  };
  const result = privateToolEnvironment(original);
  assert.equal(result.NPM_CONFIG_UPDATE_NOTIFIER, undefined);
  assert.equal(result.npm_config_AUDIT, undefined);
  assert.equal(result.NPM_CONFIG_FUND, undefined);
  assert.equal(result.npm_config_update_notifier, 'false');
  assert.equal(result.npm_config_audit, 'false');
  assert.equal(result.npm_config_fund, 'false');
  assert.equal(result.npm_config_registry, original.npm_config_registry);
  assert.equal(result.GOSUMDB, original.GOSUMDB);
  assert.equal(original.NPM_CONFIG_UPDATE_NOTIFIER, 'true');
});

test('runtime updates select current stable LTS without drifting to alpha or an older support line', () => {
  assertNodeRuntime('26.13.1', '26.13.1');
  assert.throws(() => assertNodeRuntime('22.23.5', '26.13.1'), /Use NVM/);
  assert.throws(() => assertNodeRuntime('27.0.0', '26.13.1'), /same major/);
  assert.equal(latestLtsNode([
    { version: 'v27.0.0-alpha.1', lts: false },
    { version: 'v22.99.0', lts: 'Jod' },
    { version: 'v26.13.1', lts: 'Lithium' },
    { version: 'v26.13.0', lts: 'Lithium' },
    { version: 'v28.0.0-rc.1', lts: 'unreleased' },
  ]), '26.13.1');
  assert.throws(() => latestLtsNode([{ version: 'v27.0.0-alpha.1', lts: false }]), /stable Node LTS/);
});
