import { writable } from 'svelte/store';
import { Events } from '@wailsio/runtime';
import { hasDesktopBridge } from '../backend';
import { validID, validateHint } from './bridge';

export interface AttachmentDraft {
  handle: string;
  name: string;
  mediaType: string;
  size: number;
  previewURL: string;
  requiresRelayWarning: boolean;
}
export interface AttachmentItem {
  id: string;
  name: string;
  mediaType: string;
  size: number;
  direction: 'incoming' | 'outgoing';
  state: string;
  transferredBytes: number;
  previewURL: string;
  warning: string;
}
export interface AttachmentSnapshot {
  contactID: string;
  revision: number;
  items: AttachmentItem[];
  drafts: AttachmentDraft[];
  autoAcceptImages: boolean;
  nextCursor: string;
  hasMore: boolean;
}
export interface AttachmentBridge {
  list(id: string): Promise<unknown>;
  history(id: string, cursor: string): Promise<unknown>;
  target(id: string): Promise<void>;
  pick(id: string, original: boolean): Promise<unknown>;
  paste(id: string): Promise<unknown>;
  send(id: string, handle: string, allow: boolean): Promise<void>;
  discard(id: string, handle: string): Promise<void>;
  decision(id: string, file: string, action: string): Promise<void>;
  autoAccept(id: string, enabled: boolean): Promise<void>;
  preview(id: string, file: string): Promise<string>;
  save(id: string, file: string): Promise<unknown>;
  reveal(id: string, handle: string): Promise<void>;
  subscribe(callback: (hint: unknown) => void): () => void;
}
const object = (value: unknown): Record<string, unknown> => {
  if (!value || typeof value !== 'object' || Array.isArray(value))
    throw new Error('Invalid attachment response.');
  return value as Record<string, unknown>;
};
const handle = (value: unknown): value is string =>
  typeof value === 'string' && /^[A-Za-z0-9_-]{32}$/.test(value);
export const mediaURL = (value: unknown): value is string =>
  typeof value === 'string' && /^\/local-media\/[A-Za-z0-9_-]{32}$/.test(value);
