import { describe, expect, it, vi } from 'vitest';
import { get } from 'svelte/store';
import type { AudioFailure, AudioStage } from '../call/audio';
import type { Ringer } from '../call/ring';
import {
  createMediaLink,
  createCalls,
  validateCallHint,
  validateCallView,
  type CallBridge,
  type CallView,
} from './calls';

vi.mock('@wailsio/runtime', () => ({
  Events: { On: () => () => {} },
  Stream: () => {
    throw new Error('the real media stream must not be opened in unit tests');
  },
}));
vi.mock('../call/ring', () => ({ createRinger: () => ({}) }));
vi.mock('../call/audio', () => ({ createAudioEngine: () => ({}) }));

const contactID = 'AQEBAQEBAQEBAQEBAQEBAQ';
const otherContact = 'AgICAgICAgICAgICAgICAg';
const callID = 'CQkJCQkJCQkJCQkJCQkJCQ';

function view(patch: Partial<CallView> = {}): CallView {
  return {
    contactID,
    state: 'idle',
    callID: '',
    peerRinging: false,
    micMuted: false,
    peerMicMuted: false,
    activeSince: 0,
    availability: 'ready',
    lastEnd: '',
    lastOutgoing: false,
    lastConnected: false,
    ...patch,
  };
}

function harness(endNoticeMs = 0) {
  const log: string[] = [];
  let hint: (value: unknown) => void = () => {};
  let serverView = view();
  const failures: ((reason: AudioFailure, stage?: AudioStage) => void)[] = [];
  const sent: Uint8Array[] = [];
  const engines: { started: number; stopped: number; muted: boolean[] }[] = [];
  const bridge: CallBridge = {
    get: vi.fn(async () => serverView),
    start: vi.fn(async () => {
      serverView = view({ state: 'outgoing', callID });
      return serverView;
    }),
    answer: vi.fn(async () => {
      serverView = view({ state: 'connecting', callID });
      return serverView;
    }),
    end: vi.fn(async () => {
      serverView = view({ lastEnd: 'hung-up' });
      return serverView;
    }),
    setMuted: vi.fn(async (_c, _i, muted) => {
      serverView = { ...serverView, micMuted: muted };
      return serverView;
    }),
    subscribe: (callback) => {
      hint = callback;
      return () => {
        hint = () => {};
      };
    },
  };
  const ringer: Ringer = {
    ringtone: () => log.push('ringtone'),
    ringback: () => log.push('ringback'),
    stop: () => log.push('ring-stop'),
  };
  let enabled = true;
  const calls = createCalls({
    enabled: () => enabled,
    ringMuted: () => false,
    endNoticeMs,
    bridge,
    ringer,
    engine: (options) => {
      const record = { started: 0, stopped: 0, muted: [] as boolean[] };
      engines.push(record);
      failures.push(options.failed);
      return {
        start: async () => {
          record.started += 1;
          log.push('mic-start');
          options.send(960, new Uint8Array([1, 2, 3]));
        },
        stop: () => {
          record.stopped += 1;
          log.push('mic-stop');
        },
        receive: () => log.push('audio-in'),
        setMuted: (muted) => record.muted.push(muted),
        stats: () => ({
          captured: 8,
          sent: 7,
          received: 6,
          decoded: 5,
          playedSeconds: 0.1,
          micLevel: 12,
          incomingLevel: 9,
          output: 'running' as const,
        }),
      };
    },
    openMedia: (id, receive) => {
      log.push('media-open:' + id);
      return {
        send: (frame) => {
          sent.push(frame);
          receive({
            timestamp: 960,
            recovered: false,
            data: new Uint8Array([9]),
          });
        },
        close: () => log.push('media-close'),
      };
    },
  });
  calls.listen();
  const push = async (next: CallView) => {
    serverView = next;
    hint({ contactID: next.contactID });
    await vi.waitFor(() =>
      expect(get(calls.state).views[next.contactID]).toEqual(next),
    );
  };
  return {
    calls,
    bridge,
    log,
    engines,
    failures,
    sent,
    push,
    setEnabled: (value: boolean) => (enabled = value),
    setServer: (next: CallView) => (serverView = next),
    hint: (value: unknown) => hint(value),
  };
}

