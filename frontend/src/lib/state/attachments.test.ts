import { describe, it, expect, vi } from 'vitest';
import { get } from 'svelte/store';
import {
  createAttachments,
  validateAttachments,
  validateAttachmentDraft,
  validAttachmentTime,
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
  it('accepts a bounded local UTC timestamp or unknown legacy time, rejecting malformed dates and unknown fields', () => {
    for (const recordedAt of [
      '',
      '2026-11-14T12:00:00Z',
      '2026-11-14T12:00:00.123456789Z',
    ]) {
      expect(validAttachmentTime(recordedAt)).toBe(true);
      expect(
        validateAttachments(
          { ...snapshot(), items: [{ ...item, recordedAt }] },
          id,
        ).items[0].recordedAt,
      ).toBe(recordedAt);
    }
    for (const recordedAt of [
      null,
      1,
      '2026-02-31T12:00:00Z',
      '2026-11-14T12:00:00+01:00',
      '1960-01-01T00:00:00Z',
      '2026-11-14T12:00:00.1234567891Z',
    ])
      expect(() =>
        validateAttachments(
          { ...snapshot(), items: [{ ...item, recordedAt }] },
          id,
        ),
      ).toThrow();
    expect(() =>
      validateAttachments(
        {
          ...snapshot(),
          items: [{ ...item, recordedAt: '', path: '/private' }],
        },
        id,
      ),
    ).toThrow();
  });
  it('requires explicit deletion confirmation and closes only that item media capabilities', async () => {
    const api = mock();
    const store = createAttachments(api);
    await store.select(id);
    await vi.waitFor(() => expect(get(store.state).items).toHaveLength(1));
    await store.preview({ ...item, mediaType: 'image/png' });
    expect(get(store.state).image?.id).toBe(file);
    store.requestDelete(item);
    expect(api.decision).not.toHaveBeenCalled();
    store.cancelDelete();
    await store.confirmDelete();
    expect(api.decision).not.toHaveBeenCalled();
    store.requestDelete(item);
    await store.confirmDelete();
    expect(api.decision).toHaveBeenCalledExactlyOnceWith(id, file, 'delete');
    expect(get(store.state).items).toEqual([]); // An old same-revision fixture cannot resurrect it.
    expect(get(store.state).image).toBeNull();
    expect(get(store.state).deleteTarget).toBeNull();
    expect(get(store.state).saved).toEqual({});
    store.stop();
  });
  it('does not delete a replacement selection or retain a late result after lock', async () => {
    const api = mock(),
      held = deferred<void>();
    const store = createAttachments(api);
    await store.select(id);
    await vi.waitFor(() => expect(get(store.state).items).toHaveLength(1));
    store.requestDelete(item);
    await store.select('');
    await store.confirmDelete();
    expect(api.decision).not.toHaveBeenCalled();
    await store.select(id);
    await vi.waitFor(() => expect(get(store.state).items).toHaveLength(1));
    api.decision = vi.fn(() => held.promise);
    store.requestDelete(item);
    const deleting = store.confirmDelete();
    store.stop();
    held.resolve();
    await deleting;
    expect(get(store.state).contactID).toBe('');
    expect(get(store.state).deleteTarget).toBeNull();
    expect(get(store.state).items).toEqual([]);
  });
  it('keeps a failed deletion retryable and retains the visible item', async () => {
    const api = mock();
    api.decision = vi
      .fn()
      .mockRejectedValueOnce(new Error('Deletion failed'))
      .mockResolvedValueOnce(undefined);
    const store = createAttachments(api);
    await store.select(id);
    await vi.waitFor(() => expect(get(store.state).items).toHaveLength(1));
    store.requestDelete(item);
    await store.confirmDelete();
    expect(get(store.state).items).toHaveLength(1);
    expect(get(store.state).deleteTarget?.id).toBe(file);
    await store.confirmDelete();
    expect(get(store.state).items).toEqual([]);
    store.stop();
  });
  it('clears a retired list spinner if deletion fails while that list is held', async () => {
    const api = mock(),
      held = deferred<unknown>();
    const store = createAttachments(api);
    await store.select(id);
    await vi.waitFor(() => expect(get(store.state).items).toHaveLength(1));
    api.list = vi.fn(() => held.promise);
    const listing = store.reload();
    await vi.waitFor(() => expect(get(store.state).loading).toBe(true));
    api.decision = vi.fn().mockRejectedValue(new Error('Deletion failed'));
    store.requestDelete(item);
    await store.confirmDelete();
    expect(get(store.state).loading).toBe(false);
    expect(get(store.state).busy).toBe(false);
    held.resolve(snapshot());
    await listing;
    expect(get(store.state).loading).toBe(false);
    expect(get(store.state).deleteTarget?.id).toBe(file);
    store.stop();
  });
  it('accepts only the fixed cancellation outcomes returned by Go', () => {
    for (const warning of ['cancellation-pending', 'peer-completed']) {
      const value = {
        ...snapshot(),
        items: [{ ...item, state: 'declined', warning }],
      };
      expect(validateAttachments(value, id).items[0].warning).toBe(warning);
    }
    expect(() =>
      validateAttachments(
        { ...snapshot(), items: [{ ...item, warning: 'untrusted text' }] },
        id,
      ),
    ).toThrow();
  });
  it('keeps an older-page image open across current-page reloads until explicit invalidation', async () => {
    const api = mock();
    const older = { ...item, id: 'E'.repeat(21) + 'A', mediaType: 'image/png' };
    api.list = vi.fn(async () => ({
      ...snapshot(),
      hasMore: true,
      nextCursor: '2',
    }));
    api.history = vi.fn(async () => ({
      ...snapshot(),
      items: [older],
      hasMore: false,
      nextCursor: '',
    }));
    const store = createAttachments(api);
    await store.select(id);
    await vi.waitFor(() => expect(get(store.state).hasMore).toBe(true));
    await store.older();
    await store.preview(older);
    expect(get(store.state).image?.id).toBe(older.id);
    await store.reload();
    expect(get(store.state).image?.id).toBe(older.id);
    store.invalidate();
    expect(get(store.state).image).toBeNull();
    store.stop();
  });
  it('keeps the newest attachments when loaded history exceeds its bound', async () => {
    const api = mock();
    const page = (index: number) => ({
      ...snapshot(),
      items: Array.from({ length: 32 }, (_, offset) => ({
        ...item,
        id: String(index * 32 + offset).padStart(21, 'F') + 'A',
      })),
      hasMore: index < 16,
      nextCursor: index < 16 ? String(index + 1) : '',
    });
    api.list = vi.fn(async () => page(0));
    api.history = vi.fn(async (_contact: string, cursor: string) =>
      page(Number(cursor)),
    );
    const store = createAttachments(api);
    await store.select(id);
    await vi.waitFor(() => expect(get(store.state).hasMore).toBe(true));
    while (get(store.state).hasMore) await store.older();
    const items = get(store.state).items;
    expect(items).toHaveLength(512);
    expect(items[0].id).toBe(page(0).items[0].id);
    store.stop();
  });
  it('explicit image dismissal retires a pending full-image response', async () => {
    const api = mock(),
      pending = deferred<string>();
    api.preview = vi.fn(() => pending.promise);
    const store = createAttachments(api);
    await store.select(id);
    const viewing = store.preview({ ...item, mediaType: 'image/png' });
    store.closeImage();
    pending.resolve('/local-media/' + token);
    await viewing;
    expect(get(store.state).image).toBeNull();
    store.stop();
  });
  it('preserves drafts offline, blocks wire sends only on connected protocol3, and keeps saved-file actions after removal', async () => {
    const api = mock(),
      store = createAttachments(api);
    await store.select(id);
    const draft = {
      handle: token,
      name: 'synthetic.pdf',
      mediaType: 'application/pdf',
      size: 17,
      previewURL: '',
      requiresRelayWarning: false,
    };
    store.setConnection('connected', 3);
    await store.pick(false);
    expect(api.pick).toHaveBeenCalledWith(id, false);
    await store.send(draft, false);
    expect(api.send).not.toHaveBeenCalled();
    await store.decision(item, 'resume');
    expect(api.decision).not.toHaveBeenCalled();
    store.setConnection('offline', 0);
    await store.send(draft, false);
    expect(api.send).toHaveBeenCalledWith(id, token, false);
    store.setConnection('peer-removed', 0);
    await store.send(draft, false);
    expect(api.send).toHaveBeenCalledTimes(1);
    await store.save(item);
    expect(api.save).toHaveBeenCalledWith(id, file);
    store.stop();
  });
  it('blocks staging and transfer actions while the contact is paused but keeps saved files available', async () => {
    const api = mock();
    const store = createAttachments(api);
    await store.select(id);
    store.setConnection('paused', 0);
    await store.pick();
    await store.paste();
    await store.send(
      {
        handle: token,
        name: 'synthetic.pdf',
        mediaType: 'application/pdf',
        size: 17,
        previewURL: '',
        requiresRelayWarning: false,
      },
      false,
    );
    for (const action of ['accept', 'resume', 'allow-relay'])
      await store.decision(item, action);
    expect(api.pick).not.toHaveBeenCalled();
    expect(api.paste).not.toHaveBeenCalled();
    expect(api.send).not.toHaveBeenCalled();
    expect(api.decision).not.toHaveBeenCalled();
    await store.save(item);
    expect(api.save).toHaveBeenCalledWith(id, file);
    store.stop();
  });
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
  it('rebinds the exact native input contact after history invalidation even though selection did not change', async () => {
    const api = mock();
    let nativeTarget = '';
    api.target = vi.fn(async (value) => {
      nativeTarget = value;
    });
    const store = createAttachments(api);
    await store.select(id);
    expect(nativeTarget).toBe(id);
    expect(get(store.state).inputReady).toBe(true);
    nativeTarget = ''; // Clear history retires the backend contact's input lease.
    store.invalidate();
    expect(get(store.state).inputReady).toBe(false);
    await vi.waitFor(() => expect(nativeTarget).toBe(id));
    await vi.waitFor(() => expect(get(store.state).inputReady).toBe(true));
    expect(vi.mocked(api.target).mock.calls.map(([value]) => value)).toEqual([
      id,
      id,
    ]);
    store.stop();
  });
  it('retries a failed same-contact target and reasserts it on explicit focus recovery after backend reset', async () => {
    const api = mock();
    let nativeTarget = '';
    api.target = vi
      .fn()
      .mockRejectedValueOnce(new Error('synthetic target unavailable'))
      .mockImplementation(async (value: string) => {
        nativeTarget = value;
      });
    const store = createAttachments(api);
    await store.select(id);
    expect(get(store.state).inputReady).toBe(false);
    expect(get(store.state).notice).toBe(
      'File input is unavailable for this contact.',
    );
    await store.select(id);
    expect(nativeTarget).toBe(id);
    expect(get(store.state).inputReady).toBe(true);
    nativeTarget = ''; // A missed security lifecycle hint must be recoverable.
    await store.reload();
    expect(nativeTarget).toBe(id);
    expect(get(store.state).inputReady).toBe(true);
    expect(vi.mocked(api.target).mock.calls.map(([value]) => value)).toEqual([
      id,
      id,
      id,
    ]);
    store.stop();
  });
  it('a held invalidation rebind cannot mark a replacement contact ready or rebind it after stop', async () => {
    const api = mock(),
      pending = deferred<void>(),
      other = 'E'.repeat(21) + 'A';
    let nativeTarget = '',
      calls = 0;
    api.list = vi.fn(async (selected) => ({
      ...snapshot(),
      contactID: selected,
    }));
    api.target = vi.fn(async (selected) => {
      if (++calls === 2) await pending.promise;
      nativeTarget = selected;
    });
    const store = createAttachments(api);
    await store.select(id);
    store.invalidate();
    await vi.waitFor(() => expect(api.target).toHaveBeenCalledTimes(2));
    const switching = store.select(other);
    expect(get(store.state).inputReady).toBe(false);
    pending.resolve();
    await switching;
    expect(nativeTarget).toBe(other);
    expect(get(store.state).contactID).toBe(other);
    expect(get(store.state).inputReady).toBe(true);
    store.stop();
    await vi.waitFor(() => expect(nativeTarget).toBe(''));
    expect(get(store.state).inputReady).toBe(false);
    expect(vi.mocked(api.target).mock.calls.map(([value]) => value)).toEqual([
      id,
      id,
      other,
      '',
    ]);
  });
  it('retires stale readiness throughout a held same-contact target acknowledgement after backend reset', async () => {
    const api = mock(),
      pending = deferred<void>();
    let nativeTarget = '',
      calls = 0;
    api.target = vi.fn(async (selected) => {
      if (++calls === 2) await pending.promise;
      nativeTarget = selected;
    });
    const store = createAttachments(api);
    await store.select(id);
    expect(get(store.state).inputReady).toBe(true);
    nativeTarget = '';
    const recovering = store.reload();
    expect(get(store.state).inputReady).toBe(false);
    await vi.waitFor(() => expect(api.target).toHaveBeenCalledTimes(2));
    expect(nativeTarget).toBe('');
    expect(get(store.state).inputReady).toBe(false);
    pending.resolve();
    await recovering;
    expect(nativeTarget).toBe(id);
    expect(get(store.state).inputReady).toBe(true);
    store.stop();
  });
  it('stopping while a native target rebind is held retires readiness and sends the final empty target', async () => {
    const api = mock(),
      pending = deferred<void>();
    let calls = 0,
      nativeTarget = '';
    api.target = vi.fn(async (selected) => {
      if (++calls === 2) await pending.promise;
      nativeTarget = selected;
    });
    const store = createAttachments(api);
    await store.select(id);
    store.invalidate();
    await vi.waitFor(() => expect(api.target).toHaveBeenCalledTimes(2));
    store.stop();
    pending.resolve();
    await vi.waitFor(() => expect(nativeTarget).toBe(''));
    await vi.waitFor(() => expect(api.target).toHaveBeenLastCalledWith(''));
    expect(get(store.state).contactID).toBe('');
    expect(get(store.state).inputReady).toBe(false);
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
  it('does not restore a delayed full image after the contact generation is retired', async () => {
    const api = mock(),
      pending = deferred<string>();
    api.preview = vi.fn(() => pending.promise);
    const store = createAttachments(api);
    await store.select(id);
    const viewing = store.preview({ ...item, mediaType: 'image/png' });
    store.stop();
    await store.select(id);
    pending.resolve('/local-media/' + token);
    await viewing;
    expect(get(store.state).image).toBeNull();
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
