import { writable } from 'svelte/store';
import { Events } from '@wailsio/runtime';
import { hasDesktopBridge } from '../backend';
import { validID, validateHint } from './bridge';
import { mediaURL } from './attachments';

export interface AvatarSnapshot {
  revision: number;
  ownURL: string;
  shareAvatar: boolean;
  showContactPhotos: boolean;
  contacts: { contactID: string; url: string }[];
}
export interface AvatarState {
  snapshot: AvatarSnapshot | null;
  loading: boolean;
  busy: boolean;
  notice: string;
}
export interface AvatarBridge {
  get(): Promise<unknown>;
  choose(): Promise<unknown>;
  remove(): Promise<unknown>;
  preferences(share: boolean, show: boolean): Promise<unknown>;
  subscribe(callback: (hint: unknown) => void): () => void;
}

export function validateAvatars(input: unknown): AvatarSnapshot {
  const invalid = () => {
    throw new Error('Invalid profile picture response.');
  };
  if (!input || typeof input !== 'object' || Array.isArray(input))
    return invalid();
  const value = input as Record<string, unknown>;
  if (
    Object.keys(value).length !== 5 ||
    !Number.isSafeInteger(value.revision) ||
    (value.revision as number) < 0 ||
    (value.ownURL !== '' && !mediaURL(value.ownURL)) ||
    typeof value.shareAvatar !== 'boolean' ||
    typeof value.showContactPhotos !== 'boolean' ||
    !Array.isArray(value.contacts) ||
    value.contacts.length > 32
  )
    return invalid();
  const seen = new Set<string>();
  const contacts = value.contacts.map((input) => {
    if (!input || typeof input !== 'object' || Array.isArray(input))
      return invalid();
    const contact = input as Record<string, unknown>;
    if (
      Object.keys(contact).length !== 2 ||
      !validID(contact.contactID) ||
      (contact.url !== '' && !mediaURL(contact.url)) ||
      (!value.showContactPhotos && contact.url !== '') ||
      seen.has(contact.contactID)
    )
      return invalid();
    seen.add(contact.contactID);
    return { contactID: contact.contactID, url: contact.url as string };
  });
  return {
    revision: value.revision as number,
    ownURL: value.ownURL as string,
    shareAvatar: value.shareAvatar,
    showContactPhotos: value.showContactPhotos,
    contacts,
  };
}

interface AvatarService {
  GetAvatarState(): Promise<unknown>;
  ChooseProfileAvatar(): Promise<unknown>;
  RemoveProfileAvatar(): Promise<unknown>;
  SetAvatarPreferences(share: boolean, show: boolean): Promise<unknown>;
}
async function service(): Promise<AvatarService> {
  if (!hasDesktopBridge())
    throw new Error('Profile pictures require the desktop app.');
  return await import('../../../bindings/whatsup/internal/desktop/service');
}
const bridge: AvatarBridge = {
  async get() {
    return (await service()).GetAvatarState();
  },
  async choose() {
    return (await service()).ChooseProfileAvatar();
  },
  async remove() {
    return (await service()).RemoveProfileAvatar();
  },
  async preferences(share, show) {
    return (await service()).SetAvatarPreferences(share, show);
  },
  subscribe(callback) {
    return Events.On('whatsup:contact-changed', (event) =>
      callback(event.data),
    );
  },
};
const empty = (): AvatarState => ({
  snapshot: null,
  loading: false,
  busy: false,
  notice: '',
});

