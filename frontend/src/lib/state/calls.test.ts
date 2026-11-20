import { describe, expect, it, vi } from 'vitest';
import { get } from 'svelte/store';
import type { AudioFailure, AudioStage } from '../call/audio';
import type { ReceivedMedia } from '../call/protocol';
import type { VideoFailure } from '../call/video';
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
const otherCallID = 'CgoKCgoKCgoKCgoKCgoKCg';

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
    video: false,
    cameraOn: false,
    peerCameraOn: false,
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
  // Video engines the store created, with what it did to each.
  const videos: {
    camera: 'off' | 'starting' | 'on';
    cameraStarts: number;
    cameraStops: number;
    stopped: number;
    keyframes: number;
    received: { timestamp: number; key: boolean }[];
    canvas: unknown;
    forgotten: number;
    fail: (reason: VideoFailure) => void;
  }[] = [];
  let relayedNow = false;
  let focused = true;
  let onFocus: () => void = () => {};
  // What the next attempt to start a camera does.
  let cameraStart: 'on' | VideoFailure = 'on';
  // While set, the desktop has not yet answered a camera request.
  let cameraGate: Promise<void> | null = null;
  let deliver: (frame: ReceivedMedia) => void = () => {};
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
    setCamera: vi.fn(async (_c, _i, on, overRelay) => {
      log.push('desktop-camera:' + (on ? 'on' : 'off'));
      if (on && relayedNow && !overRelay)
        throw new Error('This call is using a relay');
      if (cameraGate) await cameraGate;
      serverView = { ...serverView, cameraOn: on };
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
    relayed: () => relayedNow,
    windowFocus: {
      has: () => focused,
      subscribe: (callback) => {
        onFocus = callback;
        return () => {
          onFocus = () => {};
        };
      },
    },
    video: (options) => {
      const record = {
        camera: 'off' as 'off' | 'starting' | 'on',
        cameraStarts: 0,
        cameraStops: 0,
        stopped: 0,
        keyframes: 0,
        received: [] as { timestamp: number; key: boolean }[],
        canvas: undefined as unknown,
        forgotten: 0,
        fail: options.failed,
      };
      videos.push(record);
      return {
        startCamera: async () => {
          record.cameraStarts += 1;
          log.push('camera-start');
          if (cameraStart !== 'on') {
            options.failed(cameraStart);
            return;
          }
          record.camera = 'on';
          options.changed();
        },
        stopCamera: () => {
          record.cameraStops += 1;
          log.push('camera-stop');
          record.camera = 'off';
          options.changed();
        },
        stop: () => {
          record.stopped += 1;
          record.camera = 'off';
          log.push('video-stop');
        },
        keyframe: () => {
          record.keyframes += 1;
        },
        receive: (timestamp, key) => record.received.push({ timestamp, key }),
        setCanvas: (canvas) => {
          record.canvas = canvas;
        },
        localStream: () => null,
        camera: () => record.camera,
        hasPicture: () => record.received.length > 0,
        forgetPicture: () => {
          record.forgotten += 1;
          record.received.length = 0;
          options.changed();
        },
        stats: () => ({
          camera: record.camera,
          captured: 4,
          sent: 3,
          keyframesSent: 1,
          received: record.received.length,
          keyframesReceived: 0,
          decoded: 0,
          shown: 0,
          waitingForKeyframe: true,
        }),
      };
    },
    openMedia: (id, receive) => {
      log.push('media-open:' + id);
      deliver = receive;
      return {
        send: (frame) => {
          sent.push(frame);
          receive({
            kind: 'audio',
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
    // The desktop changes a video view itself when the camera goes on, so wait
    // for the call and its state rather than for an unchanged view.
    await vi.waitFor(() =>
      expect(get(calls.state).views[next.contactID]).toMatchObject({
        callID: next.callID,
        state: next.state,
        lastEnd: next.lastEnd,
        micMuted: next.micMuted,
        peerRinging: next.peerRinging,
      }),
    );
  };
  return {
    calls,
    bridge,
    log,
    engines,
    failures,
    videos,
    setRelayed: (value: boolean) => (relayedNow = value),
    setCameraStart: (value: 'on' | VideoFailure) => (cameraStart = value),
    /** The desktop holds its answers to camera requests until released. */
    holdCamera() {
      let release: () => void = () => {};
      cameraGate = new Promise<void>((resolve) => (release = resolve));
      return () => {
        cameraGate = null;
        release();
      };
    },
    /** The window goes to the back, or comes to the front. */
    setFocused(value: boolean) {
      const cameForward = value && !focused;
      focused = value;
      if (cameForward) onFocus();
    },
    deliver: (frame: ReceivedMedia) => deliver(frame),
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
    expect(h.bridge.answer).toHaveBeenCalledWith(contactID, callID, false);
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

  it('keeps the microphone off when a hang-up fails and a refresh still shows the call answered', async () => {
    const h = harness();
    const live = view({ state: 'active', callID, activeSince: 1 });
    await h.push(live);
    expect(h.engines).toHaveLength(1);
    // The desktop refuses the hang-up and still reports the call as active.
    h.bridge.end = vi.fn(async () => {
      throw new Error('The call could not be changed');
    });
    await h.calls.end(get(h.calls.state).current!);
    expect(h.engines[0].stopped).toBe(1);
    // The refresh after the failure, and any later hint, show it active.
    h.hint({ contactID });
    h.hint({ contactID });
    await new Promise((resolve) => setTimeout(resolve, 60));
    expect(get(h.calls.state).current?.state).toBe('active');
    expect(h.engines).toHaveLength(1);
    expect(h.engines[0].started).toBe(1);
    expect(h.log.filter((entry) => entry === 'mic-start')).toHaveLength(1);
    // A different call still gets its microphone.
    await h.push(
      view({ state: 'active', callID: otherCallID, activeSince: 2 }),
    );
    expect(h.engines).toHaveLength(2);
  });

  it('stops capture at once when voice calls are being switched off, and keeps it stopped for that call', async () => {
    const h = harness();
    await h.push(view({ state: 'active', callID, activeSince: 1 }));
    expect(h.engines[0].started).toBe(1);
    h.calls.stopCapture(); // before the desktop has been asked anything
    expect(h.engines[0].stopped).toBe(1);
    expect(h.bridge.end).not.toHaveBeenCalled();
    expect(h.calls.stats()).toBeNull();
    // The desktop is slow to end the call: views still say active.
    h.hint({ contactID });
    await new Promise((resolve) => setTimeout(resolve, 60));
    expect(h.engines).toHaveLength(1);

    // A ringing call is silenced too, and answering it later starts nothing.
    const ringing = harness();
    await ringing.push(view({ state: 'incoming', callID }));
    expect(ringing.log).toContain('ringtone');
    ringing.calls.stopCapture();
    expect(ringing.log.at(-1)).toBe('ring-stop');
    await ringing.push(view({ state: 'connecting', callID }));
    expect(ringing.engines).toHaveLength(0);
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
      camera: 'off',
      cameraHeld: false,
      remotePicture: false,
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

describe('video calls', () => {
  const video = (patch: Partial<CallView> = {}) =>
    view({ state: 'active', callID, activeSince: 1, video: true, ...patch });

  it('creates no video engine and never touches the camera for a voice call', async () => {
    const h = harness();
    await h.push(view({ state: 'active', callID, activeSince: 1 }));
    expect(h.videos).toHaveLength(0);
    expect(h.bridge.setCamera).not.toHaveBeenCalled();
    expect(get(h.calls.state).camera).toBe('off');
    await h.calls.toggleCamera(get(h.calls.state).current!);
    expect(h.bridge.setCamera).not.toHaveBeenCalled();
    expect(h.calls.stats()?.video).toBeNull();
  });

  it('tells the desktop the camera is on before starting it, and sends pictures on the media link', async () => {
    const h = harness();
    await h.push(video({ state: 'connecting' }));
    await vi.waitFor(() => expect(get(h.calls.state).camera).toBe('on'));
    expect(h.videos).toHaveLength(1);
    expect(h.bridge.setCamera).toHaveBeenCalledWith(
      contactID,
      callID,
      true,
      false,
    );
    // The desktop opens the camera permission for a camera it knows is on.
    expect(h.log.indexOf('desktop-camera:on')).toBeLessThan(
      h.log.indexOf('camera-start'),
    );
    expect(h.log.indexOf('desktop-camera:on')).toBeGreaterThan(-1);
    expect(get(h.calls.state).cameraHeld).toBe(false);
    expect(h.calls.stats()?.video).toMatchObject({ camera: 'on', sent: 3 });
  });

  it('routes incoming audio, video and the keyframe signal to the right engine', async () => {
    const h = harness();
    await h.push(video());
    await vi.waitFor(() => expect(get(h.calls.state).camera).toBe('on'));
    expect(get(h.calls.state).remotePicture).toBe(false);
    const audioBefore = h.log.filter((entry) => entry === 'audio-in').length;
    h.deliver({
      kind: 'video',
      timestamp: 90000,
      key: true,
      data: new Uint8Array([1]),
    });
    h.deliver({ kind: 'keyframe' });
    h.deliver({
      kind: 'audio',
      timestamp: 960,
      recovered: false,
      data: new Uint8Array([2]),
    });
    expect(h.videos[0].received).toEqual([{ timestamp: 90000, key: true }]);
    expect(h.videos[0].keyframes).toBe(1);
    expect(h.log.filter((entry) => entry === 'audio-in')).toHaveLength(
      audioBefore + 1,
    );
  });

  it("starts the camera when a later refresh overtakes the desktop's answer to the camera request", async () => {
    // A call that has just been answered changes state several times in its
    // first moments, and each change refreshes the view. The camera must not
    // depend on which of those answers arrives last.
    const h = harness();
    const release = h.holdCamera();
    await h.push(video({ state: 'connecting' }));
    await vi.waitFor(() => expect(h.bridge.setCamera).toHaveBeenCalledTimes(1));
    await h.push(video({ state: 'active' }));
    expect(h.videos[0].cameraStarts).toBe(0);
    release();
    await vi.waitFor(() => expect(get(h.calls.state).camera).toBe('on'));
    expect(h.videos[0].cameraStarts).toBe(1);
    expect(get(h.calls.state).notice).toBe('');
  });

  it('starts video that a relay held back once the connection is direct, without being asked', async () => {
    const h = harness();
    h.setRelayed(true);
    await h.push(video({ state: 'connecting' }));
    await vi.waitFor(() => expect(get(h.calls.state).cameraHeld).toBe(true));
    expect(h.bridge.setCamera).not.toHaveBeenCalled();
    // Still relayed: a path report changes nothing.
    h.calls.pathChanged(contactID);
    await new Promise((resolve) => setTimeout(resolve, 40));
    expect(h.bridge.setCamera).not.toHaveBeenCalled();
    // Direct now.
    h.setRelayed(false);
    h.calls.pathChanged(otherContact);
    await new Promise((resolve) => setTimeout(resolve, 40));
    expect(h.bridge.setCamera).not.toHaveBeenCalled();
    h.calls.pathChanged(contactID);
    await vi.waitFor(() => expect(get(h.calls.state).camera).toBe('on'));
    expect(get(h.calls.state).cameraHeld).toBe(false);
    expect(h.bridge.setCamera).toHaveBeenLastCalledWith(
      contactID,
      callID,
      true,
      false,
    );
    // A camera the person switched off themselves is not switched back on.
    await h.calls.toggleCamera(get(h.calls.state).current!, false);
    await vi.waitFor(() => expect(get(h.calls.state).camera).toBe('off'));
    h.calls.pathChanged(contactID);
    await new Promise((resolve) => setTimeout(resolve, 40));
    expect(get(h.calls.state).camera).toBe('off');
    expect(h.videos[0].cameraStarts).toBe(1);
  });

  it('stops video when the connection falls back to a relay, unless sending over a relay was already chosen for this call', async () => {
    const h = harness();
    await h.push(video({ state: 'connecting' }));
    await vi.waitFor(() => expect(get(h.calls.state).camera).toBe('on'));
    // Direct so far. The connection falls back to a relay part-way through.
    h.setRelayed(true);
    h.calls.pathChanged(contactID);
    await vi.waitFor(() => expect(get(h.calls.state).camera).toBe('off'));
    expect(get(h.calls.state).cameraHeld).toBe(true);
    await vi.waitFor(() =>
      expect(h.bridge.setCamera).toHaveBeenLastCalledWith(
        contactID,
        callID,
        false,
        false,
      ),
    );
    expect(h.videos[0].cameraStops).toBe(1);
    // The call itself carries on.
    expect(h.bridge.end).not.toHaveBeenCalled();
    // The person chooses to send over the relay: video is back, and a later
    // path report while still relayed leaves it alone.
    await h.calls.toggleCamera(get(h.calls.state).current!, true);
    await vi.waitFor(() => expect(get(h.calls.state).camera).toBe('on'));
    expect(get(h.calls.state).cameraHeld).toBe(false);
    h.calls.pathChanged(contactID);
    await new Promise((resolve) => setTimeout(resolve, 40));
    expect(get(h.calls.state).camera).toBe('on');
    // Direct again and relayed again within the same call: the choice stands.
    h.setRelayed(false);
    h.calls.pathChanged(contactID);
    h.setRelayed(true);
    h.calls.pathChanged(contactID);
    await new Promise((resolve) => setTimeout(resolve, 40));
    expect(get(h.calls.state).camera).toBe('on');
    expect(h.videos[0].cameraStops).toBe(1);
  });

  it('does not start the camera when the connection turns relayed while the desktop is still being asked', async () => {
    const h = harness();
    const release = h.holdCamera();
    await h.push(video({ state: 'connecting' }));
    await vi.waitFor(() => expect(h.bridge.setCamera).toHaveBeenCalledTimes(1));
    // Asked while direct. The connection falls back before the answer comes:
    // the camera is still off, so the path report alone stops nothing.
    h.setRelayed(true);
    h.calls.pathChanged(contactID);
    expect(h.videos[0].cameraStarts).toBe(0);
    release();
    // The approval arrives. No camera opens, the approval is taken back and
    // the choice is offered.
    await vi.waitFor(() =>
      expect(h.bridge.setCamera).toHaveBeenLastCalledWith(
        contactID,
        callID,
        false,
        false,
      ),
    );
    await vi.waitFor(() => expect(get(h.calls.state).cameraHeld).toBe(true));
    expect(h.videos[0].cameraStarts).toBe(0);
    expect(get(h.calls.state).camera).toBe('off');
    expect(h.bridge.end).not.toHaveBeenCalled();
    // Choosing to send over the relay still works afterwards.
    await h.calls.toggleCamera(get(h.calls.state).current!, true);
    await vi.waitFor(() => expect(get(h.calls.state).camera).toBe('on'));
    expect(h.videos[0].cameraStarts).toBe(1);
  });

  it("says so when this device's camera picture is stuck, and carries on with voice", async () => {
    const h = harness();
    h.setCameraStart('stuck');
    await h.push(video({ state: 'connecting' }));
    await vi.waitFor(() =>
      expect(get(h.calls.state).notice).toBe(
        "This device's camera picture is not updating, so your video was switched off. The call continues with voice.",
      ),
    );
    await vi.waitFor(() =>
      expect(h.bridge.setCamera).toHaveBeenLastCalledWith(
        contactID,
        callID,
        false,
        false,
      ),
    );
    expect(get(h.calls.state).camera).toBe('off');
    expect(get(h.calls.state).current?.state).toBe('connecting');
    expect(h.bridge.end).not.toHaveBeenCalled();
  });

  it('forgets their picture when their camera goes off, so a stale frame never stands in for them', async () => {
    const h = harness();
    await h.push(video({ peerCameraOn: true }));
    await vi.waitFor(() => expect(get(h.calls.state).camera).toBe('on'));
    h.deliver({
      kind: 'video',
      timestamp: 1,
      key: true,
      data: new Uint8Array([1]),
    });
    expect(h.videos[0].received).toHaveLength(1);
    // Their camera is still on: nothing is forgotten by an ordinary refresh.
    h.hint({ contactID });
    await new Promise((resolve) => setTimeout(resolve, 40));
    expect(h.videos[0].forgotten).toBe(0);
    await h.push(video({ cameraOn: true, peerCameraOn: false }));
    await vi.waitFor(() => expect(h.videos[0].forgotten).toBe(1));
    expect(get(h.calls.state).remotePicture).toBe(false);
    // Off stays forgotten once; it is not blanked again on every refresh.
    h.hint({ contactID });
    await new Promise((resolve) => setTimeout(resolve, 40));
    expect(h.videos[0].forgotten).toBe(1);
  });

  it('holds video on a relayed call until the person chooses to send it', async () => {
    const h = harness();
    h.setRelayed(true);
    await h.push(video());
    await new Promise((resolve) => setTimeout(resolve, 40));
    expect(get(h.calls.state).cameraHeld).toBe(true);
    expect(h.bridge.setCamera).not.toHaveBeenCalled();
    expect(h.videos[0].cameraStarts).toBe(0);
    // Incoming video is still shown: only sending is held.
    h.deliver({
      kind: 'video',
      timestamp: 1,
      key: true,
      data: new Uint8Array([1]),
    });
    expect(h.videos[0].received).toHaveLength(1);
    // An ordinary camera press is refused by the desktop and stays held.
    await h.calls.toggleCamera(get(h.calls.state).current!);
    expect(h.videos[0].cameraStarts).toBe(0);
    expect(get(h.calls.state).cameraHeld).toBe(true);
    // The explicit choice goes through.
    await h.calls.toggleCamera(get(h.calls.state).current!, true);
    expect(h.bridge.setCamera).toHaveBeenLastCalledWith(
      contactID,
      callID,
      true,
      true,
    );
    expect(h.videos[0].cameraStarts).toBe(1);
    expect(get(h.calls.state).camera).toBe('on');
    expect(get(h.calls.state).cameraHeld).toBe(false);
  });

  it('switches the camera off here first, then tells the desktop', async () => {
    const h = harness();
    await h.push(video());
    await vi.waitFor(() => expect(get(h.calls.state).camera).toBe('on'));
    h.log.length = 0;
    await h.calls.toggleCamera(get(h.calls.state).current!);
    expect(h.log.slice(0, 2)).toEqual(['camera-stop', 'desktop-camera:off']);
    expect(get(h.calls.state).camera).toBe('off');
    // The call and its microphone carry on.
    expect(h.engines[0].stopped).toBe(0);
    expect(get(h.calls.state).current?.state).toBe('active');
  });

  it('tries the camera once more when the window comes forward, if it was turned down while the window was behind', async () => {
    const h = harness();
    h.setFocused(false); // the person is at the other device
    h.setCameraStart('unavailable');
    await h.push(video({ state: 'connecting' }));
    await vi.waitFor(() => expect(h.videos).toHaveLength(1));
    await vi.waitFor(() =>
      expect(get(h.calls.state).notice).toContain(
        'Bring WhatsUp to the front and press the camera button',
      ),
    );
    await vi.waitFor(() =>
      expect(h.bridge.setCamera).toHaveBeenLastCalledWith(
        contactID,
        callID,
        false,
        false,
      ),
    );
    expect(h.videos[0].cameraStarts).toBe(1);
    expect(get(h.calls.state).camera).toBe('off');
    // Still behind: nothing more is tried.
    await new Promise((resolve) => setTimeout(resolve, 60));
    expect(h.videos[0].cameraStarts).toBe(1);

    h.setCameraStart('on');
    h.setFocused(true);
    await vi.waitFor(() => expect(get(h.calls.state).camera).toBe('on'));
    expect(h.videos[0].cameraStarts).toBe(2);
    expect(get(h.calls.state).notice).toBe('');
    // Once only: going behind and coming forward again starts nothing new.
    h.setFocused(false);
    h.setFocused(true);
    await new Promise((resolve) => setTimeout(resolve, 60));
    expect(h.videos[0].cameraStarts).toBe(2);
  });

  it('does not retry the camera on its own when it was turned down with the window in front, or for another kind of failure', async () => {
    for (const [result, behind] of [
      ['permission', false],
      ['no-camera', true],
      ['no-picture', true],
    ] as const) {
      const h = harness();
      h.setFocused(!behind);
      h.setCameraStart(result);
      await h.push(video({ state: 'connecting' }));
      await vi.waitFor(() => expect(h.videos).toHaveLength(1));
      await vi.waitFor(() => expect(get(h.calls.state).notice).not.toBe(''));
      await vi.waitFor(() => expect(h.videos[0].cameraStarts).toBe(1));
      h.setCameraStart('on');
      h.setFocused(false);
      h.setFocused(true);
      await new Promise((resolve) => setTimeout(resolve, 60));
      expect(h.videos[0].cameraStarts).toBe(1);
    }
    // And a retry that was pending is dropped when the person presses the
    // camera button themselves, or the call ends.
    const pressed = harness();
    pressed.setFocused(false);
    pressed.setCameraStart('unavailable');
    await pressed.push(video({ state: 'connecting' }));
    await vi.waitFor(() => expect(pressed.videos).toHaveLength(1));
    await vi.waitFor(() =>
      expect(get(pressed.calls.state).notice).not.toBe(''),
    );
    await vi.waitFor(() => expect(pressed.videos[0].cameraStarts).toBe(1));
    pressed.setCameraStart('on');
    await pressed.calls.end(get(pressed.calls.state).current!);
    pressed.setFocused(true);
    await new Promise((resolve) => setTimeout(resolve, 60));
    expect(pressed.videos[0].cameraStarts).toBe(1);
  });

  it('keeps the call going with voice when the camera cannot be used', async () => {
    const h = harness();
    await h.push(video({ state: 'connecting' }));
    await vi.waitFor(() => expect(h.videos).toHaveLength(1));
    await vi.waitFor(() => expect(get(h.calls.state).camera).toBe('on'));
    h.videos[0].fail('no-picture');
    await vi.waitFor(() =>
      expect(h.bridge.setCamera).toHaveBeenLastCalledWith(
        contactID,
        callID,
        false,
        false,
      ),
    );
    expect(get(h.calls.state).notice).toContain('camera gave no picture');
    expect(get(h.calls.state).notice).toContain('continues with voice');
    expect(h.engines[0].stopped).toBe(0);
    expect(h.bridge.end).not.toHaveBeenCalled();
    expect(get(h.calls.state).current?.state).toBe('connecting');
  });

  it('releases the camera and decoder before asking the desktop to hang up, and never restarts them for that call', async () => {
    const h = harness();
    await h.push(video());
    await vi.waitFor(() => expect(get(h.calls.state).camera).toBe('on'));
    h.log.length = 0;
    h.bridge.end = vi.fn(async () => {
      h.log.push('desktop-end');
      throw new Error('The call could not be changed');
    });
    await h.calls.end(get(h.calls.state).current!);
    expect(h.log.indexOf('video-stop')).toBeGreaterThan(-1);
    expect(h.log.indexOf('video-stop')).toBeLessThan(
      h.log.indexOf('desktop-end'),
    );
    expect(h.log.indexOf('mic-stop')).toBeLessThan(
      h.log.indexOf('desktop-end'),
    );
    expect(get(h.calls.state)).toMatchObject({
      camera: 'off',
      cameraHeld: false,
      remotePicture: false,
    });
    h.hint({ contactID });
    await new Promise((resolve) => setTimeout(resolve, 60));
    expect(h.videos).toHaveLength(1);
    expect(h.videos[0].cameraStarts).toBe(1);
  });

  it('starts a fresh video engine and camera for a second video call', async () => {
    const h = harness();
    await h.push(video());
    await vi.waitFor(() => expect(get(h.calls.state).camera).toBe('on'));
    await h.push(view({ lastEnd: 'peer-hung-up', lastConnected: true }));
    expect(h.videos[0].stopped).toBe(1);
    expect(get(h.calls.state).camera).toBe('off');
    await h.push(video({ callID: otherCallID }));
    await vi.waitFor(() => expect(h.videos).toHaveLength(2));
    await vi.waitFor(() => expect(get(h.calls.state).camera).toBe('on'));
    expect(h.videos[1].cameraStarts).toBe(1);
    expect(h.log.filter((entry) => entry.startsWith('media-open:'))).toEqual([
      'media-open:' + callID,
      'media-open:' + otherCallID,
    ]);
  });

  it('asks for video when placing or answering a video call, and only when it was offered', async () => {
    const h = harness();
    await h.calls.start(contactID, true);
    expect(h.bridge.start).toHaveBeenLastCalledWith(contactID, true);
    const other = harness();
    await other.push(view({ state: 'incoming', callID, video: true }));
    await other.calls.answer(get(other.calls.state).current!, true);
    expect(other.bridge.answer).toHaveBeenLastCalledWith(
      contactID,
      callID,
      true,
    );
    const voice = harness();
    await voice.push(view({ state: 'incoming', callID }));
    await voice.calls.answer(get(voice.calls.state).current!, true);
    expect(voice.bridge.answer).toHaveBeenLastCalledWith(
      contactID,
      callID,
      false,
    );
    const declined = harness();
    await declined.push(view({ state: 'incoming', callID, video: true }));
    await declined.calls.answer(get(declined.calls.state).current!);
    expect(declined.bridge.answer).toHaveBeenLastCalledWith(
      contactID,
      callID,
      false,
    );
  });

  it('accepts camera flags only on a video call in progress', () => {
    const base = view({ state: 'active', callID, activeSince: 1 });
    expect(
      validateCallView(
        { ...base, video: true, cameraOn: true, peerCameraOn: true },
        contactID,
      ),
    ).toMatchObject({ video: true, cameraOn: true, peerCameraOn: true });
    for (const bad of [
      { ...base, cameraOn: true },
      { ...base, peerCameraOn: true },
      { ...view(), video: true },
      { ...base, video: 'yes' },
      (({ video: _video, ...rest }) => rest)(base),
    ])
      expect(() => validateCallView(bad, contactID)).toThrow();
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
      received.push(frame.kind === 'audio' ? frame.timestamp : -1),
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
      (frame) => received.push(frame.kind === 'audio' ? frame.timestamp : -1),
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
