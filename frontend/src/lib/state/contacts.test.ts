import { get } from 'svelte/store';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { createContacts, selectedConversation } from './contacts';
import {
  validateContacts,
  validateHint,
  validatePage,
  contactsBridge,
  validID,
  validMessageID,
  type ContactSummary,
  type ContactsBridge,
  type MessagePage,
} from './bridge';
import type { ConversationMessage, ConversationSnapshot } from '../messaging';

const desktop = vi.hoisted(() => ({ MarkRead: vi.fn() }));
vi.mock('../../../bindings/whatsup/internal/desktop/service', () => desktop);
afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

const first = 'AQEBAQEBAQEBAQEBAQEBAQ';
const second = 'AgICAgICAgICAgICAgICAg';
const third = 'AwMDAwMDAwMDAwMDAwMDAw';
const messageID = 'BAQEBAQEBAQEBAQEBAQEBA';
const otherMessageID = 'BQUFBQUFBQUFBQUFBQUFBQ';
const contact = (
  contactID = first,
  revision = 1,
  unread = 0,
): ContactSummary => ({
  peerProtocol: 0,
  attachmentsSupported: false,
  contactControlsSupported: false,
  state: 'offline',
  path: 'Unknown',
  contactID,
  revision,
  unread,
  peerFingerprint: 'b'.repeat(64),
  peerDisplayName: '',
  localName: '',
  canSend: true,
  canConnect: true,
});
const message = (id = messageID): ConversationMessage => ({
  id,
  direction: 'outgoing',
  text: 'Synthetic queued text',
  createdAt: '2026-11-14T12:00:00Z',
  delivered: false,
});
const page = (
  contactID = first,
  revision = 1,
  messages: ConversationMessage[] = [],
): MessagePage => ({
  contactID,
  revision,
  messages,
  nextCursor: '',
  hasMore: false,
});
const snapshot = (
  value: ContactSummary,
  messages: ConversationMessage[] = [],
): ConversationSnapshot => {
  const { unread: _unread, ...fields } = value;
  return { ...fields, messages };
};
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((finish) => {
    resolve = finish;
  });
  return { promise, resolve };
}
function fixture() {
  let hint: (value: unknown) => void = () => {};
  const stop = vi.fn();
  const bridge: ContactsBridge = {
    contacts: vi.fn(async () => [contact(), contact(second)]),
    page: vi.fn(async (id) => page(id)),
    markRead: vi.fn(async (id) => contact(id)),
    connect: vi.fn(async (id) => snapshot(contact(id, 2))),
    send: vi.fn(async (id) => snapshot(contact(id, 2), [message()])),
    forget: vi.fn(async () => ({
      peerProtocol: 0 as const,
      attachmentsSupported: false,
      contactControlsSupported: false,
      state: 'none' as const,
      path: 'Unknown' as const,
      revision: 2,
      contactID: '',
      peerFingerprint: '',
      peerDisplayName: '',
      localName: '',
      canSend: false,
      canConnect: false,
      messages: [],
    })),
    subscribe: vi.fn((callback) => {
      hint = callback;
      return stop;
    }),
  };
  return {
    bridge,
    controller: createContacts(bridge),
    hint: (value: unknown) => hint(value),
    stop,
  };
}

