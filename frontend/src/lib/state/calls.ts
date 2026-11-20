import { Events, Stream } from '@wailsio/runtime';
import { writable } from 'svelte/store';
import { hasDesktopBridge } from '../backend';
import { createAudioEngine, type AudioFailure } from '../call/audio';
import {
  callMediaStream,
  decodeAudio,
  encodeAudio,
  encodeHello,
  validCallID,
  type ReceivedAudio,
} from '../call/protocol';
import { createRinger, type Ringer } from '../call/ring';
import { validID } from './bridge';

export type CallStateName =
  'idle' | 'outgoing' | 'incoming' | 'connecting' | 'active';
export type CallAvailability =
  'ready' | 'calls-off' | 'offline' | 'peer-update' | 'busy';

export interface CallView {
  contactID: string;
  state: CallStateName;
  callID: string;
  peerRinging: boolean;
  micMuted: boolean;
  peerMicMuted: boolean;
  activeSince: number;
  availability: CallAvailability;
  lastEnd: string;
  lastOutgoing: boolean;
  lastConnected: boolean;
}

const states = ['idle', 'outgoing', 'incoming', 'connecting', 'active'];
const availabilities = ['ready', 'calls-off', 'offline', 'peer-update', 'busy'];
const ends = [
  '',
  'hung-up',
  'peer-hung-up',
  'cancelled',
  'declined',
  'peer-declined',
  'peer-busy',
  'peer-unavailable',
  'no-answer',
  'missed',
  'connection-lost',
  'media-failed',
  'device-error',
  'protocol',
  'interrupted',
];

export function validateCallView(value: unknown, contactID: string): CallView {
  const invalid = () => new Error('The desktop returned invalid call data.');
  if (!value || typeof value !== 'object' || Array.isArray(value))
    throw invalid();
  const record = value as Record<string, unknown>;
  if (
    Object.keys(record).length !== 11 ||
    record.contactID !== contactID ||
    !validID(contactID) ||
    typeof record.state !== 'string' ||
    !states.includes(record.state) ||
    typeof record.callID !== 'string' ||
    (record.state === 'idle'
      ? record.callID !== ''
      : !validCallID(record.callID)) ||
    typeof record.peerRinging !== 'boolean' ||
    typeof record.micMuted !== 'boolean' ||
    typeof record.peerMicMuted !== 'boolean' ||
    !Number.isSafeInteger(record.activeSince) ||
    (record.activeSince as number) < 0 ||
    typeof record.availability !== 'string' ||
    !availabilities.includes(record.availability) ||
    typeof record.lastEnd !== 'string' ||
    !ends.includes(record.lastEnd) ||
    typeof record.lastOutgoing !== 'boolean' ||
    typeof record.lastConnected !== 'boolean'
  )
    throw invalid();
  return {
    contactID,
    state: record.state as CallStateName,
    callID: record.callID,
    peerRinging: record.peerRinging,
    micMuted: record.micMuted,
    peerMicMuted: record.peerMicMuted,
    activeSince: record.activeSince as number,
    availability: record.availability as CallAvailability,
    lastEnd: record.lastEnd,
    lastOutgoing: record.lastOutgoing,
    lastConnected: record.lastConnected,
  };
}

export function validateCallHint(value: unknown): string | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  return Object.keys(record).length === 1 && validID(record.contactID)
    ? record.contactID
    : null;
}

async function service() {
  if (!hasDesktopBridge()) throw new Error('Open the desktop app to call.');
  return import('../../../bindings/whatsup/internal/desktop/service');
}

export interface CallBridge {
  get(contactID: string): Promise<unknown>;
  start(contactID: string): Promise<unknown>;
  answer(contactID: string, callID: string): Promise<unknown>;
  end(contactID: string, callID: string): Promise<unknown>;
  setMuted(contactID: string, callID: string, muted: boolean): Promise<unknown>;
  subscribe(callback: (hint: unknown) => void): () => void;
}

export const callBridge: CallBridge = {
  get: async (contactID) => (await service()).GetCall(contactID),
  start: async (contactID) => (await service()).StartCall(contactID),
  answer: async (contactID, callID) =>
    (await service()).AnswerCall(contactID, callID),
  end: async (contactID, callID) =>
    (await service()).EndCall(contactID, callID),
  setMuted: async (contactID, callID, muted) =>
    (await service()).SetCallMuted(contactID, callID, muted),
  subscribe(callback) {
    return Events.On('whatsup:call-changed', (event) => callback(event.data));
  },
};

