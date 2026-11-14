import { get } from 'svelte/store';
import { afterEach, expect, it, vi } from 'vitest';
import { createSession } from './session';
import { unavailableStatus, type Status } from '../backend';

afterEach(() => {
  vi.unstubAllGlobals();
});
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((finish) => {
    resolve = finish;
  });
  return { promise, resolve };
}
function environment() {
  vi.stubGlobal(
    'window',
    Object.assign(new EventTarget(), { _wails: { environment: {} } }),
  );
  const handlers = new Map<string, (value: unknown) => void>();
  const subscribe = (name: string, callback: (value: unknown) => void) => {
    handlers.set(name, callback);
    return () => {
      handlers.delete(name);
    };
  };
  return {
    subscribe,
    emit: (name: string, value: unknown) => handlers.get(name)?.(value),
    handlers,
  };
}
const unlocked: Status = {
  ...unavailableStatus,
  storageState: 'unlocked',
  identityFingerprint: 'a'.repeat(64),
  messagingAvailable: true,
};

it('native storage hints synchronously retire decrypted permissions and old status responses before an authoritative reload', async () => {
  const events = environment();
  const old = deferred<Status>();
  const request = vi
    .fn()
    .mockReturnValueOnce(old.promise)
    .mockResolvedValueOnce({ ...unavailableStatus, storageState: 'locked' });
  const session = createSession(request, events.subscribe);
  const cleared = vi.fn();
  const requests: Promise<unknown>[] = [];
  const stop = session.mount(
    () => {
      requests.push(session.refresh());
    },
    { storage: cleared },
  );
  events.emit('whatsup:storage-changed', { state: 'locked' });
  expect(cleared).toHaveBeenCalledOnce();
  expect(get(session.state).status.identityFingerprint).toBe('');
  expect(get(session.state).status.messagingAvailable).toBe(false);
  old.resolve(unlocked);
  await Promise.all(requests);
  expect(request).toHaveBeenCalledTimes(2);
  expect(get(session.state).status.storageState).toBe('locked');
  expect(get(session.state).status.identityFingerprint).toBe('');
  stop();
  expect(events.handlers.size).toBe(0);
});

it('an unlocked hint cannot grant identity or history access while its status request is pending', async () => {
  const events = environment();
  const held = deferred<Status>();
  const request = vi
    .fn()
    .mockResolvedValueOnce({ ...unavailableStatus, storageState: 'locked' })
    .mockReturnValueOnce(held.promise);
  const session = createSession(request, events.subscribe);
  const requests: Promise<unknown>[] = [];
  const stop = session.mount(() => {
    requests.push(session.refresh());
  });
  await requests[0];
  events.emit('whatsup:storage-changed', { state: 'unlocked' });
  expect(get(session.state).status.storageState).toBe('locked');
  expect(get(session.state).status.identityFingerprint).toBe('');
  held.resolve(unlocked);
  await requests[1];
  expect(get(session.state).status).toEqual(unlocked);
  stop();
});

it('native visibility changes have no storage authority and malformed hints are ignored', async () => {
  const events = environment();
  const request = vi.fn(async () => unlocked);
  const session = createSession(request, events.subscribe);
  const visibility = vi.fn();
  const stop = session.mount(
    () => {
      void session.refresh();
    },
    { visibility },
  );
  await vi.waitFor(() =>
    expect(get(session.state).status.storageState).toBe('unlocked'),
  );
  events.emit('whatsup:window-visible', false);
  expect(get(session.state).windowVisible).toBe(false);
  expect(visibility).toHaveBeenCalledWith(false);
  events.emit('whatsup:storage-changed', {
    state: 'unlocked',
    identityFingerprint: 'spoof',
  });
  events.emit('whatsup:window-visible', { visible: true });
  expect(request).toHaveBeenCalledOnce();
  expect(get(session.state).status).toEqual(unlocked);
  stop();
});
