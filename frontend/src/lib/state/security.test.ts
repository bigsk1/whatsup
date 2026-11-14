import { describe, it, expect, vi } from 'vitest';
import { get } from 'svelte/store';
import {
  createSecurity,
  validSecret,
  validateRecoveryPreview,
  validateStorageSecurity,
  type SecurityBridge,
} from './security';
import { unavailableStatus } from '../backend';

const secret = '  synthetic 🔐 secret  ';
const handle = 'B'.repeat(32);
const preview = () => ({
  handle,
  contacts: 2,
  messages: 10001,
  attachments: 4,
  sourceRequiresPassphrase: true,
  replaceRequired: true,
  credentialResetRequired: false,
});
const status = () => ({
  ...unavailableStatus,
  storageState: 'unlocked',
  requiresPassphrase: true,
});
function api(): SecurityBridge {
  return {
    get: vi.fn(async () => ({
      requiresPassphrase: true,
      recoveryAvailable: true,
      busy: false,
    })),
    unlock: vi.fn(async () => status()),
    configure: vi.fn(async () => status()),
    export: vi.fn(async () => ({ saved: true })),
    stage: vi.fn(async () => preview()),
    commit: vi.fn(async () => status()),
    cancel: vi.fn(async () => {}),
  };
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => (resolve = done));
  return { promise, resolve };
}
function setup(bridge = api()) {
  const changed = vi.fn(),
    beforeImport = vi.fn();
  return {
    bridge,
    changed,
    beforeImport,
    store: createSecurity({ changed, beforeImport }, bridge),
  };
}
describe('security state', () => {
  it('uses exact valid UTF8 bytes and rejects unpaired UTF16 without trimming or normalization', () => {
    expect(validSecret('😀'.repeat(3))).toBe(true);
    expect(validSecret(' '.repeat(12))).toBe(true);
    expect(validSecret('e\u0301'.repeat(4))).toBe(true);
    expect(validSecret('a'.repeat(1024))).toBe(true);
    for (const value of [
      'short',
      'a'.repeat(1025),
      '\ud800' + 'x'.repeat(20),
      'x'.repeat(20) + '\udfff',
    ])
      expect(validSecret(value)).toBe(false);
  });
  it('passes exact secrets only to the typed RPC and keeps them out of observable state', async () => {
    const { store, bridge, changed } = setup();
    store.begin('unlock');
    await store.submit('', secret);
    expect(bridge.unlock).toHaveBeenCalledWith(secret);
    expect(changed).toHaveBeenCalledWith(status());
    expect(JSON.stringify(get(store.state))).not.toContain(secret);
    expect(get(store.state).operation).toBe('');
  });
  it('prompts only once on startup and cancellation stays offline until explicit Go online', () => {
    const { store } = setup();
    const locked = { ...status(), storageState: 'locked' };
    store.startup(locked);
    expect(get(store.state).operation).toBe('unlock');
    store.cancel();
    store.startup(locked);
    expect(get(store.state).operation).toBe('');
    store.begin('unlock');
    expect(get(store.state).operation).toBe('unlock');
    store.reset();
  });
  it('ignores a delayed unlock result after going offline and reopening the same profile', async () => {
    const bridge = api(),
      held = deferred<unknown>();
    bridge.unlock = vi.fn(() => held.promise);
    const { store, changed } = setup(bridge);
    store.begin('unlock');
    const running = store.submit('', secret);
    store.reset();
    store.begin('unlock');
    held.resolve(status());
    await running;
    expect(changed).not.toHaveBeenCalled();
    expect(get(store.state).operation).toBe('unlock');
    store.reset();
  });
  it('does not echo backend error text or submitted secrets', async () => {
    const bridge = api();
    bridge.unlock = vi.fn(async () => {
      throw new Error(secret);
    });
    const { store } = setup(bridge);
    store.begin('unlock');
    await store.submit('', secret);
    expect(get(store.state).notice).toContain('not accepted');
    expect(JSON.stringify(get(store.state))).not.toContain(secret);
    store.reset();
  });
  it('cancels authenticated import handles delivered after cancellation', async () => {
    const bridge = api(),
      held = deferred<unknown>();
    bridge.stage = vi.fn(() => held.promise);
    const { store } = setup(bridge);
    store.begin('import');
    const running = store.submit('', secret);
    store.reset();
    held.resolve(preview());
    await running;
    expect(bridge.cancel).toHaveBeenCalledWith(handle);
    expect(get(store.state).preview).toBeNull();
  });
  it('requires explicit replacement including a missing credential, then installs only an opaque handle', async () => {
    const bridge = api();
    bridge.stage = vi.fn(async () => ({
      ...preview(),
      replaceRequired: false,
      credentialResetRequired: true,
    }));
    const { store, changed, beforeImport } = setup(bridge);
    store.begin('import');
    await store.submit('', secret);
    await store.commit('', false, false);
    expect(bridge.commit).not.toHaveBeenCalled();
    await store.commit(secret, true, true);
    expect(bridge.commit).toHaveBeenCalledWith(handle, secret, true, true);
    expect(beforeImport).toHaveBeenCalledOnce();
    expect(changed).toHaveBeenCalledWith(status());
    expect(get(store.state).preview).toBeNull();
  });
  it('retains no preview after cancel and rejects paths or malformed summaries', async () => {
    const { store, bridge } = setup();
    store.begin('import');
    await store.submit('', secret);
    store.cancel();
    expect(bridge.cancel).toHaveBeenCalledWith(handle);
    expect(
      validateStorageSecurity({
        requiresPassphrase: false,
        recoveryAvailable: true,
        busy: false,
      }).recoveryAvailable,
    ).toBe(true);
    for (const update of [
      { handle: 'C:/private.copy' },
      { contacts: 33 },
      { messages: -1 },
      { sourceRequiresPassphrase: 'yes' },
      { path: '/private.copy' },
    ])
      expect(() =>
        validateRecoveryPreview({ ...preview(), ...update }),
      ).toThrow();
  });
  it('rechecks authoritative status after an uncertain mode change and never echoes errors', async () => {
    const bridge = api(),
      failed = vi.fn();
    bridge.configure = vi.fn(async () => {
      throw new Error(secret);
    });
    const store = createSecurity(
      { changed: vi.fn(), beforeImport: vi.fn(), failed },
      bridge,
    );
    store.begin('disable');
    await store.submit(secret, '');
    expect(failed).toHaveBeenCalledOnce();
    expect(get(store.state).operation).toBe('');
    expect(get(store.state).busy).toBe(false);
    expect(JSON.stringify(get(store.state))).not.toContain(secret);
  });
  it('cancels an import preview on native offline and ignores its delayed commit result', async () => {
    const bridge = api(),
      held = deferred<unknown>();
    bridge.commit = vi.fn(() => held.promise);
    const { store, changed } = setup(bridge);
    store.begin('import');
    await store.submit('', secret);
    const saving = store.commit('', false, true);
    store.reset();
    held.resolve(status());
    await saving;
    expect(changed).not.toHaveBeenCalled();
    expect(get(store.state).preview).toBeNull();
    expect(bridge.cancel).toHaveBeenCalledWith(handle);
  });
  it('keeps the newest authoritative settings response when concurrent loads finish out of order', async () => {
    const bridge = api(),
      old = deferred<unknown>();
    bridge.get = vi
      .fn()
      .mockReturnValueOnce(old.promise)
      .mockResolvedValueOnce({
        requiresPassphrase: false,
        recoveryAvailable: true,
        busy: false,
      });
    const { store } = setup(bridge);
    const first = store.load();
    await store.load();
    old.resolve({
      requiresPassphrase: true,
      recoveryAvailable: true,
      busy: true,
    });
    await first;
    expect(get(store.state).summary).toEqual({
      requiresPassphrase: false,
      recoveryAvailable: true,
      busy: false,
    });
  });
});