// Only opaque local capabilities enter this state. Peer hints never supply an
// image URL; they ask for a fresh authenticated, bounded backend snapshot.
export function createAvatars(api: AvatarBridge = bridge) {
  let current = empty(),
    generation = 0,
    read = 0,
    enabled = false;
  let ids: string[] = [],
    again = false;
  let mutationFeedback = false;
  let unsubscribe: (() => void) | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const revisions = new Map<string, number>();
  const state = writable(current);
  const publish = (value: Partial<AvatarState>) => {
    current = { ...current, ...value };
    state.set(current);
  };
  const apply = (snapshot: AvatarSnapshot) => {
    if (current.snapshot && snapshot.revision < current.snapshot.revision)
      return false;
    publish({
      snapshot: {
        ...snapshot,
        contacts: snapshot.contacts.filter((contact) =>
          ids.includes(contact.contactID),
        ),
      },
    });
    return true;
  };
  function schedule() {
    if (timer || !enabled) return;
    timer = setTimeout(() => {
      timer = undefined;
      void reload();
    }, 250);
  }
  function stop() {
    generation++;
    read++;
    enabled = false;
    ids = [];
    again = false;
    unsubscribe?.();
    unsubscribe = undefined;
    if (timer) clearTimeout(timer);
    timer = undefined;
    revisions.clear();
    mutationFeedback = false;
    current = empty();
    state.set(current);
  }
  async function reload(manual = false) {
    if (!enabled) return;
    if (manual) {
      mutationFeedback = false;
      publish({ notice: '' });
    }
    if (current.loading || current.busy) {
      again = true;
      return;
    }
    const epoch = generation,
      request = ++read;
    // Contact hints and focus recovery may arrive immediately after a picker
    // response. Keep the owner's action feedback until another explicit action;
    // background reads must not erase or replace it with an unrelated notice.
    publish({ loading: true, ...(mutationFeedback ? {} : { notice: '' }) });
    try {
      const snapshot = validateAvatars(await api.get());
      if (enabled && epoch === generation && request === read) apply(snapshot);
    } catch {
      if (
        enabled &&
        epoch === generation &&
        request === read &&
        !mutationFeedback
      )
        publish({ notice: 'Profile pictures could not be loaded. Try again.' });
    } finally {
      if (epoch === generation && request === read) {
        publish({ loading: false });
        if (again) {
          again = false;
          schedule();
        }
      }
    }
  }
  function scope(active: boolean, contacts: string[]) {
    if (!active) {
      if (enabled || current.snapshot) stop();
      return;
    }
    const next = contacts.filter(validID).slice(0, 32).sort();
    if (enabled && next.join(',') === ids.join(',')) return;
    generation++;
    read++;
    ids = next;
    again = false;
    if (timer) clearTimeout(timer);
    timer = undefined;
    publish({
      loading: false,
      busy: false,
      snapshot: current.snapshot
        ? {
            ...current.snapshot,
            contacts: current.snapshot.contacts.filter((contact) =>
              ids.includes(contact.contactID),
            ),
          }
        : null,
    });
    if (!enabled) {
      enabled = true;
      unsubscribe = api.subscribe((input) => {
        const hint = validateHint(input);
        if (
          !hint ||
          !ids.includes(hint.contactID) ||
          hint.revision <= (revisions.get(hint.contactID) ?? -1)
        )
          return;
        revisions.set(hint.contactID, hint.revision);
        schedule();
      });
    }
    for (const id of revisions.keys())
      if (!ids.includes(id)) revisions.delete(id);
    void reload();
  }
  async function mutate(operation: () => Promise<unknown>, success: string) {
    if (!enabled || current.busy || current.loading || !current.snapshot)
      return;
    const epoch = generation;
    read++;
    mutationFeedback = false;
    publish({ busy: true, notice: '' });
    try {
      const snapshot = validateAvatars(await operation());
      if (enabled && epoch === generation && apply(snapshot)) {
        mutationFeedback = true;
        publish({ notice: success });
      }
    } catch {
      if (enabled && epoch === generation) {
        mutationFeedback = true;
        publish({
          notice:
            'The profile picture could not be changed. Use a still PNG, JPEG or WebP image within the supported size, then try again.',
        });
      }
    } finally {
      if (epoch === generation) {
        publish({ busy: false });
        if (again) {
          again = false;
          schedule();
        }
      }
    }
  }
  const choose = () =>
    mutate(() => api.choose(), 'Profile picture saved on this device.');
  const remove = () => mutate(() => api.remove(), 'Profile picture removed.');
  const preferences = (share: boolean, show: boolean) =>
    mutate(
      () => api.preferences(share, show),
      'Picture preferences saved on this device.',
    );
  return { state, scope, reload, choose, remove, preferences, stop };
}
