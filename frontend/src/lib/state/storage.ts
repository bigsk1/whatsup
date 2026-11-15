import { writable } from 'svelte/store';
import { Events } from '@wailsio/runtime';
import { hasDesktopBridge } from '../backend';
import { validID, validateHint } from './bridge';

export interface ContactStorage {
  contactID: string;
  messagesUsedBytes: number;
  messagesQuotaBytes: number;
  attachmentsUsedBytes: number;
  attachmentsQuotaBytes: number;
}
export interface ContactStorageState {
  contactID: string;
  usage: ContactStorage | null;
  loading: boolean;
  notice: string;
}
export interface StorageBridge {
  get(id: string): Promise<unknown>;
  subscribe(callback: (hint: unknown) => void): () => void;
}
export function validateContactStorage(
  input: unknown,
  id: string,
): ContactStorage {
  if (!input || typeof input !== 'object' || Array.isArray(input))
    throw new Error('Invalid storage response.');
  const value = input as Record<string, unknown>;
  const fields = [
    'messagesUsedBytes',
    'messagesQuotaBytes',
    'attachmentsUsedBytes',
    'attachmentsQuotaBytes',
  ] as const;
  if (
    Object.keys(value).length !== 5 ||
    !validID(id) ||
    value.contactID !== id ||
    fields.some(
      (field) =>
        !Number.isSafeInteger(value[field]) || (value[field] as number) < 0,
    ) ||
    (value.messagesQuotaBytes as number) === 0 ||
    (value.attachmentsQuotaBytes as number) === 0
  )
    throw new Error('Invalid storage response.');
  return {
    contactID: id,
    messagesUsedBytes: value.messagesUsedBytes as number,
    messagesQuotaBytes: value.messagesQuotaBytes as number,
    attachmentsUsedBytes: value.attachmentsUsedBytes as number,
    attachmentsQuotaBytes: value.attachmentsQuotaBytes as number,
  };
}
const bridge: StorageBridge = {
  async get(id) {
    if (!hasDesktopBridge())
      throw new Error('Storage usage is available in the desktop app.');
    return (
      await import('../../../bindings/whatsup/internal/desktop/service')
    ).GetContactStorage(id);
  },
  subscribe(callback) {
    return Events.On('whatsup:contact-changed', (event) =>
      callback(event.data),
    );
  },
};
const empty = (): ContactStorageState => ({
  contactID: '',
  usage: null,
  loading: false,
  notice: '',
});

// Observe only while Contact settings is visible. Events are coalesced hints;
// the authoritative query and generation check provide the actual usage.
export function createContactStorage(api: StorageBridge = bridge) {
  let current = empty(),
    generation = 0,
    again = false;
  let stopEvents: (() => void) | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const state = writable(current);
  const publish = (update: Partial<ContactStorageState>) => {
    current = { ...current, ...update };
    state.set(current);
  };
  function schedule() {
    if (timer || !current.contactID) return;
    timer = setTimeout(() => {
      timer = undefined;
      void reload();
    }, 1000);
  }
  function stop() {
    generation++;
    stopEvents?.();
    stopEvents = undefined;
    if (timer) clearTimeout(timer);
    timer = undefined;
    again = false;
    current = empty();
    state.set(current);
  }
  async function reload() {
    const id = current.contactID,
      epoch = generation;
    if (!validID(id)) return;
    if (current.loading) {
      again = true;
      return;
    }
    publish({ loading: true, notice: '' });
    try {
      const usage = validateContactStorage(await api.get(id), id);
      if (epoch === generation) publish({ usage });
    } catch {
      if (epoch === generation)
        publish({
          usage: null,
          notice:
            'Storage usage could not be loaded. Check the selected contact and try again.',
        });
    } finally {
      if (epoch === generation) {
        publish({ loading: false });
        if (again) {
          again = false;
          schedule();
        }
      }
    }
  }
  function select(id: string) {
    if (id === current.contactID) return;
    stop();
    if (!validID(id)) return;
    publish({ contactID: id });
    stopEvents = api.subscribe((input) => {
      if (validateHint(input)?.contactID === current.contactID) schedule();
    });
    void reload();
  }
  return { state, select, reload, stop };
}