describe('contact and paging DTO boundaries', () => {
  it('marks a canonical zero legacy message ID read while rejecting zero contact IDs', async () => {
    const zero = 'A'.repeat(22);
    expect(validMessageID(zero)).toBe(true);
    expect(validID(zero)).toBe(false);
    vi.stubGlobal('window', { _wails: { environment: {} } });
    desktop.MarkRead.mockResolvedValue(contact());
    expect(await contactsBridge.markRead(first, zero)).toEqual(contact());
    expect(desktop.MarkRead).toHaveBeenCalledWith(first, zero);
    await expect(contactsBridge.markRead(zero, zero)).rejects.toThrow();
    expect(desktop.MarkRead).toHaveBeenCalledOnce();
  });
  it('accepts bounded summaries and pages but rejects extra fields, duplicate IDs and foreign pages', () => {
    expect(validateContacts([contact(), contact(second)])).toHaveLength(2);
    for (const value of [
      [contact(), contact()],
      Array.from({ length: 33 }, () => contact()),
      [{ ...contact(), messages: [] }],
      [{ ...contact(), unread: -1 }],
    ])
      expect(() => validateContacts(value)).toThrow();
    expect(
      validatePage(page(first, 1, [message()]), first).messages,
    ).toHaveLength(1);
    for (const value of [
      page(second),
      { ...page(), nextCursor: '../private' },
      { ...page(), nextCursor: '1' },
      { ...page(), hasMore: true },
      {
        ...page(),
        messages: [{ ...message(), endpoint: 'synthetic-private' }],
      },
    ])
      expect(() => validatePage(value, first)).toThrow();
    expect(validateHint({ contactID: first, revision: 2 })).toEqual({
      contactID: first,
      revision: 2,
    });
    expect(
      validateHint({ contactID: first, revision: 2, text: 'hidden' }),
    ).toBeNull();
  });
});

