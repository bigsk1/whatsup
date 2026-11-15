import { get } from 'svelte/store';
import { describe, expect, it, vi } from 'vitest';
import {
  createAvatars,
  validateAvatars,
  type AvatarBridge,
  type AvatarSnapshot,
} from './avatars';

const id = 'B'.repeat(21) + 'A',
  other = 'C'.repeat(21) + 'A';
const photo = '/local-media/' + 'a'.repeat(32),
  replacement = '/local-media/' + 'b'.repeat(32);
const snapshot = (update: Partial<AvatarSnapshot> = {}): AvatarSnapshot => ({
  revision: 1,
  ownURL: photo,
  shareAvatar: false,
  showContactPhotos: true,
  contacts: [{ contactID: id, url: photo }],
  ...update,
});
const api = (
  getValue: () => Promise<unknown> = async () => snapshot(),
): AvatarBridge => ({
  get: vi.fn(getValue),
  choose: vi.fn(async () => snapshot({ revision: 2, ownURL: replacement })),
  remove: vi.fn(async () => snapshot({ revision: 2, ownURL: '' })),
  preferences: vi.fn(async (shareAvatar, showContactPhotos) =>
    snapshot({
      revision: 2,
      shareAvatar,
      showContactPhotos,
      contacts: showContactPhotos
        ? [{ contactID: id, url: photo }]
        : [{ contactID: id, url: '' }],
    }),
  ),
  subscribe: () => () => {},
});
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