describe('call view validation', () => {
  it('accepts the exact DTO and rejects anything else', () => {
    expect(validateCallView(view(), contactID)).toEqual(view());
    expect(
      validateCallView(view({ state: 'incoming', callID }), contactID).callID,
    ).toBe(callID);
    const bad: unknown[] = [
      null,
      [],
      { ...view(), extra: 1 },
      { ...view(), contactID: otherContact },
      { ...view(), state: 'ringing' },
      { ...view(), state: 'incoming', callID: '' },
      { ...view(), state: 'incoming', callID: 'A'.repeat(22) },
      { ...view(), callID },
      { ...view(), availability: 'maybe' },
      { ...view(), lastEnd: '<b>x</b>' },
      { ...view(), activeSince: -1 },
      { ...view(), micMuted: 'no' },
    ];
    for (const value of bad)
      expect(() => validateCallView(value, contactID)).toThrow();
    expect(validateCallHint({ contactID })).toBe(contactID);
    for (const value of [
      null,
      {},
      { contactID, state: 'incoming' },
      { contactID: 'nope' },
    ])
      expect(validateCallHint(value)).toBeNull();
  });
});

describe('calls', () => {
  it('rings for an incoming call without touching the microphone, then captures only once answered', async () => {
    const h = harness();
    await h.push(view({ state: 'incoming', callID }));
    expect(h.log).toEqual(['ringtone']);
    expect(h.engines).toHaveLength(0);
    expect(get(h.calls.state).current?.state).toBe('incoming');

    await h.calls.answer(get(h.calls.state).current!);
    expect(h.bridge.answer).toHaveBeenCalledWith(contactID, callID);
    expect(h.log).toEqual([
      'ringtone',
      'ring-stop',
      'media-open:' + callID,
      'mic-start',
      'audio-in',
    ]);
    expect(h.engines).toHaveLength(1);
    expect(h.sent).toHaveLength(1);
    expect([...h.sent[0]]).toEqual([1, 0, 0, 3, 192, 1, 2, 3]);

    // Becoming active keeps the same capture; nothing restarts.
    await h.push(view({ state: 'active', callID, activeSince: 1 }));
    expect(h.engines).toHaveLength(1);
    expect(h.engines[0].started).toBe(1);
  });

  it('plays ringback for an outgoing call and opens the microphone only after the answer', async () => {
    const h = harness();
    await h.calls.start(contactID);
    expect(h.log).toEqual(['ringback']);
    await h.push(view({ state: 'outgoing', callID, peerRinging: true }));
    expect(h.engines).toHaveLength(0);
    await h.push(view({ state: 'connecting', callID }));
    expect(h.log.slice(1)).toEqual([
      'ring-stop',
      'media-open:' + callID,
      'mic-start',
      'audio-in',
    ]);
  });

  it('releases the microphone before asking Go to hang up', async () => {
    const h = harness();
    await h.push(view({ state: 'active', callID, activeSince: 1 }));
    const order: string[] = [];
    h.bridge.end = vi.fn(async () => {
      order.push('bridge-end:mic-stopped=' + h.engines[0].stopped);
      return view({ lastEnd: 'hung-up' });
    });
    await h.calls.end(get(h.calls.state).current!);
    expect(order).toEqual(['bridge-end:mic-stopped=1']);
    expect(h.log.slice(-2)).toEqual(['mic-stop', 'media-close']);
    expect(get(h.calls.state).current).toBeNull();
    expect(get(h.calls.state).notice).toBe('');
  });

  it('stops capture and explains when the other side ends or the call drops', async () => {
    for (const [lastEnd, notice] of [
      ['peer-hung-up', 'Call ended.'],
      ['connection-lost', 'Call ended: the connection was lost.'],
      ['interrupted', 'Call ended.'],
    ]) {
      const h = harness();
      await h.push(view({ state: 'active', callID, activeSince: 1 }));
      await h.push(view({ lastEnd, lastConnected: true }));
      expect(h.engines[0].stopped).toBe(1);
      expect(h.log.slice(-2)).toEqual(['mic-stop', 'media-close']);
      expect(get(h.calls.state).notice).toBe(notice);
    }
  });

  it('tells the caller why an unanswered call ended and never opens the microphone', async () => {
    for (const [lastEnd, notice] of [
      ['peer-declined', 'Call declined.'],
      ['peer-busy', 'They are on another call.'],
      ['peer-unavailable', 'They have voice calls switched off.'],
      ['no-answer', 'No answer.'],
    ]) {
      const h = harness();
      await h.calls.start(contactID);
      await h.push(view({ lastEnd, lastOutgoing: true }));
      expect(get(h.calls.state).notice).toBe(notice);
      expect(h.engines).toHaveLength(0);
      expect(h.log).toEqual(['ringback', 'ring-stop']);
    }
  });

  it('ends the call and says so when the microphone is refused', async () => {
    const h = harness();
    await h.push(view({ state: 'connecting', callID }));
    h.failures[0]('permission');
    await vi.waitFor(() =>
      expect(h.bridge.end).toHaveBeenCalledWith(contactID, callID),
    );
    await vi.waitFor(() => expect(get(h.calls.state).current).toBeNull());
    expect(get(h.calls.state).notice).toContain('microphone');
    expect(h.engines[0].stopped).toBe(1);
  });

  it('clears an ordinary ending by itself but keeps a problem on screen', async () => {
    const ended = harness(400);
    await ended.calls.start(contactID);
    await ended.push(view({ lastEnd: 'no-answer', lastOutgoing: true }));
    expect(get(ended.calls.state).notice).toBe('No answer.');
    await vi.waitFor(() => expect(get(ended.calls.state).notice).toBe(''));

    const failed = harness(100);
    await failed.push(view({ state: 'connecting', callID }));
    failed.failures[0]('permission');
    await vi.waitFor(() => expect(get(failed.calls.state).current).toBeNull());
    await new Promise((resolve) => setTimeout(resolve, 250));
    expect(get(failed.calls.state).notice).toContain('microphone');
  });

  it('names the step that failed when the device cannot run call audio', async () => {
    const h = harness();
    await h.push(view({ state: 'connecting', callID }));
    h.failures[0]('unsupported', 'worklet');
    await vi.waitFor(() =>
      expect(h.bridge.end).toHaveBeenCalledWith(contactID, callID),
    );
    expect(get(h.calls.state).notice).toBe(
      'This device cannot run call audio. (step: worklet)',
    );
    expect(h.engines[0].stopped).toBe(1);
  });

  it('offers audio counters only while a call is answered', async () => {
    const h = harness();
    expect(h.calls.stats()).toBeNull();
    await h.push(view({ state: 'outgoing', callID, peerRinging: true }));
    expect(h.calls.stats()).toBeNull();
    await h.push(view({ state: 'active', callID, activeSince: 1 }));
    expect(h.calls.stats()).toMatchObject({ sent: 7, output: 'running' });
    await h.calls.end(get(h.calls.state).current!);
    expect(h.calls.stats()).toBeNull();
  });

  it('lock releases ring and microphone at once and ignores late answers', async () => {
    const h = harness();
    await h.push(view({ state: 'active', callID, activeSince: 1 }));
    let release: (value: CallView) => void = () => {};
    h.bridge.get = vi.fn(
      () => new Promise<CallView>((resolve) => (release = resolve)),
    );
    h.hint({ contactID });
    h.calls.reset();
    expect(h.engines[0].stopped).toBe(1);
    expect(get(h.calls.state)).toEqual({
      views: {},
      current: null,
      notice: '',
      busy: false,
    });
    release(view({ state: 'active', callID, activeSince: 1 }));
    await Promise.resolve();
    await Promise.resolve();
    expect(get(h.calls.state).current).toBeNull();
    expect(h.engines).toHaveLength(1);
  });

  it('does nothing while storage is locked and ignores malformed hints and views', async () => {
    const h = harness();
    h.setEnabled(false);
    await h.calls.start(contactID);
    h.hint({ contactID });
    expect(h.bridge.start).not.toHaveBeenCalled();
    expect(h.bridge.get).not.toHaveBeenCalled();
    h.setEnabled(true);
    h.hint({ contactID, extra: true });
    h.hint('call');
    expect(h.bridge.get).not.toHaveBeenCalled();
    h.setServer({ ...view(), state: 'incoming' } as CallView);
    h.hint({ contactID });
    await vi.waitFor(() => expect(h.bridge.get).toHaveBeenCalledTimes(1));
    expect(get(h.calls.state).current).toBeNull();
    expect(h.log).toEqual([]);
  });

  it('shows a fixed message when an action fails and re-reads the call', async () => {
    const h = harness();
    h.bridge.start = vi.fn(async () => {
      throw new Error('You are already in a call');
    });
    await h.calls.start(contactID);
    expect(get(h.calls.state).notice).toBe('You are already in a call');
    h.bridge.start = vi.fn(async () => {
      throw new Error('<script>alert(1)</script>');
    });
    await h.calls.start(contactID);
    expect(get(h.calls.state).notice).toBe(
      'The call could not be changed. Try again.',
    );
    expect(h.bridge.get).toHaveBeenCalled();
  });

  it('mute follows the view Go returns and reaches the running capture', async () => {
    const h = harness();
    await h.push(view({ state: 'active', callID, activeSince: 1 }));
    await h.calls.toggleMute(get(h.calls.state).current!);
    expect(h.bridge.setMuted).toHaveBeenCalledWith(contactID, callID, true);
    expect(get(h.calls.state).current?.micMuted).toBe(true);
    expect(h.engines[0].muted.at(-1)).toBe(true);
    expect(h.engines[0].started).toBe(1);
  });
});