export interface MediaLink {
  send(frame: Uint8Array): void;
  close(): void;
}

export type OpenMedia = (
  callID: string,
  receive: (frame: ReceivedAudio) => void,
) => MediaLink;

/** Encoded audio to and from Go over the app's own origin. No port is opened. */
export const openStreamMedia: OpenMedia = (callID, receive) => {
  const socket = Stream(callMediaStream);
  socket.binaryType = 'arraybuffer';
  let ready = false;
  socket.addEventListener('open', () => {
    socket.send(encodeHello(callID) as Uint8Array<ArrayBuffer>);
    ready = true;
  });
  socket.addEventListener('message', (event) => {
    const frame = decodeAudio((event as MessageEvent).data);
    if (frame) receive(frame);
  });
  return {
    // Audio produced before the stream is up is dropped, never queued: late
    // audio is worse than none.
    send(frame) {
      if (ready && socket.readyState === socket.OPEN)
        socket.send(frame as Uint8Array<ArrayBuffer>);
    },
    close() {
      ready = false;
      socket.close();
    },
  };
};

export interface CallEngine {
  start(): Promise<void>;
  stop(): void;
  receive(timestamp: number, data: Uint8Array): void;
  setMuted(muted: boolean): void;
}

export type EngineFactory = (options: {
  send(timestamp: number, data: Uint8Array): void;
  failed(reason: AudioFailure): void;
}) => CallEngine;

const endNotices: Record<string, string> = {
  'peer-hung-up': 'Call ended.',
  'peer-declined': 'Call declined.',
  'peer-busy': 'They are on another call.',
  'peer-unavailable': 'They have voice calls switched off.',
  'no-answer': 'No answer.',
  missed: 'Missed call.',
  'connection-lost': 'Call ended: the connection was lost.',
  'media-failed': 'Call ended: audio could not be sent or received.',
  'device-error': 'Call ended: the microphone stopped working.',
  protocol: 'Call ended unexpectedly.',
  interrupted: 'Call ended.',
};

const audioNotices: Record<AudioFailure, string> = {
  permission:
    'WhatsUp could not use the microphone. Allow microphone access for WhatsUp, then call again.',
  'no-microphone': 'No microphone was found on this device.',
  unsupported: 'This device cannot run call audio.',
  failed: 'Call audio stopped working.',
};

export interface CallsState {
  views: Record<string, CallView>;
  /** The one call in progress on this device, in any non-idle state. */
  current: CallView | null;
  notice: string;
  busy: boolean;
}

const answered = (view: CallView | null) =>
  view !== null && (view.state === 'connecting' || view.state === 'active');

