import { writable } from 'svelte/store';
import { Events } from '@wailsio/runtime';
import { hasDesktopBridge } from '../backend';
import { validID, validateHint } from './bridge';

export const publicRelayURL = 'https://tailcat.dev/derpmap.json';
export const maxRelayCarryBytes = 8192;
export const relayCarryPrefix = 'whatsup-relay:v1:';
export function validRelayCarry(value: string): boolean {
  return (
    value.length > relayCarryPrefix.length &&
    value.length <= maxRelayCarryBytes &&
    value.startsWith(relayCarryPrefix) &&
    /^[\x21-\x7e]+$/.test(value)
  );
}
export function validateRelayCarry(input: unknown): string {
  const value = object(input);
  if (
    Object.keys(value).length !== 1 ||
    typeof value.text !== 'string' ||
    !validRelayCarry(value.text)
  )
    throw new Error('Invalid signed service-change text.');
  return value.text;
}
export interface RelayCapabilities {
  peerProtocol: 0 | 3 | 4;
  attachmentsSupported: boolean;
  contactControlsSupported: boolean;
}
export interface RelayChangeSnapshot {
  contactID: string;
  revision: number;
  requestID: string;
  state:
    | 'idle'
    | 'proposed'
    | 'awaiting-approval'
    | 'preparing'
    | 'switching'
    | 'recovering'
    | 'complete'
    | 'declined'
    | 'update-required';
  currentServiceURL: string;
  proposedServiceURL: string;
  proposedHost: string;
  localApproved: boolean;
  peerApproved: boolean;
  canPropose: boolean;
  canApprove: boolean;
}
export interface RelayChangeBridge {
  get(id: string): Promise<unknown>;
  propose(id: string, url: string, consent: boolean): Promise<unknown>;
  approve(id: string, requestID: string, consent: boolean): Promise<unknown>;
  decline(id: string, requestID: string): Promise<unknown>;
  cancel(id: string, requestID: string): Promise<unknown>;
  export(id: string): Promise<unknown>;
  import(id: string, text: string): Promise<unknown>;
  subscribe(callback: (hint: unknown) => void): () => void;
}
const unavailableCapabilities = (): RelayCapabilities => ({
  peerProtocol: 0,
  attachmentsSupported: false,
  contactControlsSupported: false,
});
const object = (value: unknown): Record<string, unknown> => {
  if (!value || typeof value !== 'object' || Array.isArray(value))
    throw new Error('Invalid relay-change response.');
  return value as Record<string, unknown>;
};
export function validateRelayCapabilities(input: unknown): RelayCapabilities {
  const value = object(input);
  if (
    Object.keys(value).length !== 3 ||
    ![0, 3, 4].includes(value.peerProtocol as number) ||
    typeof value.attachmentsSupported !== 'boolean' ||
    typeof value.contactControlsSupported !== 'boolean' ||
    ((value.attachmentsSupported || value.contactControlsSupported) &&
      value.peerProtocol !== 4) ||
    (value.contactControlsSupported && !value.attachmentsSupported)
  )
    throw new Error('Invalid peer capabilities.');
  return {
    peerProtocol: value.peerProtocol as 0 | 3 | 4,
    attachmentsSupported: value.attachmentsSupported,
    contactControlsSupported: value.contactControlsSupported,
  };
}
export function validRelayURL(value: string): boolean {
  if (
    value.length < 1 ||
    value.length > 2048 ||
    /[\s\u0000-\u001f\u007f]/u.test(value) ||
    !value.startsWith('https://')
  )
    return false;
  try {
    const url = new URL(value);
    return (
      url.protocol === 'https:' &&
      url.hostname !== '' &&
      url.username === '' &&
      url.password === '' &&
      url.search === '' &&
      url.hash === '' &&
      (!url.port || Number(url.port) > 0) &&
      !value.includes('?') &&
      !value.includes('#')
    );
  } catch {
    return false;
  }
}
export function validateRelayChange(
  input: unknown,
  id: string,
): RelayChangeSnapshot {
  const value = object(input);
  if (
    Object.keys(value).length !== 11 ||
    !validID(id) ||
    value.contactID !== id ||
    !Number.isSafeInteger(value.revision) ||
    (value.revision as number) < 0 ||
    typeof value.requestID !== 'string' ||
    (value.requestID !== '' && !validID(value.requestID)) ||
    ![
      'idle',
      'proposed',
      'awaiting-approval',
      'preparing',
      'switching',
      'recovering',
      'complete',
      'declined',
      'update-required',
    ].includes(value.state as string) ||
    typeof value.currentServiceURL !== 'string' ||
    !validRelayURL(value.currentServiceURL) ||
    typeof value.proposedServiceURL !== 'string' ||
    typeof value.proposedHost !== 'string' ||
    ['localApproved', 'peerApproved', 'canPropose', 'canApprove'].some(
      (key) => typeof value[key] !== 'boolean',
    )
  )
    throw new Error('Invalid relay-change response.');
  const pending = [
    'proposed',
    'awaiting-approval',
    'preparing',
    'switching',
    'recovering',
  ].includes(value.state as string);
  if (
    (pending && value.requestID === '') ||
    (pending && value.proposedServiceURL === '') ||
    (value.proposedServiceURL === '' && value.proposedHost !== '') ||
    (value.proposedServiceURL !== '' &&
      (!validRelayURL(value.proposedServiceURL) ||
        value.proposedHost !== new URL(value.proposedServiceURL).host)) ||
    (value.canApprove &&
      (!pending || value.localApproved || value.requestID === '')) ||
    (value.canPropose && pending)
  )
    throw new Error('Invalid relay-change response.');
  return {
    contactID: id,
    revision: value.revision as number,
    requestID: value.requestID,
    state: value.state as RelayChangeSnapshot['state'],
    currentServiceURL: value.currentServiceURL,
    proposedServiceURL: value.proposedServiceURL,
    proposedHost: value.proposedHost,
    localApproved: value.localApproved as boolean,
    peerApproved: value.peerApproved as boolean,
    canPropose: value.canPropose as boolean,
    canApprove: value.canApprove as boolean,
  };
}
async function native() {
  if (!hasDesktopBridge())
    throw new Error('Open the desktop app to change a connection service.');
  return import('../../../bindings/whatsup/internal/desktop/service');
}
const bridge: RelayChangeBridge = {
  get: async (id) => (await native()).GetRelayChange(id),
  propose: async (id, url, consent) =>
    (await native()).ProposeRelayChange(id, url, consent),
  approve: async (id, request, consent) =>
    (await native()).ApproveRelayChange(id, request, consent),
  decline: async (id, request) =>
    (await native()).DeclineRelayChange(id, request),
  cancel: async (id, request) =>
    (await native()).CancelRelayChange(id, request),
  export: async (id) => (await native()).ExportRelayChange(id),
  import: async (id, text) => (await native()).ImportRelayChange(id, text),
  subscribe: (callback) =>
    Events.On('whatsup:contact-changed', (event) => callback(event.data)),
};
const initial = () => ({
  contactID: '',
  capabilities: unavailableCapabilities(),
  snapshot: null as RelayChangeSnapshot | null,
  draftURL: '',
  consent: false,
  approvalConsent: false,
  manual: false,
  exportText: '',
  importText: '',
  loaded: false,
  loading: false,
  busy: false,
  notice: '',
});
export type RelayChangeState = ReturnType<typeof initial>;
export function createRelayChange(api: RelayChangeBridge = bridge) {
  let current = initial(),
    generation = 0,
    request = 0,
    revision = 0;
  let unsubscribe: (() => void) | undefined,
    timer: ReturnType<typeof setTimeout> | undefined;
  const state = writable(current);
  const publish = (update: Partial<typeof current>) => {
    current = { ...current, ...update };
    state.set(current);
  };
  const live = (epoch: number, id: string) =>
    epoch === generation && id === current.contactID;
  function retire() {
    generation++;
    request++;
    revision = 0;
    if (timer) clearTimeout(timer);
    timer = undefined;
  }
  function accept(snapshot: RelayChangeSnapshot) {
    if (snapshot.revision < revision) return;
    const before = current.snapshot;
    const changed =
      before?.requestID !== snapshot.requestID ||
      before?.proposedServiceURL !== snapshot.proposedServiceURL ||
      before?.currentServiceURL !== snapshot.currentServiceURL ||
      before?.state !== snapshot.state ||
      before?.localApproved !== snapshot.localApproved ||
      before?.peerApproved !== snapshot.peerApproved;
    revision = snapshot.revision;
    publish({
      snapshot,
      loaded: true,
      notice: '',
      ...(changed
        ? { consent: false, approvalConsent: false, exportText: '' }
        : {}),
    });
  }
  async function reload() {
    const id = current.contactID,
      epoch = generation,
      read = ++request;
    if (!validID(id)) return;
    publish({ loading: true });
    try {
      const value = validateRelayChange(await api.get(id), id);
      if (live(epoch, id) && read === request) accept(value);
    } catch {
      if (live(epoch, id) && read === request)
        publish({
          loaded: false,
          notice:
            'The connection service could not be checked. Reload before changing it.',
        });
    } finally {
      if (live(epoch, id) && read === request) publish({ loading: false });
    }
  }
  function schedule() {
    if (!timer)
      timer = setTimeout(() => {
        timer = undefined;
        void reload();
      }, 100);
  }
  function start() {
    if (!unsubscribe)
      unsubscribe = api.subscribe((input) => {
        const hint = validateHint(input);
        if (hint?.contactID === current.contactID && hint.revision > revision)
          schedule();
      });
  }
  function select(id: string, capabilities: RelayCapabilities) {
    let parsed: RelayCapabilities;
    try {
      parsed = validateRelayCapabilities(capabilities);
    } catch {
      retire();
      current = initial();
      current.notice = 'The peer capabilities could not be checked.';
      state.set(current);
      return;
    }
    if (
      id === current.contactID &&
      JSON.stringify(parsed) === JSON.stringify(current.capabilities)
    )
      return;
    retire();
    current = initial();
    current.capabilities = parsed;
    if (validID(id)) current.contactID = id;
    state.set(current);
    if (current.contactID) void reload();
  }
  async function run(action: () => Promise<unknown>, localOnly = false) {
    if (
      current.busy ||
      !current.loaded ||
      !validID(current.contactID) ||
      (!localOnly && !current.capabilities.contactControlsSupported)
    )
      return false;
    const id = current.contactID,
      epoch = generation;
    publish({ busy: true, notice: '' });
    try {
      const value = validateRelayChange(await action(), id);
      if (!live(epoch, id)) return false;
      accept(value);
      return true;
    } catch {
      if (live(epoch, id))
        publish({
          loaded: false,
          notice:
            'The service change could not be confirmed. Reload its status before trying again.',
        });
      return false;
    } finally {
      if (live(epoch, id)) publish({ busy: false });
    }
  }
  return {
    state,
    start,
    select,
    reload,
    stop() {
      unsubscribe?.();
      unsubscribe = undefined;
      retire();
      current = initial();
      state.set(current);
    },
    setDraft(value: string) {
      if (value !== current.draftURL)
        publish({ draftURL: value, consent: false });
    },
    preset() {
      if (current.draftURL === '')
        publish({ draftURL: publicRelayURL, consent: false });
    },
    setConsent(consent: boolean) {
      publish({ consent: consent === true });
    },
    setApprovalConsent(consent: boolean) {
      publish({ approvalConsent: consent === true });
    },
    setManual(manual: boolean) {
      if (current.manual !== manual)
        publish({
          manual,
          consent: false,
          approvalConsent: false,
          exportText: '',
          importText: '',
        });
    },
    setImportText(importText: string) {
      publish({ importText });
    },
    async exportCarry() {
      if (
        current.busy ||
        !current.loaded ||
        !validID(current.contactID) ||
        (!validID(current.snapshot?.requestID) &&
          !['declined', 'complete'].includes(current.snapshot?.state ?? ''))
      )
        return false;
      const id = current.contactID,
        epoch = generation,
        context = current.snapshot!;
      publish({ busy: true, notice: '', exportText: '' });
      try {
        const text = validateRelayCarry(await api.export(id));
        if (
          !live(epoch, id) ||
          context.requestID !== current.snapshot?.requestID ||
          context.state !== current.snapshot?.state ||
          context.currentServiceURL !== current.snapshot?.currentServiceURL ||
          context.proposedServiceURL !== current.snapshot?.proposedServiceURL ||
          context.localApproved !== current.snapshot?.localApproved ||
          context.peerApproved !== current.snapshot?.peerApproved
        )
          return false;
        publish({ exportText: text });
        return true;
      } catch {
        if (live(epoch, id))
          publish({
            notice:
              'Signed service-change text could not be prepared. Approve or decline the current proposal, then try again.',
          });
        return false;
      } finally {
        if (live(epoch, id)) publish({ busy: false });
      }
    },
    inspectCarry() {
      if (
        current.busy ||
        !current.loaded ||
        !validID(current.contactID) ||
        !validRelayCarry(current.importText)
      )
        return Promise.resolve(false);
      const id = current.contactID,
        text = current.importText;
      publish({
        importText: '',
        consent: false,
        approvalConsent: false,
        exportText: '',
      });
      // Local signature/context inspection persists only an inert proposal or
      // signed peer decision. Go must not fetch the service before local consent.
      return run(() => api.import(id, text), true);
    },
    async copyCarry(
      copy = (text: string) => navigator.clipboard.writeText(text),
    ) {
      const text = current.exportText,
        id = current.contactID,
        epoch = generation;
      if (!validRelayCarry(text) || !validID(id)) return false;
      try {
        await copy(text);
        if (live(epoch, id))
          publish({ notice: 'Signed service-change text copied.' });
        return true;
      } catch {
        if (live(epoch, id))
          publish({
            notice:
              'Clipboard access is unavailable. Select and copy the signed text below.',
          });
        return false;
      }
    },
    propose() {
      if (
        !current.snapshot?.canPropose ||
        !current.consent ||
        !validRelayURL(current.draftURL)
      )
        return Promise.resolve(false);
      const id = current.contactID,
        url = current.draftURL;
      publish({ consent: false });
      return run(() => api.propose(id, url, true), current.manual);
    },
    approve(requestID: string) {
      if (
        !current.snapshot?.canApprove ||
        !current.approvalConsent ||
        requestID !== current.snapshot.requestID
      )
        return Promise.resolve(false);
      const id = current.contactID;
      publish({ approvalConsent: false });
      return run(() => api.approve(id, requestID, true), current.manual);
    },
    decline(requestID: string) {
      if (
        !current.snapshot?.canApprove ||
        requestID !== current.snapshot.requestID
      )
        return Promise.resolve(false);
      const id = current.contactID;
      publish({ approvalConsent: false });
      return run(() => api.decline(id, requestID), current.manual);
    },
    cancel(requestID: string) {
      if (
        !current.snapshot ||
        !['proposed', 'awaiting-approval', 'preparing'].includes(
          current.snapshot.state,
        ) ||
        requestID !== current.snapshot.requestID
      )
        return Promise.resolve(false);
      const id = current.contactID;
      publish({ consent: false, approvalConsent: false });
      return run(() => api.cancel(id, requestID), current.manual);
    },
  };
}
