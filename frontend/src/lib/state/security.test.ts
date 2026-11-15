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
const backup =
  '.recovery-rollback-AQEBAQEBAQEBAQEBAQEBAQ/pre-migration-00000000000000000000000000000000.vault';
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
    setAside: vi.fn(async () => status()),
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
      backupAttention: '',
    });
  });

  it('accepts only the original security fields and a bounded recognized relative backup filename', () => {
    const original = {
      requiresPassphrase: true,
      recoveryAvailable: false,
      busy: false,
    };
    expect(validateStorageSecurity(original).backupAttention).toBe('');
    for (const path of [
      '',
      backup,
      backup.replaceAll('/', '\\'),
      'pre-migration-0123456789abcdef0123456789abcdef.vault',
    ])
      expect(
        validateStorageSecurity({ ...original, backupAttention: path })
          .backupAttention,
      ).toBe(path);
    for (const update of [
      { backupAttention: null },
      { backupAttention: 'C:/Users/private/state.vault' },
      { backupAttention: '/home/private/state.vault' },
      { backupAttention: '../state.vault' },
      { backupAttention: 'attachments/private.bin' },
      {
        backupAttention:
          'pre-migration-' + '0'.repeat(32) + '.vault/../state.vault',
      },
      { backupAttention: '.state-' + '1'.repeat(161) + '.tmp' },
      { backupAttention: backup, path: '/private/location' },
      { other: backup },
    ])
      expect(() =>
        validateStorageSecurity({ ...original, ...update }),
      ).toThrow();
  });

  it('reloads authoritative backup attention after a failed passphrase change without echoing the error', async () => {
    const bridge = api();
    let attention = '';
    bridge.get = vi.fn(async () => ({
      requiresPassphrase: true,
      recoveryAvailable: false,
      busy: false,
      backupAttention: attention,
    }));
    bridge.configure = vi.fn(async () => {
      attention = backup;
      throw new Error(secret + ' C:/Users/private/state.vault');
    });
    const { store } = setup(bridge);
    store.begin('disable');
    await store.submit(secret, '');
    expect(get(store.state).summary.backupAttention).toBe(backup);
    expect(get(store.state).loaded).toBe(true);
    expect(get(store.state).operation).toBe('');
    expect(JSON.stringify(get(store.state))).not.toContain(secret);
    expect(JSON.stringify(get(store.state))).not.toContain('C:/Users');
  });

  it('repairs only an explicitly selected reported backup with the exact current secret and no path RPC argument', async () => {
    const bridge = api();
    bridge.get = vi.fn(async () => ({
      requiresPassphrase: true,
      recoveryAvailable: false,
      busy: false,
      backupAttention: backup,
    }));
    const { store, changed } = setup(bridge);
    store.begin('repair');
    expect(get(store.state).operation).toBe('');
    await store.load();
    expect(bridge.setAside).not.toHaveBeenCalled();
    store.begin('repair');
    await store.submit('short', secret);
    expect(bridge.setAside).not.toHaveBeenCalled();
    await store.submit(secret, 'ignored new secret');
    expect(bridge.setAside).toHaveBeenCalledExactlyOnceWith(secret);
    expect(changed).toHaveBeenCalledExactlyOnceWith(status());
    expect(get(store.state).summary.backupAttention).toBe('');
    expect(get(store.state).operation).toBe('');
    expect(JSON.stringify(get(store.state))).not.toContain(secret);
  });

  it('requires no secret for an OS-only set-aside and refuses repair without a reported target', async () => {
    const bridge = api();
    bridge.get = vi.fn(async () => ({
      requiresPassphrase: false,
      recoveryAvailable: false,
      busy: false,
      backupAttention: backup,
    }));
    const { store } = setup(bridge);
    await store.load();
    store.begin('repair');
    await store.submit('', '');
    expect(bridge.setAside).toHaveBeenCalledExactlyOnceWith('');
    store.begin('repair');
    await store.submit('', '');
    expect(bridge.setAside).toHaveBeenCalledTimes(1);
  });

  it('keeps repair retryable after a wrong current secret or changed target and never echoes backend data', async () => {
    const bridge = api();
    bridge.get = vi.fn(async () => ({
      requiresPassphrase: true,
      recoveryAvailable: false,
      busy: false,
      backupAttention: backup,
    }));
    bridge.setAside = vi
      .fn()
      .mockRejectedValueOnce(new Error(secret + ' C:/Users/private'))
      .mockResolvedValueOnce(status());
    const { store, changed } = setup(bridge);
    await store.load();
    store.begin('repair');
    await store.submit(secret, '');
    expect(get(store.state).operation).toBe('repair');
    expect(get(store.state).summary.backupAttention).toBe(backup);
    expect(get(store.state).busy).toBe(false);
    expect(get(store.state).notice).toContain('could not be set aside');
    expect(JSON.stringify(get(store.state))).not.toContain(secret);
    expect(JSON.stringify(get(store.state))).not.toContain('C:/Users');
    expect(changed).not.toHaveBeenCalled();
    await store.submit(secret, '');
    expect(changed).toHaveBeenCalledOnce();
    expect(get(store.state).operation).toBe('');
  });

  it('ignores a delayed repair result after native offline and does not revive authority', async () => {
    const bridge = api(),
      held = deferred<unknown>();
    bridge.get = vi.fn(async () => ({
      requiresPassphrase: true,
      recoveryAvailable: false,
      busy: false,
      backupAttention: backup,
    }));
    bridge.setAside = vi.fn(() => held.promise);
    const { store, changed } = setup(bridge);
    await store.load();
    store.begin('repair');
    const running = store.submit(secret, '');
    store.reset();
    held.resolve(status());
    await running;
    expect(changed).not.toHaveBeenCalled();
    expect(get(store.state).operation).toBe('');
    expect(get(store.state).summary.backupAttention).toBe('');
  });

  it('ignores a late attention reload after a failed unlock is cancelled', async () => {
    const bridge = api(),
      held = deferred<unknown>();
    bridge.unlock = vi.fn(async () => {
      throw new Error(secret);
    });
    const { store } = setup(bridge);
    store.begin('unlock');
    await store.load();
    bridge.get = vi.fn(() => held.promise);
    const running = store.submit('', secret);
    await vi.waitFor(() => expect(bridge.get).toHaveBeenCalledOnce());
    store.reset();
    held.resolve({
      requiresPassphrase: true,
      recoveryAvailable: false,
      busy: false,
      backupAttention: backup,
    });
    await running;
    expect(get(store.state).loaded).toBe(false);
    expect(get(store.state).summary.backupAttention).toBe('');
  });

  it('reloads backup attention and the installed protection requirement after an uncertain recovery commit', async () => {
    const bridge = api();
    let failedCommit = false;
    bridge.get = vi.fn(async () => ({
      requiresPassphrase: !failedCommit,
      recoveryAvailable: false,
      busy: false,
      backupAttention: failedCommit ? backup : '',
    }));
    bridge.commit = vi.fn(async () => {
      failedCommit = true;
      throw new Error(secret);
    });
    const { store, changed } = setup(bridge);
    store.begin('import');
    await store.submit('', secret);
    await store.commit(secret, true, true);
    expect(get(store.state).summary.requiresPassphrase).toBe(false);
    expect(get(store.state).summary.backupAttention).toBe(backup);
    expect(get(store.state).preview).toBeNull();
    expect(changed).not.toHaveBeenCalled();
    store.begin('repair');
    await store.submit('', '');
    expect(bridge.setAside).toHaveBeenCalledExactlyOnceWith('');
    expect(JSON.stringify(get(store.state))).not.toContain(secret);
  });
});
