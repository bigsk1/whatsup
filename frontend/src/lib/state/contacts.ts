import { writable } from 'svelte/store';
import {
  unavailableConversation,
  validMessageText,
  type ConversationSnapshot,
} from '../messaging';
import {
  contactsBridge,
  validateHint,
  type ContactSummary,
  type ContactsBridge,
  type MessagePage,
} from './bridge';

export interface ContactsState {
  contacts: ContactSummary[];
  selectedID: string;
  pages: Record<string, MessagePage>;
  drafts: Record<string, string>;
  loading: boolean;
  loaded: boolean;
  loadingPages: Record<string, boolean>;
  sending: Record<string, boolean>;
  connecting: Record<string, boolean>;
  forgetting: Record<string, boolean>;
  pausing: Record<string, boolean>;
  notices: Record<string, string>;
  notice: string;
}

const empty = (): ContactsState => ({
  contacts: [],
  selectedID: '',
  pages: {},
  drafts: {},
  loading: false,
  loaded: false,
  loadingPages: {},
  sending: {},
  connecting: {},
  forgetting: {},
  pausing: {},
  notices: {},
  notice: '',
});

export function selectedConversation(
  state: ContactsState,
): ConversationSnapshot & { paused: boolean } {
  const contact = state.contacts.find(
    (item) => item.contactID === state.selectedID,
  );
  if (!contact)
    return {
      ...unavailableConversation,
      state: 'none',
      messages: [],
      paused: false,
    };
  const { unread: _unread, ...summary } = contact;
  return {
    ...summary,
    messages: state.pages[contact.contactID]?.messages ?? [],
  };
}

export function contactLabel(
  contact: Pick<ContactSummary, 'localName' | 'peerDisplayName'>,
): string {
  return contact.localName || contact.peerDisplayName || 'Approved contact';
}

export function connectionLabel(state: ConversationSnapshot['state']): string {
  return (
    (
      {
        connected: 'Connected',
        connecting: 'Waiting for their device',
        'identity-changed': 'Identity could not be verified',
        'update-required':
          'Update required: update both devices to the latest WhatsUp preview.',
        'route-unavailable':
          'Approved connection route unavailable — choose Connect to retry',
        'authentication-failed': 'Authentication needs attention',
        failed: 'Connection unavailable',
        'capacity-reached': 'Local message storage full',
        'peer-capacity-reached': 'Contact message storage full',
        'peer-removed': 'This contact removed you',
        paused: 'Connection paused',
      } as Partial<Record<ConversationSnapshot['state'], string>>
    )[state] ?? 'Offline'
  );
}

