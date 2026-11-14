import { describe, it, expect, vi } from 'vitest';
import { get } from 'svelte/store';
import {
  createAttachments,
  validateAttachments,
  validateAttachmentDraft,
  type AttachmentBridge,
  type AttachmentItem,
} from './attachments';

const id = 'B'.repeat(21) + 'A';
const file = 'C'.repeat(21) + 'A';
const token = 'D'.repeat(32);
const item: AttachmentItem = {
  id: file,
  name: 'synthetic.pdf',
  mediaType: 'application/pdf',
  size: 17,
  direction: 'incoming',
  state: 'complete',
  transferredBytes: 17,
  warning: '',
  previewURL: '',
};
const snapshot = () => ({
  contactID: id,
  revision: 2,
  items: [item],
  drafts: [],
  autoAcceptImages: false,
  nextCursor: '',
  hasMore: false,
});
function mock(): AttachmentBridge {
  return {
    list: vi.fn(async () => snapshot()),
    history: vi.fn(async () => snapshot()),
    target: vi.fn(async () => {}),
    pick: vi.fn(async () => ({})),
    paste: vi.fn(async () => ({})),
    send: vi.fn(async () => {}),
    discard: vi.fn(async () => {}),
    decision: vi.fn(async () => {}),
    autoAccept: vi.fn(async () => {}),
    preview: vi.fn(async () => '/local-media/' + token),
    save: vi.fn(async () => ({ handle: '' })),
    reveal: vi.fn(async () => {}),
    subscribe: vi.fn(() => () => {}),
  };
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => (resolve = done));
  return { promise, resolve };
}
describe('attachment state boundary', () => {
  it('serializes and coalesces native drop targets through contact changes and offline', async () => {
    const api = mock(),
      pending = deferred<void>(),
      other = 'E'.repeat(21) + 'A';
    api.list = vi.fn(async (id) => ({ ...snapshot(), contactID: id }));
    api.target = vi.fn((id) =>
      id === snapshot().contactID ? pending.promise : Promise.resolve(),
    );
    const store = createAttachments(api);
    const first = store.select(id);
    await vi.waitFor(() => expect(api.target).toHaveBeenCalledWith(id));
    const second = store.select(other);
    expect(api.target).toHaveBeenCalledTimes(1);
    pending.resolve();
    await Promise.all([first, second]);
    expect(vi.mocked(api.target).mock.calls.map(([id]) => id)).toEqual([
      id,
      other,
    ]);
    expect(get(store.state).contactID).toBe(other);
    store.stop();
    await vi.waitFor(() => expect(api.target).toHaveBeenLastCalledWith(''));
  });
  it('does not restore a delayed older attachment page after Clear history', async () => {
    const api = mock(),
      pending = deferred<unknown>();
    api.list = vi.fn(async () => ({
      ...snapshot(),
      hasMore: true,
      nextCursor: '50',
    }));
    api.history = vi.fn(() => pending.promise);
    const store = createAttachments(api);
    await store.select(id);
    await vi.waitFor(() => expect(get(store.state).hasMore).toBe(true));
    const older = store.older();
    api.list = vi.fn(async () => ({ ...snapshot(), revision: 3, items: [] }));
    store.invalidate();
    await vi.waitFor(() => expect(get(store.state).items).toEqual([]));
    pending.resolve(snapshot());
    await older;
    expect(get(store.state).items).toEqual([]);
    store.stop();
  });
  it('rejects external assets, paths, excessive sizes and unexpected response fields', () => {
    expect(validateAttachments(snapshot(), id).items[0]).toEqual(item);
    for (const changed of [
      { previewURL: 'https://example.test/a.png' },
      { previewURL: '/local-media/../secret' },
      { name: '../private.pdf' },
      { size: 2 ** 31 + 1 },
      { warning: 'raw diagnostic text' },
      { localPath: 'C:/private.pdf' },
    ]) {
      expect(() =>
        validateAttachments(
          { ...snapshot(), items: [{ ...item, ...changed }] },
          id,
        ),
      ).toThrow();
    }
    expect(() =>
      validateAttachmentDraft({
        handle: token,
        name: 'photo.png',
        mediaType: 'image/png',
        size: 42,
        previewURL: 'data:image/png;base64,AAAA',
        requiresRelayWarning: false,
      }),
    ).toThrow();
  });
  it('picking and image paste never implicitly send or approve a received offer', async () => {
    const api = mock(),
      store = createAttachments(api);
    store.stop();
    expect(api.target).not.toHaveBeenCalled();
    await store.select(id);
    await store.pick(false);
    await store.paste();
    expect(api.pick).toHaveBeenCalledWith(id, false);
    expect(api.paste).toHaveBeenCalledWith(id);
    expect(api.send).not.toHaveBeenCalled();
    expect(api.decision).not.toHaveBeenCalled();
    expect(api.autoAccept).not.toHaveBeenCalled();
    store.stop();
  });
  it('retires decrypted results and pending previews after lock even for the same contact on reopen', async () => {
    const api = mock(),
      pending = deferred<string>();
    api.preview = vi.fn(() => pending.promise);
    const store = createAttachments(api);
    await store.select(id);
    await vi.waitFor(() => expect(get(store.state).items).toHaveLength(1));
    const playing = store.preview({ ...item, mediaType: 'video/mp4' });
    store.stop();
    expect(get(store.state).items).toEqual([]);
    await store.select(id);
    pending.resolve('/local-media/' + token);
    await playing;
    expect(get(store.state).videos).toEqual({});
    store.stop();
  });
  it('does not retain a delayed saved-file capability after going offline', async () => {
    const api = mock(),
      pending = deferred<unknown>();
    api.save = vi.fn(() => pending.promise);
    const store = createAttachments(api);
    await store.select(id);
    const saving = store.save(item);
    store.stop();
    await store.select(id);
    pending.resolve({ handle: token });
    await saving;
    expect(get(store.state).saved).toEqual({});
    store.stop();
  });
});
