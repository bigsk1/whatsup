import { get } from 'svelte/store';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  createAppPreferences,
  defaultAppPreferences,
  validateAppPreferences,
} from './preferences';
import {
  createInvitationForm,
  publicRelayMap,
  validateInspection,
} from './invitation';
import { groupedFingerprint } from './fingerprint';
import {
  unavailablePairing,
  validConnectionService,
  validInvitationLink,
  validateInvitationShare,
  maximumInvitationBytes,
  maximumServiceURLBytes,
} from '../pairing';
import { unavailableConversation, validateConversation } from '../messaging';

afterEach(() => vi.useRealTimers());
const link =
  'whatsup://pair/v2?id=synthetic&service=https%3A%2F%2Frelay.example.invalid%2Fmap.json';
const serviceURL = 'https://relay.example.invalid/map.json';
const inspection = { serviceURL, host: 'relay.example.invalid' };
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((finish) => {
    resolve = finish;
  });
  return { promise, resolve };
}
function form() {
  const bridge = {
    enabled: () => true,
    pairing: () => unavailablePairing,
    create: vi.fn(async () => ({
      link,
      expiresAt: new Date(Date.now() + 600000).toISOString(),
    })),
    join: vi.fn(async () => {}),
    cancel: vi.fn(async () => {}),
    close: vi.fn(),
    inspect: vi.fn(async () => inspection),
    copy: vi.fn(async () => {}),
  };
  return { bridge, flow: createInvitationForm(bridge) };
}

describe('local app preferences', () => {
  it('defaults to tray and generic notifications with login/name/preview off, and rejects extra fields', () => {
    expect(defaultAppPreferences).toEqual({
      closeToTray: true,
      startAtLogin: false,
      notifications: true,
      notificationName: false,
      notificationPreview: false,
    });
    expect(() =>
      validateAppPreferences({ ...defaultAppPreferences, endpoint: 'hidden' }),
    ).toThrow();
    expect(() =>
      validateAppPreferences({
        ...defaultAppPreferences,
        notificationPreview: 'true',
      }),
    ).toThrow();
  });
  it('saves only explicit preferences and retires delayed writes on teardown', async () => {
    const held = deferred<unknown>();
    const bridge = {
      get: vi.fn(async () => defaultAppPreferences),
      set: vi.fn(() => held.promise),
    };
    const preferences = createAppPreferences(bridge);
    await preferences.load();
    const update = { ...defaultAppPreferences, notificationName: true };
    const saving = preferences.save(update);
    expect(bridge.set).toHaveBeenCalledWith(update);
    preferences.stop();
    held.resolve(update);
    await saving;
    expect(get(preferences.state).value).toEqual(defaultAppPreferences);
    expect(get(preferences.state).loaded).toBe(false);
  });
  it('keeps failed writes unavailable until an authoritative reload without exposing raw errors', async () => {
    const bridge = {
      get: vi.fn(async () => defaultAppPreferences),
      set: vi.fn(async () => {
        throw new Error('SYNTHETIC-PRIVATE-ENDPOINT');
      }),
    };
    const preferences = createAppPreferences(bridge);
    await preferences.load();
    await preferences.save({ ...defaultAppPreferences, startAtLogin: true });
    expect(get(preferences.state).loaded).toBe(false);
    expect(get(preferences.state).value.startAtLogin).toBe(false);
    expect(get(preferences.state).notice).not.toContain(
      'SYNTHETIC-PRIVATE-ENDPOINT',
    );
    await preferences.load();
    expect(get(preferences.state).loaded).toBe(true);
  });
});

