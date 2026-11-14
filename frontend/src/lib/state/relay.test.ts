import { describe, it, expect, vi } from 'vitest';
import { get } from 'svelte/store';
import {
  createRelayChange,
  validateRelayChange,
  validateRelayCapabilities,
  validRelayURL,
  publicRelayURL,
  validRelayCarry,
  validateRelayCarry,
  relayCarryPrefix,
  maxRelayCarryBytes,
  type RelayChangeBridge,
  type RelayChangeSnapshot,
  type RelayCapabilities,
} from './relay';

const id = 'AQEBAQEBAQEBAQEBAQEBAQ',
  other = 'AgICAgICAgICAgICAgICAg',
  requestID = 'AwMDAwMDAwMDAwMDAwMDAw',
  requestB = 'BAQEBAQEBAQEBAQEBAQEBA';
const supported: RelayCapabilities = {
  peerProtocol: 4,
  attachmentsSupported: true,
  contactControlsSupported: true,
};
const offline: RelayCapabilities = {
  peerProtocol: 0,
  attachmentsSupported: false,
  contactControlsSupported: false,
};
// Synthetic opaque text only; these tests prove bridge/consent behavior, not
// Ed25519 verification (which belongs to the Go protocol tests).
const carry = relayCarryPrefix + 'c3ludGhldGlj';
const snapshot = (
  update: Partial<RelayChangeSnapshot> = {},
): RelayChangeSnapshot => ({
  contactID: id,
  revision: 1,
  requestID: '',
  state: 'idle',
  currentServiceURL: 'https://relay.example/map.json',
  proposedServiceURL: '',
  proposedHost: '',
  localApproved: false,
  peerApproved: false,
  canPropose: true,
  canApprove: false,
  ...update,
});
const proposal = (update: Partial<RelayChangeSnapshot> = {}) =>
  snapshot({
    revision: 2,
    requestID,
    state: 'proposed',
    proposedServiceURL: publicRelayURL,
    proposedHost: 'tailcat.dev',
    localApproved: true,
    canPropose: false,
    ...update,
  });
function api() {
  let hint: (value: unknown) => void = () => {};
  const bridge: RelayChangeBridge = {
    get: vi.fn(async (id) => snapshot({ contactID: id })),
    propose: vi.fn(async () => proposal()),
    approve: vi.fn(async () =>
      proposal({ revision: 3, state: 'preparing', peerApproved: true }),
    ),
    decline: vi.fn(async () =>
      proposal({ state: 'declined', canPropose: true, canApprove: false }),
    ),
    cancel: vi.fn(async () =>
      proposal({ state: 'declined', canPropose: true }),
    ),
    export: vi.fn(async () => ({ text: carry })),
    import: vi.fn(async () =>
      proposal({
        state: 'awaiting-approval',
        localApproved: false,
        peerApproved: true,
        canApprove: true,
      }),
    ),
    subscribe: vi.fn((callback) => {
      hint = callback;
      return () => {
        hint = () => {};
      };
    }),
  };
  return { bridge, emit: (value: unknown) => hint(value) };
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => (resolve = done));
  return { promise, resolve };
}
async function ready(
  store: ReturnType<typeof createRelayChange>,
  contact = id,
  capabilities = supported,
) {
  store.select(contact, capabilities);
  await vi.waitFor(() => expect(get(store.state).loaded).toBe(true));
}