function metadata(value: Record<string, unknown>) {
  if (
    typeof value.name !== 'string' ||
    value.name.length < 1 ||
    value.name.length > 255 ||
    /[\u0000-\u001f\u007f<>\/\\]/.test(value.name) ||
    typeof value.mediaType !== 'string' ||
    !/^[a-z0-9!#$&^_.+-]+\/[a-z0-9!#$&^_.+-]+$/.test(value.mediaType) ||
    !Number.isSafeInteger(value.size) ||
    (value.size as number) < 0 ||
    (value.size as number) > 2 ** 31 ||
    (value.previewURL !== '' && !mediaURL(value.previewURL))
  )
    throw new Error('Invalid attachment response.');
}
export function validateAttachmentDraft(input: unknown): AttachmentDraft {
  const value = object(input);
  metadata(value);
  if (
    Object.keys(value).length !== 6 ||
    !handle(value.handle) ||
    typeof value.requiresRelayWarning !== 'boolean'
  )
    throw new Error('Invalid attachment response.');
  return {
    handle: value.handle,
    name: value.name as string,
    mediaType: value.mediaType as string,
    size: value.size as number,
    previewURL: value.previewURL as string,
    requiresRelayWarning: value.requiresRelayWarning,
  };
}
export function validateAttachments(
  input: unknown,
  id: string,
): AttachmentSnapshot {
  const value = object(input);
  if (
    Object.keys(value).length !== 7 ||
    value.contactID !== id ||
    !validID(id) ||
    !Number.isSafeInteger(value.revision) ||
    (value.revision as number) < 0 ||
    typeof value.hasMore !== 'boolean' ||
    typeof value.nextCursor !== 'string' ||
    value.hasMore !== (value.nextCursor !== '') ||
    (value.nextCursor !== '' &&
      (!/^[1-9][0-9]{0,18}$/.test(value.nextCursor) ||
        BigInt(value.nextCursor) > 9223372036854775807n)) ||
    typeof value.autoAcceptImages !== 'boolean' ||
    !Array.isArray(value.items) ||
    value.items.length > 256 ||
    !Array.isArray(value.drafts) ||
    value.drafts.length > 4
  )
    throw new Error('Invalid attachment response.');
  const items = value.items.map((input): AttachmentItem => {
    const record = object(input);
    metadata(record);
    if (
      Object.keys(record).length !== 9 ||
      !validID(record.id) ||
      !['incoming', 'outgoing'].includes(record.direction as string) ||
      !['', 'relay-approval'].includes(record.warning as string) ||
      ![
        'offered',
        'queued',
        'accepted',
        'paused',
        'complete',
        'declined',
        'relay-warning',
        'failed',
        'sending',
        'receiving',
      ].includes(record.state as string) ||
      !Number.isSafeInteger(record.transferredBytes) ||
      (record.transferredBytes as number) < 0 ||
      (record.transferredBytes as number) > (record.size as number)
    )
      throw new Error('Invalid attachment response.');
    return {
      id: record.id,
      name: record.name as string,
      mediaType: record.mediaType as string,
      size: record.size as number,
      direction: record.direction as 'incoming' | 'outgoing',
      state: record.state as string,
      transferredBytes: record.transferredBytes as number,
      previewURL: record.previewURL as string,
      warning: record.warning as string,
    };
  });
  const drafts = value.drafts.map(validateAttachmentDraft);
  if (
    new Set(items.map((item) => item.id)).size !== items.length ||
    new Set(drafts.map((item) => item.handle)).size !== drafts.length
  )
    throw new Error('Invalid attachment response.');
  return {
    contactID: id,
    revision: value.revision as number,
    items,
    drafts,
    autoAcceptImages: value.autoAcceptImages,
    nextCursor: value.nextCursor as string,
    hasMore: value.hasMore as boolean,
  };
}
async function service() {
  if (!hasDesktopBridge())
    throw new Error('Open the desktop app to use attachments.');
  return import('../../../bindings/whatsup/internal/desktop/service');
}
const bridge: AttachmentBridge = {
  async list(id) {
    return (await service()).GetAttachments(id);
  },
  async history(id, cursor) {
    return (await service()).GetAttachmentHistory(id, cursor);
  },
  async target(id) {
    await (await service()).SetAttachmentInputContact(id);
  },
  async pick(id, original) {
    return (await service()).ChooseAttachment(id, original);
  },
  async paste(id) {
    return (await service()).PasteAttachment(id);
  },
  async send(id, handle, allow) {
    await (await service()).SendAttachment(id, handle, allow);
  },
  async discard(id, handle) {
    await (await service()).DiscardAttachment(id, handle);
  },
  async decision(id, file, action) {
    const native = await service();
    switch (action) {
      case 'accept':
        await native.AcceptAttachment(id, file);
        break;
      case 'decline':
        await native.DeclineAttachment(id, file);
        break;
      case 'cancel':
        await native.CancelAttachment(id, file);
        break;
      case 'withdraw':
        await native.WithdrawAttachment(id, file);
        break;
      case 'resume':
        await native.ResumeAttachment(id, file);
        break;
      case 'allow-relay':
        await native.AllowAttachmentRelay(id, file);
        break;
      default:
        throw new Error('Invalid attachment action.');
    }
  },
  async autoAccept(id, enabled) {
    await (await service()).SetAutoAcceptImages(id, enabled);
  },
  async preview(id, file) {
    return (await service()).PreviewAttachment(id, file);
  },
  async save(id, file) {
    return (await service()).SaveAttachment(id, file);
  },
  async reveal(id, handle) {
    await (await service()).ShowSavedAttachment(id, handle);
  },
  subscribe(callback) {
    return Events.On('whatsup:contact-changed', (event) =>
      callback(event.data),
    );
  },
};

const empty = () => ({
  contactID: '',
  items: [] as AttachmentItem[],
  drafts: [] as AttachmentDraft[],
  autoAcceptImages: false,
  nextCursor: '',
  hasMore: false,
  loading: false,
  busy: false,
  notice: '',
  videos: {} as Record<string, string>,
  image: null as { id: string; name: string; url: string } | null,
  saved: {} as Record<string, string>,
});
export function createAttachments(api: AttachmentBridge = bridge) {
  let wireAllowed = true,
    stageAllowed = true;
  let current = empty();
  const state = writable(current);
  const publish = (update: Partial<typeof current>) => {
    current = { ...current, ...update };
    state.set(current);
  };
  let generation = 0,
    revision = 0,
    stop: (() => void) | undefined,
    timer: ReturnType<typeof setTimeout> | undefined;
  let imageGeneration = 0;
  let loading = false,
    again = false;
  let desiredTarget = '',
    targetVersion = 0;
  let targetTail: Promise<void> = Promise.resolve();
  function setTarget(id: string): Promise<void> {
    desiredTarget = id;
    const version = ++targetVersion;
    const pending = targetTail
      .catch(() => {})
      .then(async () => {
        if (version !== targetVersion) return;
        await api.target(desiredTarget);
      });
    targetTail = pending;
    return pending;
  }
  function stopRequests() {
    generation++;
    if (timer) clearTimeout(timer);
    timer = undefined;
    loading = false;
    again = false;
    revision = 0;
  }
  async function reload() {
    if (!current.contactID) return;
    if (loading) {
      again = true;
      return;
    }
    const id = current.contactID,
      epoch = generation;
    loading = true;
    publish({ loading: true });
    try {
      const value = validateAttachments(await api.list(id), id);
      if (epoch !== generation) return;
      if (value.revision >= revision) {
        revision = value.revision;
        publish({
          items: value.items,
          drafts: value.drafts,
          autoAcceptImages: value.autoAcceptImages,
          nextCursor: value.nextCursor,
          hasMore: value.hasMore,
        });
      }
    } catch (error) {
      if (epoch === generation)
        publish({
          notice:
            error instanceof Error
              ? error.message
              : 'Attachments could not be loaded.',
        });
    } finally {
      if (epoch === generation) {
        loading = false;
        publish({ loading: false });
        if (again) {
          again = false;
          schedule();
        }
      }
    }
  }
  function schedule() {
    if (!timer)
      timer = setTimeout(() => {
        timer = undefined;
        void reload();
      }, 150);
  }
  async function select(id: string) {
    if (id === current.contactID) return;
    stopRequests();
    current = empty();
    current.contactID = id;
    state.set(current);
    const epoch = generation;
    if (!id) {
      void setTarget('').catch(() => {});
      return;
    }
    if (!validID(id)) {
      current = empty();
      state.set(current);
      return;
    }
    try {
      await setTarget(id);
      if (epoch === generation) void reload();
    } catch {
      if (epoch === generation)
        publish({ notice: 'File input is unavailable for this contact.' });
    }
  }
  async function action(
    run: (id: string) => Promise<void>,
    reloadAfter = true,
  ) {
    const id = current.contactID,
      epoch = generation;
    if (!validID(id) || current.busy) return;
    publish({ busy: true, notice: '' });
    try {
      await run(id);
      if (reloadAfter && epoch === generation) await reload();
    } catch (error) {
      if (epoch === generation)
        publish({
          notice:
            error instanceof Error
              ? error.message
              : 'The file action could not be completed.',
        });
    } finally {
      if (epoch === generation) publish({ busy: false });
    }
  }
  return {
    state,
    select,
    reload,
    setConnection(state: string, peerProtocol: number) {
      stageAllowed = state !== 'peer-removed';
      wireAllowed =
        stageAllowed && !(state === 'connected' && peerProtocol === 3);
    },
    older() {
      return action(async (id) => {
        const cursor = current.nextCursor,
          epoch = generation;
        if (!cursor) return;
        const page = validateAttachments(await api.history(id, cursor), id);
        if (
          epoch !== generation ||
          page.revision < revision ||
          current.nextCursor !== cursor
        )
          return;
        const keep = new Map(current.items.map((item) => [item.id, item]));
        for (const item of page.items)
          if (!keep.has(item.id)) keep.set(item.id, item);
        publish({
          items: [...keep.values()].slice(-512),
          nextCursor: page.nextCursor,
          hasMore: page.hasMore,
        });
      }, false);
    },
    start() {
      if (!stop)
        stop = api.subscribe((input) => {
          const hint = validateHint(input);
          if (hint?.contactID === current.contactID && hint.revision > revision)
            schedule();
        });
    },
    invalidate() {
      const id = current.contactID;
      stopRequests();
      current = empty();
      current.contactID = id;
      state.set(current);
      void reload();
    },
    stop() {
      stop?.();
      stop = undefined;
      stopRequests();
      current = empty();
      state.set(current);
      if (targetVersion !== 0) void setTarget('').catch(() => {});
    },
    pick(original = false) {
      if (!stageAllowed) return Promise.resolve();
      return action(async (id) => {
        await api.pick(id, original);
      });
    },
    paste() {
      if (!stageAllowed) return Promise.resolve();
      return action(async (id) => {
        await api.paste(id);
      });
    },
    send(draft: AttachmentDraft, allow: boolean) {
      if (!wireAllowed) {
        publish({
          notice:
            'Attachments cannot be sent on this connection. Local drafts and saved files are kept.',
        });
        return Promise.resolve();
      }
      return action((id) => api.send(id, draft.handle, allow));
    },
    discard(draft: AttachmentDraft) {
      return action((id) => api.discard(id, draft.handle));
    },
    decision(item: AttachmentItem, kind: string) {
      if (!wireAllowed && ['accept', 'resume', 'allow-relay'].includes(kind)) {
        publish({
          notice:
            'This connection cannot transfer attachments. Saved files remain available.',
        });
        return Promise.resolve();
      }
      return action((id) => api.decision(id, item.id, kind));
    },
    setAutoAccept(enabled: boolean) {
      return action((id) => api.autoAccept(id, enabled));
    },
    preview(item: AttachmentItem) {
      return action(async (id) => {
        const epoch = generation;
        const imageEpoch = imageGeneration;
        const url = await api.preview(id, item.id);
        if (!mediaURL(url)) throw new Error('Invalid media preview.');
        if (epoch === generation && id === current.contactID) {
          if (
            ['image/png', 'image/jpeg', 'image/gif', 'image/webp'].includes(
              item.mediaType,
            )
          ) {
            if (imageEpoch === imageGeneration)
              publish({ image: { id: item.id, name: item.name, url } });
          } else publish({ videos: { ...current.videos, [item.id]: url } });
        }
      }, false);
    },
    closeImage() {
      imageGeneration++;
      publish({ image: null });
    },
    save(item: AttachmentItem) {
      return action(async (id) => {
        const epoch = generation;
        const result = object(await api.save(id, item.id));
        if (
          Object.keys(result).length !== 1 ||
          (result.handle !== '' && !handle(result.handle))
        )
          throw new Error('Invalid save response.');
        if (epoch === generation && id === current.contactID && result.handle)
          publish({
            saved: { ...current.saved, [item.id]: result.handle as string },
          });
      });
    },
    reveal(item: AttachmentItem) {
      return action(async (id) => {
        const saved = current.saved[item.id];
        if (!handle(saved)) throw new Error('Save the file first.');
        await api.reveal(id, saved);
      });
    },
  };
}
