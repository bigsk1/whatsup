import assert from 'node:assert/strict';
import test from 'node:test';
import { assertToolBuildInfo } from '../lib/workflow.mjs';

test('a latest CLI version cannot hide stale compiler or vulnerable transitive dependencies', () => {
  const graph = 'example.org/cli v3.1.0\ngithub.com/klauspost/compress v1.20.4';
  const info = 'tool: go1.27.4\n\tmod\texample.org/cli\tv3.1.0\th1:synthetic\n\tdep\tgithub.com/klauspost/compress\tv1.20.4\th1:synthetic';
  const check = (value) => assertToolBuildInfo(value, 'example.org/cli', 'v3.1.0', graph, '1.27.4');
  check(info);
  assert.throws(() => check(info.replace('go1.27.4', 'go1.27.3')), /outdated Go/);
  assert.throws(() => check(info.replace('v1.20.4', 'v1.18.7')), /differs from the locked graph/);
  assert.throws(() => check(info.replace('v3.1.0', 'v3.0.0')), /reviewed pin/);
  assert.throws(() => check(`${info}\n\t=>\t../unreviewed\t(devel)`), /replacement/);
  assert.throws(() => assertToolBuildInfo(info, 'example.org/cli', 'v3.1.0', `${graph} => ./unreviewed`, '1.27.4'), /selected graph replacement/);
});