describe('invitation service consent', () => {
  it('accepts an 8192-byte invitation carrying a 2048-byte encoded service and rejects either bound plus one', () => {
    const servicePrefix = 'https://relay.example.invalid/map?value=';
    const service =
      servicePrefix + '/'.repeat(maximumServiceURLBytes - servicePrefix.length);
    const prefix = `whatsup://pair/v2?service=${encodeURIComponent(service)}&padding=`;
    const invitation =
      prefix + 'x'.repeat(maximumInvitationBytes - prefix.length);
    expect(validConnectionService(service)).toBe(true);
    expect(validConnectionService(service + '/')).toBe(false);
    expect(validInvitationLink(invitation)).toBe(true);
    expect(
      validateInvitationShare({
        link: invitation,
        expiresAt: '2026-11-14T12:00:00Z',
      }).link,
    ).toBe(invitation);
    expect(validInvitationLink(invitation + 'x')).toBe(false);
    const unicodePrefix = 'whatsup://pair/v2?x=';
    const unicode =
      unicodePrefix +
      'é'.repeat(
        Math.floor((maximumInvitationBytes - unicodePrefix.length) / 2),
      );
    expect(validInvitationLink(unicode)).toBe(true);
    expect(validInvitationLink(unicode + 'é')).toBe(false);
  });
  it('fills only an empty creation field and never grants relay consent', () => {
    const { flow, bridge } = form();
    flow.usePublicRelay();
    expect(get(flow.state).relayURL).toBe(publicRelayMap);
    expect(get(flow.state).consent).toBe(false);
    flow.setURL(serviceURL);
    flow.setConsent(true);
    flow.usePublicRelay();
    expect(get(flow.state).relayURL).toBe(serviceURL);
    expect(bridge.create).not.toHaveBeenCalled();
    flow.reset();
  });
  it('locally reviews the carried service before separate consent and never joins during inspection', async () => {
    const { flow, bridge } = form();
    flow.setMode('join');
    flow.setLink(link);
    expect(flow.ready()).toBe(false);
    await flow.inspect();
    expect(bridge.inspect).toHaveBeenCalledWith(link);
    expect(bridge.join).not.toHaveBeenCalled();
    expect(get(flow.state).inspection).toEqual(inspection);
    expect(get(flow.state).consent).toBe(false);
    flow.setConsent(true);
    await flow.start();
    expect(bridge.join).toHaveBeenCalledWith(link, serviceURL, true);
    expect(get(flow.state).pastedLink).toBe('');
    flow.reset();
  });
  it('retires a delayed inspection after link edits, cancel, or lock without restoring consent', async () => {
    const { flow, bridge } = form();
    const held = deferred<typeof inspection>();
    bridge.inspect.mockReturnValueOnce(held.promise);
    flow.setMode('join');
    flow.setLink(link);
    const inspecting = flow.inspect();
    flow.setLink(link + '&changed=1');
    held.resolve(inspection);
    await inspecting;
    expect(get(flow.state).inspection).toBeNull();
    expect(get(flow.state).consent).toBe(false);
    expect(bridge.join).not.toHaveBeenCalled();
    flow.reset();
  });
  it('rejects mismatched or credential-bearing service descriptions', () => {
    for (const value of [
      { ...inspection, host: 'other.example.invalid' },
      {
        ...inspection,
        serviceURL: 'https://user:secret@relay.example.invalid/map.json',
      },
      { ...inspection, secret: 'hidden' },
    ])
      expect(() => validateInspection(value)).toThrow();
  });
});

it('shows every fingerprint character in groups of four and resets disconnected path claims', () => {
  const fingerprint = '0123456789abcdef'.repeat(4);
  expect(groupedFingerprint(fingerprint).split(' ')).toHaveLength(16);
  expect(groupedFingerprint(fingerprint).replaceAll(' ', '')).toBe(fingerprint);
  expect(groupedFingerprint(fingerprint + '\u202e')).toBe('');
  const value = {
    ...unavailableConversation,
    state: 'offline',
    contactID: 'AQEBAQEBAQEBAQEBAQEBAQ',
    peerFingerprint: fingerprint,
    path: 'Direct',
    canSend: true,
    canConnect: true,
  };
  expect(validateConversation(value).path).toBe('Unknown');
  expect(validateConversation({ ...value, state: 'connected' }).path).toBe(
    'Direct',
  );
  expect(() =>
    validateConversation({ ...value, path: 'Direct only' }),
  ).toThrow();
});