export function createContacts(bridge: ContactsBridge = contactsBridge) {
  let current = empty();
  const state = writable(current);
  const publish = (update: Partial<ContactsState>) => {
    current = { ...current, ...update };
    state.set(current);
  };
  let enabled = false;
  let visible = true;
  let generation = 0;
  let listGeneration = 0;
  let unsubscribe: (() => void) | undefined;
  let refreshing: Promise<void> | undefined;
  // Nonzero only while a refresh loop can still observe refreshAgain.
  let activeRefresh = 0;
  let refreshTokens = 0;
  let refreshAgain = false;
  let scheduled = false;
  const removed = new Set<string>();
  const versions = new Map<string, number>();
  const operations = new Map<string, number>();
  const pageRequests = new Map<string, number>();
  const marked = new Map<string, string>();
  const has = (id: string) =>
    current.contacts.some((item) => item.contactID === id);
  const live = (epoch: number, id?: string, operation?: number) =>
    enabled &&
    epoch === generation &&
    (id === undefined ||
      (has(id) &&
        !removed.has(id) &&
        (operation === undefined || operation === (operations.get(id) ?? 0))));
  const setNotice = (id: string, notice: string) =>
    publish({ notices: { ...current.notices, [id]: notice } });

  function retireList() {
    listGeneration += 1;
    refreshing = undefined;
    activeRefresh = 0;
    refreshAgain = false;
  }

  function summary(value: ContactSummary): boolean {
    if (
      !enabled ||
      removed.has(value.contactID) ||
      value.revision < (versions.get(value.contactID) ?? 0)
    )
      return false;
    versions.set(value.contactID, value.revision);
    publish({
      contacts: current.contacts.map((item) =>
        item.contactID === value.contactID ? value : item,
      ),
    });
    return true;
  }

  function scheduleRefresh() {
    if (!enabled || scheduled) return;
    scheduled = true;
    const epoch = generation;
    void Promise.resolve().then(() => {
      if (epoch !== generation) return;
      scheduled = false;
      if (enabled) void reload();
    });
  }

  async function markVisibleRead(id: string) {
    if (!enabled || !visible || current.selectedID !== id) return;
    const message = current.pages[id]?.messages
      .slice()
      .reverse()
      .find((item) => item.direction === 'incoming');
    if (!message || marked.get(id) === message.id) return;
    const epoch = generation;
    const operation = operations.get(id) ?? 0;
    marked.set(id, message.id);
    try {
      const value = await bridge.markRead(id, message.id);
      if (live(epoch, id, operation) && summary(value)) void loadPage(id);
    } catch {
      if (live(epoch, id, operation)) marked.delete(id);
    }
  }

  async function loadPage(id: string, older = false) {
    if (!enabled || !has(id) || removed.has(id)) return;
    const before = current.pages[id];
    if (older && (!before?.hasMore || current.loadingPages[id])) return;
    const cursor = older ? before.nextCursor : '';
    const epoch = generation;
    const operation = operations.get(id) ?? 0;
    const request = (pageRequests.get(id) ?? 0) + 1;
    pageRequests.set(id, request);
    publish({ loadingPages: { ...current.loadingPages, [id]: true } });
    try {
      const page = await bridge.page(id, cursor, 100);
      if (!live(epoch, id, operation) || request !== pageRequests.get(id))
        return;
      if (page.revision < (versions.get(id) ?? 0)) return;
      if (
        older &&
        (current.pages[id] !== before || page.revision !== before.revision)
      ) {
        scheduleRefresh();
        return;
      }
      if (
        older &&
        page.hasMore &&
        (page.nextCursor === cursor ||
          BigInt(page.nextCursor) >= BigInt(cursor))
      )
        throw new Error('The history cursor did not advance.');
      const messages = older
        ? [...page.messages, ...before.messages]
        : page.messages;
      if (new Set(messages.map((item) => item.id)).size !== messages.length)
        throw new Error('The history page overlaps.');
      // Bound decrypted UI memory. The database retains all messages; reload
      // the newest page when this view's window is exhausted.
      if (
        messages.length > 2000 ||
        messages.reduce(
          (bytes, item) => bytes + new TextEncoder().encode(item.text).length,
          0,
        ) >
          8 * 1024 * 1024
      ) {
        setNotice(
          id,
          'This view has reached its history window. Reload the latest messages to continue.',
        );
        return;
      }
      publish({ pages: { ...current.pages, [id]: { ...page, messages } } });
      if (page.revision > (versions.get(id) ?? 0)) scheduleRefresh();
      void markVisibleRead(id);
    } catch {
      if (live(epoch, id, operation) && request === pageRequests.get(id)) {
        if (!older) {
          const pages = { ...current.pages };
          delete pages[id];
          publish({ pages });
          publish({
            contacts: current.contacts.map((item) =>
              item.contactID === id ? { ...item, canSend: false } : item,
            ),
          });
        }
        setNotice(
          id,
          'Conversation history could not be verified. Try again before sending a message.',
        );
      }
    } finally {
      if (live(epoch, id, operation) && request === pageRequests.get(id))
        publish({ loadingPages: { ...current.loadingPages, [id]: false } });
    }
  }

  async function reload() {
    if (!enabled) return;
    if (activeRefresh && refreshing) {
      refreshAgain = true;
      return refreshing;
    }
    const epoch = generation;
    const listEpoch = listGeneration;
    const currentList = () => live(epoch) && listEpoch === listGeneration;
    const token = (refreshTokens += 1);
    activeRefresh = token;
    const task = refreshLoop();
    refreshing = task;
    try {
      await task;
    } finally {
      if (refreshing === task) refreshing = undefined;
    }

    async function refreshLoop() {
      try {
        do {
          refreshAgain = false;
          publish({ loading: true });
          try {
            const values = await bridge.contacts();
            if (!currentList()) return;
            const contacts = values
              .filter((item) => !removed.has(item.contactID))
              .map((value) => {
                const existing = current.contacts.find(
                  (item) => item.contactID === value.contactID,
                );
                if (value.revision < (versions.get(value.contactID) ?? 0))
                  return existing;
                versions.set(value.contactID, value.revision);
                return value;
              })
              .filter((value): value is ContactSummary => value !== undefined);
            const ids = new Set(contacts.map((item) => item.contactID));
            const retain = <T>(values: Record<string, T>) =>
              Object.fromEntries(
                Object.entries(values).filter(([id]) => ids.has(id)),
              );
            const selectedID = ids.has(current.selectedID)
              ? current.selectedID
              : (contacts[0]?.contactID ?? '');
            publish({
              contacts,
              loaded: true,
              selectedID,
              pages: retain(current.pages),
              drafts: retain(current.drafts),
              notice: '',
            });
            if (
              selectedID &&
              (!current.pages[selectedID] ||
                current.pages[selectedID].revision <
                  (versions.get(selectedID) ?? 0))
            )
              await loadPage(selectedID);
            else if (selectedID) void markVisibleRead(selectedID);
          } catch {
            if (currentList()) {
              // Fail closed after malformed or unavailable summaries. Retain no
              // decrypted messages behind a composer with uncertain authority.
              publish({
                contacts: [],
                loaded: false,
                selectedID: '',
                pages: {},
                notice:
                  'Conversation status could not be verified. No message will be sent. Try again or disconnect and lock.',
              });
            }
          } finally {
            if (currentList()) publish({ loading: false });
          }
        } while (currentList() && refreshAgain);
      } finally {
        // Clear with no await after the last refreshAgain check, so a reload
        // arriving while this task settles starts a new pass instead of
        // joining a finished one.
        if (activeRefresh === token) activeRefresh = 0;
      }
    }
  }

  function start() {
    if (enabled) return reload();
    enabled = true;
    generation += 1;
    unsubscribe = bridge.subscribe((value) => {
      const hint = validateHint(value);
      if (
        !enabled ||
        !hint ||
        removed.has(hint.contactID) ||
        hint.revision <= (versions.get(hint.contactID) ?? -1)
      )
        return;
      scheduleRefresh();
    });
    return reload();
  }

  function stop() {
    enabled = false;
    generation += 1;
    listGeneration += 1;
    unsubscribe?.();
    unsubscribe = undefined;
    refreshing = undefined;
    activeRefresh = 0;
    refreshAgain = scheduled = false;
    removed.clear();
    versions.clear();
    operations.clear();
    pageRequests.clear();
    marked.clear();
    current = empty();
    state.set(current);
  }

  function select(id: string) {
    if (!enabled || !has(id) || removed.has(id)) return;
    publish({ selectedID: id });
    if (
      !current.pages[id] ||
      current.pages[id].revision < (versions.get(id) ?? 0)
    )
      void loadPage(id);
    else void markVisibleRead(id);
  }

  function apply(value: ConversationSnapshot): boolean {
    const id = value.contactID;
    if (
      !enabled ||
      !has(id) ||
      removed.has(id) ||
      value.revision < (versions.get(id) ?? 0)
    )
      return false;
    const previous = current.contacts.find((item) => item.contactID === id)!;
    const { messages, ...fields } = value;
    summary({
      ...fields,
      unread: previous.unread,
      paused: value.state === 'paused',
    });
    // Mutation snapshots are bounded recent history. A fresh page supplies a
    // real cursor rather than making one up from timestamps or message IDs.
    publish({
      pages: {
        ...current.pages,
        [id]: {
          contactID: id,
          revision: value.revision,
          messages,
          nextCursor: '',
          hasMore: false,
        },
      },
    });
    void loadPage(id);
    return true;
  }

  async function mutate(
    id: string,
    kind: 'sending' | 'connecting',
  ): Promise<boolean> {
    const contact = current.contacts.find((item) => item.contactID === id);
    const text = current.drafts[id] ?? '';
    if (
      !enabled ||
      !contact ||
      contact.paused ||
      current.pausing[id] ||
      current[kind][id] ||
      (kind === 'sending'
        ? !contact.canSend || !validMessageText(text)
        : !contact.canConnect)
    )
      return false;
    const epoch = generation;
    const operation = operations.get(id) ?? 0;
    publish({ [kind]: { ...current[kind], [id]: true } });
    setNotice(id, '');
    try {
      const value =
        kind === 'sending'
          ? await bridge.send(id, text)
          : await bridge.connect(id);
      if (!live(epoch, id, operation)) return false;
      apply(value);
      if (kind === 'sending' && current.drafts[id] === text)
        publish({ drafts: { ...current.drafts, [id]: '' } });
      // The backend accepted the operation even when a newer authoritative
      // event has already superseded its response snapshot.
      return true;
    } catch {
      if (live(epoch, id, operation))
        setNotice(
          id,
          kind === 'sending'
            ? 'The message could not be saved to the local queue. Your draft is still here; try again.'
            : 'The connection could not be started. Your saved messages remain on this device. Try Connect again.',
        );
      return false;
    } finally {
      if (live(epoch, id, operation))
        publish({ [kind]: { ...current[kind], [id]: false } });
    }
  }

  async function setPaused(id: string, paused: boolean): Promise<boolean> {
    const contact = current.contacts.find((item) => item.contactID === id);
    if (
      !enabled ||
      !contact ||
      contact.state === 'peer-removed' ||
      current.pausing[id] ||
      contact.paused === paused
    )
      return false;
    const epoch = generation;
    const operation = operations.get(id) ?? 0;
    publish({ pausing: { ...current.pausing, [id]: true } });
    setNotice(id, '');
    try {
      const value = await bridge.setPaused(id, paused);
      if (!live(epoch, id, operation)) return false;
      summary(value);
      return true;
    } catch {
      if (live(epoch, id, operation))
        setNotice(
          id,
          paused
            ? 'The connection could not be paused. Check its status and try again.'
            : 'The connection could not be resumed. Your saved history and drafts are kept. Try again.',
        );
      return false;
    } finally {
      if (live(epoch, id, operation))
        publish({ pausing: { ...current.pausing, [id]: false } });
    }
  }

  async function forget(id: string): Promise<boolean> {
    if (!enabled || !has(id) || current.forgetting[id]) return false;
    const epoch = generation;
    retireList();
    operations.set(id, (operations.get(id) ?? 0) + 1);
    removed.add(id);
    const without = <T>(values: Record<string, T>) => {
      const copy = { ...values };
      delete copy[id];
      return copy;
    };
    marked.delete(id);
    const contacts = current.contacts.filter((item) => item.contactID !== id);
    publish({
      contacts,
      selectedID:
        current.selectedID === id
          ? (contacts[0]?.contactID ?? '')
          : current.selectedID,
      drafts: without(current.drafts),
      pages: without(current.pages),
      notices: without(current.notices),
      sending: without(current.sending),
      connecting: without(current.connecting),
      loadingPages: without(current.loadingPages),
      forgetting: { ...current.forgetting, [id]: true },
    });
    try {
      await bridge.forget(id);
      if (!live(epoch)) return false;
      void reload();
      return true;
    } catch {
      // Storage uncertainty is resolved by the session controller's status
      // refresh; no pre-removal snapshot can restore the forgotten contact.
      if (live(epoch)) {
        removed.delete(id);
        retireList();
      }
      return false;
    } finally {
      if (live(epoch)) publish({ forgetting: without(current.forgetting) });
    }
  }

  async function reloadLatest() {
    const id = current.selectedID;
    if (enabled && id) {
      pageRequests.set(id, (pageRequests.get(id) ?? 0) + 1);
      const pages = { ...current.pages };
      delete pages[id];
      publish({ pages });
      setNotice(id, '');
    }
    await reload();
  }

  return {
    state,
    start,
    stop,
    reload,
    select,
    apply,
    forget,
    setPaused,
    send: (id = current.selectedID) => mutate(id, 'sending'),
    connect: (id = current.selectedID) => mutate(id, 'connecting'),
    loadOlder: () => loadPage(current.selectedID, true),
    reloadLatest,
    setDraft(id: string, text: string) {
      if (enabled && has(id))
        publish({ drafts: { ...current.drafts, [id]: text } });
    },
    dismissNotice(id = current.selectedID) {
      setNotice(id, '');
      publish({ notice: '' });
    },
    setVisible(value: boolean) {
      visible = value;
      if (value && enabled) void markVisibleRead(current.selectedID);
    },
  };
}