export function createCalls(options: {
  /** Storage is unlocked and the bridge is usable. */
  enabled: () => boolean;
  ringMuted: () => boolean;
  bridge?: CallBridge;
  engine?: EngineFactory;
  ringer?: Ringer;
  openMedia?: OpenMedia;
}) {
  const bridge = options.bridge ?? callBridge;
  const makeEngine: EngineFactory = options.engine ?? createAudioEngine;
  const ringer = options.ringer ?? createRinger(options.ringMuted);
  const openMedia = options.openMedia ?? openStreamMedia;

  let current: CallsState = {
    views: {},
    current: null,
    notice: '',
    busy: false,
  };
  const state = writable(current);
  const publish = (value: Partial<CallsState>) => {
    current = { ...current, ...value };
    state.set(current);
  };

  let generation = 0;
  const sequence: Record<string, number> = {};
  let ringing = '';
  let media: { callID: string; link: MediaLink; engine: CallEngine } | null =
    null;

  function stopMedia() {
    if (!media) return;
    const stale = media;
    media = null;
    // Release the microphone first; closing the stream cannot block that.
    stale.engine.stop();
    stale.link.close();
  }

  function startMedia(view: CallView) {
    stopMedia();
    const callID = view.callID;
    const contactID = view.contactID;
    const engine = makeEngine({
      send: (timestamp, data) => {
        if (media?.callID === callID)
          media.link.send(encodeAudio(timestamp, data));
      },
      failed: (reason) => {
        if (media?.callID !== callID) return;
        publish({ notice: audioNotices[reason] });
        void end(contactID, callID);
      },
    });
    const link = openMedia(callID, (frame) => {
      if (media?.callID === callID) engine.receive(frame.timestamp, frame.data);
    });
    media = { callID, link, engine };
    engine.setMuted(view.micMuted);
    void engine.start();
  }

  function settle(view: CallView | null, previous: CallView | null) {
    // Ring sounds follow the ringing states only.
    const ringKey =
      view && (view.state === 'incoming' || view.state === 'outgoing')
        ? view.state + view.callID
        : '';
    if (ringKey !== ringing) {
      ringing = ringKey;
      if (view?.state === 'incoming') ringer.ringtone();
      else if (view?.state === 'outgoing') ringer.ringback();
      else ringer.stop();
    }
    // The microphone follows answered calls only. A ringing call, incoming
    // or outgoing, never reaches this branch.
    if (answered(view)) {
      if (media?.callID !== view!.callID) startMedia(view!);
      else media.engine.setMuted(view!.micMuted);
    } else stopMedia();
    if (previous && previous.state !== 'idle' && !view) {
      const ended = current.views[previous.contactID];
      const text = ended ? (endNotices[ended.lastEnd] ?? '') : '';
      if (text && !current.notice) publish({ notice: text });
    }
  }

  function apply(view: CallView) {
    const previous = current.current;
    const views = { ...current.views, [view.contactID]: view };
    const live = Object.values(views).find((v) => v.state !== 'idle') ?? null;
    const starting = live && (!previous || previous.callID !== live.callID);
    publish({
      views,
      current: live,
      ...(starting ? { notice: '' } : {}),
    });
    settle(live, previous);
  }

  async function run(
    contactID: string,
    request: () => Promise<unknown>,
    quiet = false,
  ) {
    if (!options.enabled() || !validID(contactID)) return;
    const epoch = generation;
    const turn = (sequence[contactID] = (sequence[contactID] ?? 0) + 1);
    try {
      const view = validateCallView(await request(), contactID);
      if (epoch === generation && turn === sequence[contactID]) apply(view);
    } catch (error) {
      if (epoch !== generation || quiet) return;
      const message = error instanceof Error ? error.message : '';
      publish({
        notice: /^[A-Z][^<>{}]{0,160}$/.test(message)
          ? message
          : 'The call could not be changed. Try again.',
      });
      // The view may have changed even though the action failed.
      void run(contactID, () => bridge.get(contactID), true);
    }
  }

  async function act(contactID: string, request: () => Promise<unknown>) {
    if (current.busy) return;
    publish({ busy: true });
    try {
      await run(contactID, request);
    } finally {
      publish({ busy: false });
    }
  }

  const refresh = (contactID: string) =>
    run(contactID, () => bridge.get(contactID), true);

  function end(contactID: string, callID: string) {
    // Stop capturing before asking Go: hanging up must never wait on the
    // bridge to release the microphone.
    if (media?.callID === callID) stopMedia();
    return run(contactID, () => bridge.end(contactID, callID));
  }

  let unsubscribe: (() => void) | null = null;

  return {
    state,
    /** Start listening for call changes from Go. */
    listen() {
      unsubscribe?.();
      unsubscribe = bridge.subscribe((hint) => {
        const contactID = validateCallHint(hint);
        if (contactID) void refresh(contactID);
      });
    },
    refresh,
    start: (contactID: string) => act(contactID, () => bridge.start(contactID)),
    answer(view: CallView) {
      if (view.state !== 'incoming') return Promise.resolve();
      return act(view.contactID, () =>
        bridge.answer(view.contactID, view.callID),
      );
    },
    end(view: CallView) {
      if (view.state === 'idle') return Promise.resolve();
      return end(view.contactID, view.callID);
    },
    toggleMute(view: CallView) {
      if (!answered(view)) return Promise.resolve();
      return run(view.contactID, () =>
        bridge.setMuted(view.contactID, view.callID, !view.micMuted),
      );
    },
    dismissNotice: () => publish({ notice: '' }),
    /** Storage locked or the app is closing: release everything at once. */
    reset() {
      generation += 1;
      ringing = '';
      ringer.stop();
      stopMedia();
      publish({ views: {}, current: null, notice: '', busy: false });
    },
    destroy() {
      unsubscribe?.();
      unsubscribe = null;
      generation += 1;
      ringer.stop();
      stopMedia();
    },
  };
}
