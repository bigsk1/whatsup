import { get } from 'svelte/store';
import { describe, expect, it, vi } from 'vitest';
import {
  createContactStorage,
  validateContactStorage,
  type StorageBridge,
} from './storage';

const id = 'B'.repeat(21) + 'A',
  other = 'C'.repeat(21) + 'A';
const usage = (contactID = id) => ({
  contactID,
  messagesUsedBytes: 4096,
  messagesQuotaBytes: 2 ** 30,
  attachmentsUsedBytes: 2 ** 20,
  attachmentsQuotaBytes: 4 * 2 ** 30,
});
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}
describe('per-contact storage state', () => {
  it('accepts only exact scoped finite quota counters', () => {
    expect(validateContactStorage(usage(), id)).toEqual(usage());
    for (const update of [
      { contactID: other },
      { messagesUsedBytes: -1 },
      { messagesUsedBytes: NaN },
      { attachmentsUsedBytes: Number.MAX_SAFE_INTEGER + 1 },
      { messagesQuotaBytes: 0 },
      { path: '/private/file' },
    ])
      expect(() =>
        validateContactStorage({ ...usage(), ...update }, id),
      ).toThrow();
    expect(() => validateContactStorage(usage(), '')).toThrow();
  });
  it('loads only a selected settings contact and drops stale replies after switching or locking', async () => {
    const held = deferred<unknown>(),
      stop = vi.fn();
    const api: StorageBridge = {
      get: vi.fn(async (selected) =>
        selected === id ? held.promise : usage(other),
      ),
      subscribe: vi.fn(() => stop),
    };
    const store = createContactStorage(api);
    expect(api.get).not.toHaveBeenCalled();
    store.select(id);
    expect(get(store.state).loading).toBe(true);
    store.select(other);
    await vi.waitFor(() =>
      expect(get(store.state).usage?.contactID).toBe(other),
    );
    held.resolve(usage());
    await Promise.resolve();
    expect(get(store.state).usage?.contactID).toBe(other);
    store.stop();
    expect(get(store.state).usage).toBeNull();
    expect(stop).toHaveBeenCalledTimes(2);
  });
  it('coalesces valid contact hints and stops subscriptions when settings closes', async () => {
    vi.useFakeTimers();
    try {
      let hint: (value: unknown) => void = () => {};
      const unsubscribe = vi.fn();
      const api: StorageBridge = {
        get: vi.fn(async () => usage()),
        subscribe: (callback) => {
          hint = callback;
          return unsubscribe;
        },
      };
      const store = createContactStorage(api);
      store.select(id);
      await Promise.resolve();
      hint({ contactID: other, revision: 1 });
      hint({ contactID: id, revision: 2, path: '/private' });
      await vi.advanceTimersByTimeAsync(1000);
      expect(api.get).toHaveBeenCalledTimes(1);
      for (let revision = 2; revision < 50; revision++)
        hint({ contactID: id, revision });
      await vi.advanceTimersByTimeAsync(1000);
      expect(api.get).toHaveBeenCalledTimes(2);
      hint({ contactID: id, revision: 51 });
      store.stop();
      await vi.advanceTimersByTimeAsync(2000);
      expect(api.get).toHaveBeenCalledTimes(2);
      expect(unsubscribe).toHaveBeenCalledOnce();
    } finally {
      vi.useRealTimers();
    }
  });
  it('hides previous usage on a failed refresh without echoing backend paths', async () => {
    const api: StorageBridge = {
      get: vi
        .fn()
        .mockResolvedValueOnce(usage())
        .mockRejectedValueOnce(new Error('/private/profile')),
      subscribe: () => () => {},
    };
    const store = createContactStorage(api);
    store.select(id);
    await vi.waitFor(() => expect(get(store.state).usage).not.toBeNull());
    await store.reload();
    expect(get(store.state).usage).toBeNull();
    expect(get(store.state).notice).toContain('could not be loaded');
    expect(JSON.stringify(get(store.state))).not.toContain('/private');
    store.stop();
  });
});