describe('call media link', () => {
  function fakeSockets() {
    const sockets: {
      readyState: number;
      binaryType: string;
      sent: Uint8Array[];
      closed: boolean;
      fire(type: string, data?: unknown): void;
    }[] = [];
    const connect = () => {
      const listeners: Record<string, ((event: Event) => void)[]> = {};
      const socket = {
        readyState: 0,
        binaryType: '',
        sent: [] as Uint8Array[],
        closed: false,
        send(data: Uint8Array) {
          socket.sent.push(data);
        },
        close() {
          socket.closed = true;
          socket.readyState = 3;
        },
        addEventListener(type: string, listener: (event: Event) => void) {
          (listeners[type] ??= []).push(listener);
        },
        fire(type: string, data?: unknown) {
          if (type === 'open') socket.readyState = 1;
          if (type === 'close') socket.readyState = 3;
          for (const listener of listeners[type] ?? [])
            listener({ data } as unknown as Event);
        },
      };
      sockets.push(socket);
      return socket;
    };
    return { sockets, connect };
  }
  const audio = new Uint8Array([1, 0, 0, 0, 3, 192, 5, 6]);

  it('names the call when the stream opens and drops audio until then', () => {
    const { sockets, connect } = fakeSockets();
    const received: number[] = [];
    const link = createMediaLink(connect, callID, (frame) =>
      received.push(frame.timestamp),
    );
    expect(link.state?.()).toBe('connecting');
    link.send(new Uint8Array([1, 2]));
    expect(sockets[0].sent).toHaveLength(0);
    sockets[0].fire('open');
    expect(link.state?.()).toBe('open');
    expect(sockets[0].sent[0][0]).toBe(0);
    expect(sockets[0].sent[0]).toHaveLength(23);
    link.send(new Uint8Array([1, 2]));
    expect(sockets[0].sent).toHaveLength(2);
    sockets[0].fire('message', audio);
    expect(received).toEqual([960]);
  });

  it('opens a new stream and names the call again when the stream ends during the call', async () => {
    const { sockets, connect } = fakeSockets();
    const received: number[] = [];
    const link = createMediaLink(
      connect,
      callID,
      (frame) => received.push(frame.timestamp),
      5,
    );
    sockets[0].fire('open');
    sockets[0].fire('close');
    expect(link.state?.()).toBe('connecting');
    link.send(new Uint8Array([1, 2])); // nothing to send on
    await vi.waitFor(() => expect(sockets).toHaveLength(2));
    sockets[1].fire('open');
    expect(sockets[1].sent[0]).toHaveLength(23);
    expect(link.state?.()).toBe('open');
    // A late event from the dead socket is ignored.
    sockets[0].fire('message', audio);
    expect(received).toEqual([]);
    sockets[1].fire('message', audio);
    expect(received).toEqual([960]);
  });

  it('stops for good when the call closes it, and gives up after a bounded number of tries', async () => {
    const closing = fakeSockets();
    const link = createMediaLink(closing.connect, callID, () => {}, 5);
    closing.sockets[0].fire('open');
    link.close();
    expect(closing.sockets[0].closed).toBe(true);
    expect(link.state?.()).toBe('closed');
    closing.sockets[0].fire('close');
    await new Promise((resolve) => setTimeout(resolve, 30));
    expect(closing.sockets).toHaveLength(1);

    const failing = fakeSockets();
    const dead = createMediaLink(failing.connect, callID, () => {}, 1);
    for (let i = 0; i < 6; i++) {
      await vi.waitFor(() => expect(failing.sockets).toHaveLength(i + 1));
      failing.sockets[i].fire('close');
    }
    await new Promise((resolve) => setTimeout(resolve, 30));
    expect(failing.sockets).toHaveLength(6);
    expect(dead.state?.()).toBe('closed');
  });
});