describe('event-driven contact state', () => {
  it('never drops a reload requested while a finished refresh is settling', async () => {
    for (let delay = 0; delay < 24; delay += 1) {
      const { bridge, controller } = fixture();
      await controller.start();
      const before = vi.mocked(bridge.contacts).mock.calls.length;
      const list = deferred<ContactSummary[]>();
      vi.mocked(bridge.contacts).mockReturnValueOnce(list.promise);
      const refresh = controller.reload();
      list.resolve([contact(), contact(second)]);
      for (let tick = 0; tick < delay; tick += 1) await Promise.resolve();
      const later = controller.reload();
      await Promise.all([refresh, later]);
      expect(
        vi.mocked(bridge.contacts).mock.calls.length - before,
        `reload after ${delay} microtasks`,
      ).toBe(2);
      controller.stop();
    }
  });
  it('restores an unchanged contact only from a post-failure read after Forget fails, and retires older responses', async () => {
    const { bridge, controller } = fixture();
    await controller.start();
    controller.setDraft(first, 'Discarded before uncertain Forget');
    controller.setDraft(second, 'Other draft is preserved');
    const oldList = deferred<ContactSummary[]>();
    vi.mocked(bridge.contacts).mockReturnValueOnce(oldList.promise);
    const oldRefresh = controller.reload();
    const oldSend = deferred<ConversationSnapshot>();
    vi.mocked(bridge.send).mockReturnValueOnce(oldSend.promise);
    const oldMutation = controller.send(first);
    const otherSend = deferred<ConversationSnapshot>();
    vi.mocked(bridge.send).mockReturnValueOnce(otherSend.promise);
    const otherMutation = controller.send(second);
    vi.mocked(bridge.forget).mockRejectedValueOnce(
      new Error('SYNTHETIC-PRIVATE-FAILURE'),
    );
    expect(await controller.forget(first)).toBe(false);
    expect(
      get(controller.state).contacts.map((item) => item.contactID),
    ).toEqual([second]);
    // A new authoritative read must not wait for or accept the held old read.
    await controller.reload();
    expect(
      get(controller.state).contacts.map((item) => item.contactID),
    ).toEqual([first, second]);
    expect(get(controller.state).drafts).toEqual({
      [second]: 'Other draft is preserved',
    });
    expect(get(controller.state).sending[first]).toBeUndefined();
    expect(get(controller.state).sending[second]).toBe(true);
    controller.select(first);
    await vi.waitFor(() =>
      expect(get(controller.state).pages[first]).toBeDefined(),
    );
    oldSend.resolve(snapshot(contact(first, 99), [message()]));
    oldList.resolve([]);
    await Promise.all([oldRefresh, oldMutation]);
    expect(
      get(controller.state).contacts.map((item) => item.contactID),
    ).toEqual([first, second]);
    expect(get(controller.state).contacts[0].revision).toBe(1);
    expect(get(controller.state).pages[first].messages).toEqual([]);
    expect(get(controller.state).drafts[second]).toBe(
      'Other draft is preserved',
    );
    controller.setDraft(first, 'Fresh message after recovery');
    await controller.send(first);
    expect(bridge.send).toHaveBeenLastCalledWith(
      first,
      'Fresh message after recovery',
    );
    expect(get(controller.state).sending[first]).toBe(false);
    expect(get(controller.state).drafts[first]).toBe('');
    expect(get(controller.state).sending[second]).toBe(true);
    otherSend.resolve(snapshot(contact(second, 2), [message()]));
    await otherMutation;
    expect(JSON.stringify(get(controller.state))).not.toContain(
      'SYNTHETIC-PRIVATE-FAILURE',
    );
    controller.stop();
  });
  it('subscribes before the first snapshot, coalesces hints, ignores duplicates, and uses no timed polling', async () => {
    const { bridge, controller, hint } = fixture();
    await controller.start();
    expect(
      vi.mocked(bridge.subscribe).mock.invocationCallOrder[0],
    ).toBeLessThan(vi.mocked(bridge.contacts).mock.invocationCallOrder[0]);
    expect(selectedConversation(get(controller.state)).contactID).toBe(first);
    hint({ contactID: first, revision: 1 });
    hint({ contactID: first, revision: 0 });
    await Promise.resolve();
    expect(bridge.contacts).toHaveBeenCalledTimes(1);
    vi.mocked(bridge.contacts).mockResolvedValue([
      contact(first, 4),
      contact(second),
    ]);
    hint({ contactID: first, revision: 4 });
    hint({ contactID: first, revision: 3 });
    await vi.waitFor(() =>
      expect(get(controller.state).contacts[0].revision).toBe(4),
    );
    expect(bridge.contacts).toHaveBeenCalledTimes(2);
    controller.stop();
  });

  it('keeps per-contact drafts, send state and results separate across selection changes', async () => {
    const { bridge, controller } = fixture();
    await controller.start();
    controller.setDraft(first, 'First draft');
    const held = deferred<ConversationSnapshot>();
    vi.mocked(bridge.send).mockReturnValueOnce(held.promise);
    const sending = controller.send(first);
    controller.select(second);
    controller.setDraft(second, 'Second draft');
    expect(get(controller.state).sending[first]).toBe(true);
    expect(get(controller.state).sending[second]).toBeUndefined();
    held.resolve(snapshot(contact(first, 2), [message()]));
    await sending;
    expect(get(controller.state).selectedID).toBe(second);
    expect(get(controller.state).drafts).toEqual({
      [first]: '',
      [second]: 'Second draft',
    });
    expect(bridge.send).toHaveBeenCalledWith(first, 'First draft');
    controller.stop();
  });

  it('clears decrypted state on lock and rejects old send, page and list responses after a new unlock', async () => {
    const { bridge, controller, stop } = fixture();
    await controller.start();
    controller.setDraft(first, 'Discard on lock');
    const held = deferred<ConversationSnapshot>();
    vi.mocked(bridge.send).mockReturnValueOnce(held.promise);
    const sending = controller.send(first);
    controller.stop();
    vi.mocked(bridge.contacts).mockResolvedValue([contact(third)]);
    await controller.start();
    held.resolve(snapshot(contact(first, 99), [message()]));
    await sending;
    expect(
      get(controller.state).contacts.map((item) => item.contactID),
    ).toEqual([third]);
    expect(get(controller.state).drafts).toEqual({});
    expect(get(controller.state).pages[first]).toBeUndefined();
    expect(stop).toHaveBeenCalledOnce();
    controller.stop();
  });

  it('ignores an old summary after a newer mutation and never downgrades a delivery acknowledgement', async () => {
    const { bridge, controller } = fixture();
    await controller.start();
    const held = deferred<ContactSummary[]>();
    vi.mocked(bridge.contacts).mockReturnValueOnce(held.promise);
    const refresh = controller.reload();
    const delivered = { ...message(), delivered: true };
    vi.mocked(bridge.page).mockResolvedValue(page(first, 8, [delivered]));
    controller.apply(snapshot(contact(first, 8), [delivered]));
    held.resolve([contact(first, 2), contact(second)]);
    await refresh;
    expect(get(controller.state).contacts[0].revision).toBe(8);
    expect(
      selectedConversation(get(controller.state)).messages[0].delivered,
    ).toBe(true);
    controller.stop();
  });

  it('paginates in sequence order, accepts hidden-only pages, and rejects cursor loops or stale stitching', async () => {
    const { bridge, controller } = fixture();
    vi.mocked(bridge.page).mockResolvedValueOnce({
      ...page(first, 1, [message()]),
      nextCursor: '20',
      hasMore: true,
    });
    await controller.start();
    vi.mocked(bridge.page).mockResolvedValueOnce({
      ...page(),
      nextCursor: '10',
      hasMore: true,
    });
    await controller.loadOlder();
    expect(get(controller.state).pages[first].nextCursor).toBe('10');
    vi.mocked(bridge.page).mockResolvedValueOnce(
      page(first, 1, [message(otherMessageID)]),
    );
    await controller.loadOlder();
    expect(
      selectedConversation(get(controller.state)).messages.map(
        (item) => item.id,
      ),
    ).toEqual([otherMessageID, messageID]);
    expect(bridge.page).toHaveBeenLastCalledWith(first, '10', 100);
    controller.stop();
  });

  it('forgets only its target and cannot resurrect it with delayed snapshots or events', async () => {
    const { bridge, controller, hint } = fixture();
    await controller.start();
    controller.setDraft(first, 'First');
    controller.setDraft(second, 'Second');
    const held = deferred<ConversationSnapshot>();
    vi.mocked(bridge.forget).mockReturnValueOnce(held.promise);
    const forgetting = controller.forget(first);
    expect(get(controller.state).selectedID).toBe(second);
    expect(get(controller.state).drafts).toEqual({ [second]: 'Second' });
    held.resolve({ ...snapshot(contact()), state: 'none', contactID: '' });
    expect(await forgetting).toBe(true);
    hint({ contactID: first, revision: 100 });
    await Promise.resolve();
    expect(
      get(controller.state).contacts.map((item) => item.contactID),
    ).toEqual([second]);
    expect(get(controller.state).drafts[second]).toBe('Second');
    controller.stop();
  });

  it('marks only the visible selected incoming message read, and does not create a read/event loop', async () => {
    const { bridge, controller } = fixture();
    vi.mocked(bridge.contacts).mockResolvedValue([
      contact(first, 1, 2),
      contact(second, 1, 4),
    ]);
    vi.mocked(bridge.page).mockImplementation(async (id) =>
      page(id, 2, [{ ...message(), direction: 'incoming', delivered: true }]),
    );
    vi.mocked(bridge.markRead).mockImplementation(async (id) =>
      contact(id, 2, 0),
    );
    await controller.start();
    await vi.waitFor(() =>
      expect(get(controller.state).contacts[0].unread).toBe(0),
    );
    expect(get(controller.state).contacts[1].unread).toBe(4);
    expect(bridge.markRead).toHaveBeenCalledTimes(1);
    controller.setVisible(false);
    controller.select(second);
    await vi.waitFor(() =>
      expect(get(controller.state).pages[second]).toBeDefined(),
    );
    expect(bridge.markRead).toHaveBeenCalledTimes(1);
    controller.stop();
  });

  it('keeps a failed queue draft and never exposes raw backend errors', async () => {
    const { bridge, controller } = fixture();
    await controller.start();
    vi.mocked(bridge.send).mockRejectedValue(
      new Error('SYNTHETIC-PRIVATE-ENDPOINT'),
    );
    controller.setDraft(first, 'Keep draft');
    await controller.send();
    expect(get(controller.state).drafts[first]).toBe('Keep draft');
    expect(get(controller.state).notices[first]).toContain(
      'could not be saved',
    );
    expect(JSON.stringify(get(controller.state))).not.toContain(
      'SYNTHETIC-PRIVATE-ENDPOINT',
    );
    controller.stop();
  });
});
