import { describe, expect, it, vi } from 'vitest';
import { createAudioEngine, type AudioPipeline } from './audio';
import {
  decodeAudio,
  encodeAudio,
  encodeHello,
  maxAudioFrame,
  validCallID,
} from './protocol';

vi.mock('./capture.worklet.js?url', () => ({ default: '/capture.js' }));
vi.mock('./playback.worklet.js?url', () => ({ default: '/playback.js' }));
vi.mock('./context', () => ({
  sharedAudioContext: () => {
    throw new Error('the real audio context must not be used in unit tests');
  },
}));

function fakeStream() {
  const track = { stop: vi.fn(), enabled: true };
  return {
    track,
    stream: {
      getTracks: () => [track],
      getAudioTracks: () => [track],
    } as unknown as MediaStream,
  };
}

function fakePipeline() {
  const pipeline = {
    setMuted: vi.fn(),
    receive: vi.fn(),
    close: vi.fn(),
  };
  return pipeline as AudioPipeline & typeof pipeline;
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe('call audio engine', () => {
  it('asks for audio only, with no video', async () => {
    const { stream } = fakeStream();
    const getUserMedia = vi.fn(
      async (_constraints: MediaStreamConstraints) => stream,
    );
    const engine = createAudioEngine({
      send: () => {},
      failed: () => {},
      deps: { getUserMedia, createPipeline: async () => fakePipeline() },
    });
    await engine.start();
    expect(getUserMedia).toHaveBeenCalledTimes(1);
    const constraints = getUserMedia.mock.calls[0][0];
    expect(constraints.video).toBe(false);
    expect(constraints.audio).toMatchObject({ echoCancellation: true });
  });

  it('releases the microphone when stopped while the permission request is still pending', async () => {
    const pending = deferred<MediaStream>();
    const createPipeline = vi.fn(async () => fakePipeline());
    const failed = vi.fn();
    const engine = createAudioEngine({
      send: () => {},
      failed,
      deps: { getUserMedia: () => pending.promise, createPipeline },
    });
    const starting = engine.start();
    engine.stop(); // hang up, calls off or lock, with the prompt still open
    const { stream, track } = fakeStream();
    pending.resolve(stream); // the person clicks Allow afterwards
    await starting;
    expect(track.stop).toHaveBeenCalledTimes(1);
    expect(createPipeline).not.toHaveBeenCalled();
    expect(failed).not.toHaveBeenCalled();
    expect(engine.isRunning()).toBe(false);
  });

  it('releases everything when stopped while the audio chain is still being built', async () => {
    const building = deferred<AudioPipeline>();
    const { stream, track } = fakeStream();
    const engine = createAudioEngine({
      send: () => {},
      failed: () => {},
      deps: {
        getUserMedia: async () => stream,
        createPipeline: () => building.promise,
      },
    });
    const starting = engine.start();
    await Promise.resolve();
    await Promise.resolve();
    engine.stop();
    expect(track.stop).toHaveBeenCalledTimes(1);
    const pipeline = fakePipeline();
    building.resolve(pipeline);
    await starting;
    expect(pipeline.close).toHaveBeenCalledTimes(1);
    expect(pipeline.setMuted).not.toHaveBeenCalled();
  });

  it('stops the tracks and the chain on an ordinary hang-up, and is safe to stop twice', async () => {
    const { stream, track } = fakeStream();
    const pipeline = fakePipeline();
    const engine = createAudioEngine({
      send: () => {},
      failed: () => {},
      deps: {
        getUserMedia: async () => stream,
        createPipeline: async () => pipeline,
      },
    });
    engine.setMuted(true);
    await engine.start();
    expect(pipeline.setMuted).toHaveBeenLastCalledWith(true);
    engine.receive(960, new Uint8Array([1]));
    expect(pipeline.receive).toHaveBeenCalledTimes(1);
    engine.stop();
    engine.stop();
    expect(track.stop).toHaveBeenCalledTimes(1);
    expect(pipeline.close).toHaveBeenCalledTimes(1);
    engine.receive(1920, new Uint8Array([1]));
    expect(pipeline.receive).toHaveBeenCalledTimes(1);
  });

  it('reports why the microphone could not be used and leaves nothing open', async () => {
    for (const [name, reason] of [
      ['NotAllowedError', 'permission'],
      ['NotFoundError', 'no-microphone'],
      ['AbortError', 'failed'],
    ]) {
      const failed = vi.fn();
      const createPipeline = vi.fn(async () => fakePipeline());
      const engine = createAudioEngine({
        send: () => {},
        failed,
        deps: {
          getUserMedia: async () => {
            throw Object.assign(new Error('synthetic'), { name });
          },
          createPipeline,
        },
      });
      await engine.start();
      expect(failed).toHaveBeenCalledWith(reason);
      expect(createPipeline).not.toHaveBeenCalled();
      expect(engine.isRunning()).toBe(false);
    }
    const { stream, track } = fakeStream();
    const failed = vi.fn();
    const engine = createAudioEngine({
      send: () => {},
      failed,
      deps: {
        getUserMedia: async () => stream,
        createPipeline: async () => {
          throw new Error('no codec');
        },
      },
    });
    await engine.start();
    expect(failed).toHaveBeenCalledWith('unsupported');
    expect(track.stop).toHaveBeenCalledTimes(1);
  });

  it('releases the microphone when the running chain fails', async () => {
    const { stream, track } = fakeStream();
    const pipeline = fakePipeline();
    let breakChain: (reason: 'failed') => void = () => {};
    const failed = vi.fn();
    const engine = createAudioEngine({
      send: () => {},
      failed,
      deps: {
        getUserMedia: async () => stream,
        createPipeline: async (_stream, _send, onFailure) => {
          breakChain = onFailure;
          return pipeline;
        },
      },
    });
    await engine.start();
    breakChain('failed');
    expect(track.stop).toHaveBeenCalledTimes(1);
    expect(pipeline.close).toHaveBeenCalledTimes(1);
    expect(failed).toHaveBeenCalledWith('failed');
  });
});

describe('call media stream frames', () => {
  const callID = 'CQkJCQkJCQkJCQkJCQkJCQ';
  it('names the call in the first frame and refuses a bad handle', () => {
    const hello = encodeHello(callID);
    expect(hello).toHaveLength(23);
    expect(hello[0]).toBe(0);
    expect(String.fromCharCode(...hello.subarray(1))).toBe(callID);
    for (const bad of ['', 'short', 'A'.repeat(22), callID + 'x'])
      expect(() => encodeHello(bad)).toThrow();
    expect(validCallID(callID)).toBe(true);
  });

  it('frames outgoing audio with a wrapped 32-bit timestamp', () => {
    expect([...encodeAudio(960, new Uint8Array([7, 8]))]).toEqual([
      1, 0, 0, 3, 192, 7, 8,
    ]);
    expect([...encodeAudio(2 ** 32 + 5, new Uint8Array([1]))]).toEqual([
      1, 0, 0, 0, 5, 1,
    ]);
    expect(() => encodeAudio(0, new Uint8Array(0))).toThrow();
    expect(() => encodeAudio(0, new Uint8Array(maxAudioFrame + 1))).toThrow();
  });

  it('accepts only well-formed incoming audio', () => {
    const frame = new Uint8Array([1, 1, 0, 0, 3, 192, 5, 6]);
    expect(decodeAudio(frame)).toEqual({
      recovered: true,
      timestamp: 960,
      data: new Uint8Array([5, 6]),
    });
    expect(decodeAudio(frame.buffer)?.recovered).toBe(true);
    for (const bad of [
      new Uint8Array([]),
      new Uint8Array([1, 0, 0, 0, 0, 0]),
      new Uint8Array([2, 0, 0, 0, 0, 0, 1]),
      new Uint8Array([1, 4, 0, 0, 0, 0, 1]),
      new Uint8Array(7 + maxAudioFrame).fill(1),
    ])
      expect(decodeAudio(bad)).toBeNull();
  });
});