describe('relay change boundary', () => {
  it('validates bounded exact DTOs and never accepts endpoint/secret fields or a spoofed host', () => {
    expect(validateRelayChange(snapshot(), id)).toEqual(snapshot());
    for (const update of [
      { contactID: other },
      { revision: -1 },
      { requestID: 'bad' },
      { state: 'identity-changed' },
      { currentServiceURL: 'http://relay.example/map' },
      { proposedServiceURL: publicRelayURL, proposedHost: 'other.example' },
      {
        proposedServiceURL: publicRelayURL + '?private=1',
        proposedHost: 'tailcat.dev',
      },
      { serverEndpoint: 'private synthetic' },
      { canApprove: true },
    ])
      expect(() =>
        validateRelayChange({ ...snapshot(), ...update }, id),
      ).toThrow();
    expect(
      validateRelayChange(
        proposal({
          proposedServiceURL: 'https://Relay.Example:443/map',
          proposedHost: 'relay.example',
        }),
        id,
      ).proposedHost,
    ).toBe('relay.example');
    for (const value of [
      'https://user:secret@relay.example/map',
      'https://relay.example/map#fragment',
      'https://relay.example/map?x=1',
      'https://relay.example/map\n',
      'https://relay.example/' + 'x'.repeat(2048),
    ])
      expect(validRelayURL(value)).toBe(false);
  });
  it('distinguishes protocol3, base4 and control4 without guessing support from version alone', () => {
    for (const value of [
      {
        peerProtocol: 3,
        attachmentsSupported: false,
        contactControlsSupported: false,
      },
      {
        peerProtocol: 4,
        attachmentsSupported: true,
        contactControlsSupported: false,
      },
      supported,
    ])
      expect(validateRelayCapabilities(value)).toEqual(value);
    for (const update of [
      { peerProtocol: 2 },
      { peerProtocol: 3, attachmentsSupported: true },
      { peerProtocol: 3, contactControlsSupported: true },
      { attachmentsSupported: false, contactControlsSupported: true },
      { privateKey: 'synthetic' },
    ])
      expect(() =>
        validateRelayCapabilities({ ...supported, ...update }),
      ).toThrow();
  });
  it('keeps the relay field empty and consent unchecked; preset only fills an empty field and never fetches', async () => {
    const { bridge } = api(),
      store = createRelayChange(bridge);
    await ready(store);
    expect(get(store.state).draftURL).toBe('');
    expect(get(store.state).consent).toBe(false);
    store.preset();
    expect(get(store.state).draftURL).toBe(publicRelayURL);
    expect(get(store.state).consent).toBe(false);
    expect(await store.propose()).toBe(false);
    expect(bridge.propose).not.toHaveBeenCalled();
    store.setConsent(true);
    store.setDraft('https://another.example/map');
    expect(get(store.state).consent).toBe(false);
    store.preset();
    expect(get(store.state).draftURL).toBe('https://another.example/map');
    store.setConsent(true);
    expect(await store.propose()).toBe(true);
    expect(bridge.propose).toHaveBeenCalledWith(
      id,
      'https://another.example/map',
      true,
    );
    store.stop();
  });
  it('requires exact current proposal context and separate new consent before a peer approval', async () => {
    const { bridge } = api();
    bridge.get = vi.fn(async () =>
      proposal({
        state: 'awaiting-approval',
        localApproved: false,
        peerApproved: true,
        canApprove: true,
      }),
    );
    const store = createRelayChange(bridge);
    await ready(store);
    store.preset();
    store.setConsent(true);
    expect(await store.approve(requestID)).toBe(false);
    store.setApprovalConsent(true);
    bridge.get = vi.fn(async () =>
      proposal({
        revision: 5,
        requestID: requestB,
        state: 'awaiting-approval',
        localApproved: false,
        peerApproved: true,
        canApprove: true,
      }),
    );
    await store.reload();
    expect(get(store.state).approvalConsent).toBe(false);
    expect(get(store.state).consent).toBe(false);
    store.setApprovalConsent(true);
    expect(await store.approve(requestID)).toBe(false);
    expect(bridge.approve).not.toHaveBeenCalled();
    bridge.approve = vi.fn(async () =>
      proposal({
        revision: 6,
        requestID: requestB,
        state: 'preparing',
        peerApproved: true,
      }),
    );
    expect(await store.approve(requestB)).toBe(true);
    expect(bridge.approve).toHaveBeenCalledWith(id, requestB, true);
    expect(get(store.state).approvalConsent).toBe(false);
    store.stop();
  });
  it('does not send controls to protocol3 or base4 peers even if an obsolete snapshot allows proposing', async () => {
    for (const capability of [
      {
        peerProtocol: 3,
        attachmentsSupported: false,
        contactControlsSupported: false,
      },
      {
        peerProtocol: 4,
        attachmentsSupported: true,
        contactControlsSupported: false,
      },
    ] as RelayCapabilities[]) {
      const { bridge } = api(),
        store = createRelayChange(bridge);
      await ready(store, id, capability);
      store.preset();
      store.setConsent(true);
      expect(await store.propose()).toBe(false);
      expect(bridge.propose).not.toHaveBeenCalled();
      store.stop();
    }
  });
  it('ignores a delayed action for an inactive contact and clears all drafts/consent on switch or lock', async () => {
    const { bridge } = api(),
      held = deferred<unknown>();
    bridge.propose = vi.fn(() => held.promise);
    const store = createRelayChange(bridge);
    await ready(store);
    store.preset();
    store.setConsent(true);
    const running = store.propose();
    await ready(store, other);
    held.resolve(proposal({ revision: 100 }));
    await running;
    expect(get(store.state).contactID).toBe(other);
    expect(get(store.state).snapshot?.contactID).toBe(other);
    expect(get(store.state).draftURL).toBe('');
    store.preset();
    store.setConsent(true);
    store.stop();
    expect(get(store.state).snapshot).toBeNull();
    expect(get(store.state).draftURL).toBe('');
    expect(get(store.state).consent).toBe(false);
  });
  it('accepts RPC revisions only, ignores old responses, and uses hints only to reload', async () => {
    const { bridge, emit } = api(),
      old = deferred<unknown>();
    bridge.get = vi
      .fn()
      .mockReturnValueOnce(old.promise)
      .mockResolvedValueOnce(
        snapshot({
          revision: 10,
          currentServiceURL: 'https://new.example/map',
        }),
      );
    const store = createRelayChange(bridge);
    store.start();
    store.select(id, supported);
    await store.reload();
    old.resolve(snapshot({ revision: 2 }));
    await vi.waitFor(() =>
      expect(get(store.state).snapshot?.revision).toBe(10),
    );
    emit({ contactID: id, revision: 9999 });
    expect(get(store.state).snapshot?.revision).toBe(10);
    store.stop();
  });
  it('keeps current approval consent and manual text across unchanged background revisions', async () => {
    const { bridge } = api();
    const incoming = (revision: number) =>
      proposal({
        revision,
        state: 'awaiting-approval',
        localApproved: false,
        peerApproved: true,
        canApprove: true,
      });
    bridge.get = vi.fn(async () => incoming(2));
    const store = createRelayChange(bridge);
    await ready(store);
    store.setManual(true);
    store.setDraft('https://manual.example/map');
    store.setImportText(carry);
    store.setApprovalConsent(true);
    bridge.get = vi.fn(async () => incoming(10));
    await store.reload();
    store.select(id, supported);
    expect(get(store.state).snapshot?.revision).toBe(10);
    expect(get(store.state).manual).toBe(true);
    expect(get(store.state).importText).toBe(carry);
    expect(get(store.state).draftURL).toBe('https://manual.example/map');
    expect(get(store.state).approvalConsent).toBe(true);
    store.stop();
  });
  it('keeps uncertain actions disabled until a successful authoritative reload, with fixed notices', async () => {
    const { bridge } = api();
    bridge.propose = vi.fn(async () => {
      throw new Error('https://private.example/raw-error');
    });
    const store = createRelayChange(bridge);
    await ready(store);
    store.preset();
    store.setConsent(true);
    await store.propose();
    expect(get(store.state).loaded).toBe(false);
    expect(get(store.state).notice).not.toContain('private.example');
    store.setConsent(true);
    await store.propose();
    expect(bridge.propose).toHaveBeenCalledTimes(1);
    await store.reload();
    expect(get(store.state).loaded).toBe(true);
    expect(get(store.state).notice).toBe('');
    store.stop();
  });
  it('binds decline/cancel to current requests and does not cancel a switching or recovering commit', async () => {
    const { bridge } = api();
    bridge.get = vi.fn(async () => proposal());
    const store = createRelayChange(bridge);
    await ready(store);
    expect(await store.cancel(requestB)).toBe(false);
    expect(await store.cancel(requestID)).toBe(true);
    expect(bridge.cancel).toHaveBeenCalledWith(id, requestID);
    bridge.get = vi.fn(async () =>
      proposal({ revision: 8, state: 'switching', peerApproved: true }),
    );
    await store.reload();
    expect(await store.cancel(requestID)).toBe(false);
    expect(bridge.cancel).toHaveBeenCalledTimes(1);
    store.stop();
  });
  it('retires the previous contact if replacement capabilities are malformed', async () => {
    const { bridge } = api(),
      store = createRelayChange(bridge);
    await ready(store);
    store.preset();
    store.setConsent(true);
    store.select(other, {
      ...supported,
      peerProtocol: 99,
    } as unknown as RelayCapabilities);
    expect(get(store.state).contactID).toBe('');
    expect(get(store.state).snapshot).toBeNull();
    expect(get(store.state).draftURL).toBe('');
    expect(await store.propose()).toBe(false);
    expect(bridge.propose).not.toHaveBeenCalled();
    store.stop();
  });
  it('keeps signed text opaque, ASCII bounded and exact without trimming or rendering metadata', () => {
    expect(validateRelayCarry({ text: carry })).toBe(carry);
    for (const text of [
      '',
      relayCarryPrefix,
      carry + '\n',
      ' ' + carry,
      carry + '\u202e',
      relayCarryPrefix + 'a'.repeat(maxRelayCarryBytes),
      'wrong:' + carry,
    ])
      expect(validRelayCarry(text)).toBe(false);
    for (const value of [
      { text: carry, path: '/private' },
      { text: carry, privateKey: 'synthetic' },
      { text: 1 },
    ])
      expect(() => validateRelayCarry(value)).toThrow();
  });
  it('creates and exports an offline signed proposal only after manual mode plus explicit URL consent', async () => {
    const { bridge } = api(),
      store = createRelayChange(bridge);
    await ready(store, id, offline);
    store.preset();
    store.setConsent(true);
    expect(await store.propose()).toBe(false);
    store.setManual(true);
    expect(get(store.state).consent).toBe(false);
    expect(await store.propose()).toBe(false);
    store.setConsent(true);
    expect(await store.propose()).toBe(true);
    expect(bridge.propose).toHaveBeenCalledWith(id, publicRelayURL, true);
    expect(await store.exportCarry()).toBe(true);
    expect(bridge.export).toHaveBeenCalledWith(id);
    expect(get(store.state).exportText).toBe(carry);
    store.stop();
  });
  it('inspects peer signed text offline without approving/fetching and requires separate approval consent', async () => {
    const { bridge } = api(),
      store = createRelayChange(bridge);
    await ready(store, id, offline);
    store.setManual(true);
    store.setImportText(carry);
    expect(await store.inspectCarry()).toBe(true);
    expect(bridge.import).toHaveBeenCalledWith(id, carry);
    expect(get(store.state).importText).toBe('');
    expect(get(store.state).approvalConsent).toBe(false);
    expect(bridge.approve).not.toHaveBeenCalled();
    expect(bridge.propose).not.toHaveBeenCalled();
    expect(await store.approve(requestID)).toBe(false);
    store.setApprovalConsent(true);
    expect(await store.approve(requestID)).toBe(true);
    expect(bridge.approve).toHaveBeenCalledWith(id, requestID, true);
    store.stop();
  });
  it('clears imported text before awaiting inspection and rejects delayed capabilities after lock', async () => {
    const { bridge } = api(),
      held = deferred<unknown>();
    bridge.import = vi.fn(() => held.promise);
    const store = createRelayChange(bridge);
    await ready(store, id, offline);
    store.setManual(true);
    store.setImportText(carry);
    const importing = store.inspectCarry();
    expect(get(store.state).importText).toBe('');
    store.stop();
    held.resolve(proposal());
    await importing;
    expect(get(store.state).snapshot).toBeNull();
    expect(get(store.state).exportText).toBe('');
    expect(get(store.state).manual).toBe(false);
  });
  it('does not copy automatically and drops a delayed export when the approved proposal changes', async () => {
    const { bridge } = api(),
      held = deferred<unknown>();
    bridge.get = vi.fn(async () => proposal());
    bridge.export = vi.fn(() => held.promise);
    const store = createRelayChange(bridge);
    await ready(store, id, offline);
    store.setManual(true);
    const exporting = store.exportCarry();
    bridge.get = vi.fn(async () =>
      proposal({ revision: 10, requestID: requestB }),
    );
    await store.reload();
    held.resolve({ text: carry });
    expect(await exporting).toBe(false);
    expect(get(store.state).exportText).toBe('');
    bridge.export = vi.fn(async () => ({ text: carry }));
    expect(await store.exportCarry()).toBe(true);
    const copy = vi.fn(async () => {});
    expect(copy).not.toHaveBeenCalled();
    await store.copyCarry(copy);
    expect(copy).toHaveBeenCalledWith(carry);
    store.stop();
    expect(get(store.state).exportText).toBe('');
  });
  it('exports a local decline while disconnected without treating it as approval', async () => {
    const { bridge } = api();
    bridge.get = vi.fn(async () =>
      proposal({
        state: 'awaiting-approval',
        localApproved: false,
        peerApproved: true,
        canApprove: true,
      }),
    );
    bridge.decline = vi.fn(async () =>
      proposal({
        revision: 3,
        requestID: '',
        state: 'declined',
        localApproved: false,
        canPropose: true,
        canApprove: false,
      }),
    );
    const store = createRelayChange(bridge);
    await ready(store, id, offline);
    store.setManual(true);
    expect(await store.decline(requestID)).toBe(true);
    expect(bridge.approve).not.toHaveBeenCalled();
    expect(await store.exportCarry()).toBe(true);
    expect(get(store.state).snapshot?.localApproved).toBe(false);
    store.stop();
  });
});