describe('opaque encrypted profile picture state', () => {
  it('accepts only bounded exact opaque capabilities, never bytes, paths, remote URLs or metadata suffixes', () => {
    expect(validateAvatars(snapshot())).toEqual(snapshot());
    const inputs = [
      null,
      [],
      snapshot({ revision: -1 }),
      snapshot({ revision: Number.MAX_SAFE_INTEGER + 1 }),
      snapshot({ ownURL: 'https://peer.example/avatar.png' }),
      snapshot({ ownURL: 'data:image/png;base64,' + 'A'.repeat(300000) }),
      snapshot({ ownURL: 'file:///private/photo.jpg' }),
      snapshot({ ownURL: photo + '?exif=location' }),
      { ...snapshot(), image: 'private bytes' },
      snapshot({ contacts: Array(33).fill({ contactID: id, url: photo }) }),
      snapshot({
        contacts: [
          { contactID: id, url: photo },
          { contactID: id, url: replacement },
        ],
      }),
      snapshot({ contacts: [{ contactID: 'not a contact', url: photo }] }),
      snapshot({ showContactPhotos: false }),
    ];
    for (const input of inputs)
      expect(() => validateAvatars(input)).toThrow(
        'Invalid profile picture response.',
      );
  });
  it('shows the owner photo when sharing is off, and changing contact display never clears it', async () => {
    const bridge = api(),
      store = createAvatars(bridge);
    store.scope(true, [id]);
    await vi.waitFor(() =>
      expect(get(store.state).snapshot?.ownURL).toBe(photo),
    );
    expect(get(store.state).snapshot?.shareAvatar).toBe(false);
    await store.preferences(false, false);
    expect(get(store.state).snapshot?.ownURL).toBe(photo);
    expect(get(store.state).snapshot?.contacts[0].url).toBe('');
    expect(bridge.preferences).toHaveBeenCalledWith(false, false);
    store.stop();
  });
  it('ignores a replayed older snapshot after replacement and discards replies after lock', async () => {
    const bridge = api(),
      store = createAvatars(bridge);
    store.scope(true, [id]);
    await vi.waitFor(() => expect(get(store.state).snapshot).not.toBeNull());
    await store.choose();
    await store.reload();
    expect(get(store.state).snapshot?.ownURL).toBe(replacement);
    const held = deferred<unknown>();
    bridge.choose = () => held.promise;
    const choosing = store.choose();
    store.stop();
    held.resolve(snapshot({ revision: 3, ownURL: photo }));
    await choosing;
    expect(get(store.state)).toEqual({
      snapshot: null,
      loading: false,
      busy: false,
      notice: '',
    });
  });
  it('filters unknown contacts and cannot revive a forgotten photo with a held response', async () => {
    const held = deferred<unknown>(),
      bridge = api(),
      store = createAvatars(bridge);
    store.scope(true, [id]);
    await vi.waitFor(() => expect(get(store.state).snapshot).not.toBeNull());
    bridge.get = vi
      .fn()
      .mockReturnValueOnce(held.promise)
      .mockResolvedValue(
        snapshot({
          revision: 2,
          contacts: [{ contactID: other, url: replacement }],
        }),
      );
    const old = store.reload();
    store.scope(true, [other]);
    await vi.waitFor(() =>
      expect(get(store.state).snapshot?.contacts[0].contactID).toBe(other),
    );
    held.resolve(snapshot({ revision: 3 }));
    await old;
    expect(get(store.state).snapshot?.contacts).toEqual([
      { contactID: other, url: replacement },
    ]);
    store.stop();
  });
  it('coalesces authenticated-contact hints and rejects malformed or replayed event hints', async () => {
    vi.useFakeTimers();
    try {
      let hint: (value: unknown) => void = () => {};
      const bridge = api();
      bridge.subscribe = (callback) => {
        hint = callback;
        return () => {};
      };
      const store = createAvatars(bridge);
      store.scope(true, [id]);
      await Promise.resolve();
      await Promise.resolve();
      hint({ contactID: other, revision: 1 });
      hint({ contactID: id, revision: 1, url: 'https://peer.example' });
      for (let revision = 1; revision < 100; revision++)
        hint({ contactID: id, revision });
      await vi.advanceTimersByTimeAsync(250);
      expect(bridge.get).toHaveBeenCalledTimes(2);
      hint({ contactID: id, revision: 98 });
      hint({ contactID: id, revision: 99 });
      await vi.advanceTimersByTimeAsync(500);
      expect(bridge.get).toHaveBeenCalledTimes(2);
      store.stop();
    } finally {
      vi.useRealTimers();
    }
  });
  it('reports a malformed or oversized replacement generically without echoing backend data', async () => {
    const bridge = api(),
      store = createAvatars(bridge);
    store.scope(true, [id]);
    await vi.waitFor(() => expect(get(store.state).snapshot).not.toBeNull());
    bridge.choose = async () =>
      snapshot({ ownURL: 'data:image/png;base64,' + 'secret'.repeat(100000) });
    await store.choose();
    expect(get(store.state).snapshot?.ownURL).toBe(photo);
    expect(get(store.state).notice).toContain('still PNG, JPEG or WebP');
    expect(get(store.state).notice).not.toContain('secret');
    store.stop();
  });
  it('keeps failed-picker feedback through queued contact hints and failed background reads, until an explicit refresh', async () => {
    vi.useFakeTimers();
    try {
      let hint: (value: unknown) => void = () => {};
      const bridge = api(),
        held = deferred<unknown>();
      bridge.subscribe = (callback) => {
        hint = callback;
        return () => {};
      };
      bridge.choose = () => held.promise;
      const store = createAvatars(bridge);
      store.scope(true, [id]);
      await vi.advanceTimersByTimeAsync(0);
      const selecting = store.choose();
      hint({ contactID: id, revision: 10 });
      await vi.advanceTimersByTimeAsync(250);
      expect(bridge.get).toHaveBeenCalledTimes(1);
      held.resolve(
        snapshot({ ownURL: 'data:image/png;base64,SYNTHETIC-PRIVATE' }),
      );
      await selecting;
      const feedback = get(store.state).notice;
      expect(feedback).toContain('The profile picture could not be changed.');
      await vi.advanceTimersByTimeAsync(250);
      expect(bridge.get).toHaveBeenCalledTimes(2);
      expect(get(store.state).notice).toBe(feedback);
      expect(get(store.state).snapshot?.ownURL).toBe(photo);
      bridge.get = vi
        .fn()
        .mockRejectedValueOnce(new Error('SYNTHETIC-PRIVATE-GET'))
        .mockResolvedValue(snapshot());
      await store.reload();
      expect(get(store.state).notice).toBe(feedback);
      expect(get(store.state).notice).not.toContain('SYNTHETIC-PRIVATE');
      await store.reload(true);
      expect(get(store.state).notice).toBe('');
      store.stop();
    } finally {
      vi.useRealTimers();
    }
  });
  it('keeps successful action feedback during background reload and clears it when the next mutation begins', async () => {
    const bridge = api(),
      store = createAvatars(bridge);
    store.scope(true, [id]);
    await vi.waitFor(() => expect(get(store.state).snapshot).not.toBeNull());
    await store.choose();
    const feedback = get(store.state).notice;
    expect(feedback).toBe('Profile picture saved on this device.');
    await store.reload();
    expect(get(store.state).notice).toBe(feedback);
    const held = deferred<unknown>();
    bridge.remove = () => held.promise;
    const removing = store.remove();
    expect(get(store.state).notice).toBe('');
    held.resolve(snapshot({ revision: 3, ownURL: '' }));
    await removing;
    expect(get(store.state).notice).toBe('Profile picture removed.');
    store.stop();
    expect(get(store.state).notice).toBe('');
  });
});
