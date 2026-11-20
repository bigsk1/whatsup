import { describe, expect, it, vi } from 'vitest';
import { decodeMedia, encodeVideo, maxVideoFrame } from './protocol';
import {
  createVideoEngine,
  vp8KeyframeSize,
  VideoSetupError,
  type VideoReceiver,
  type VideoSender,
} from './video';

function fakeStream() {
  const track = { stop: vi.fn() };
  return {
    track,
    stream: { getTracks: () => [track] } as unknown as MediaStream,
  };
}

function fakeSender() {
  const counts = { captured: 3, sent: 2, keyframesSent: 1 };
  const sender = {
    keyframe: vi.fn(),
    close: vi.fn(),
    counts: () => ({ ...counts }),
  };
  return sender as VideoSender & typeof sender;
}

function fakeReceiver() {
  const receiver = {
    receive: vi.fn(),
    setCanvas: vi.fn(),
    clear: vi.fn(),
    close: vi.fn(),
    counts: () => ({
      received: 5,
      keyframesReceived: 1,
      decoded: 4,
      shown: 4,
      waitingForKeyframe: false,
    }),
  };
  return receiver as VideoReceiver & typeof receiver;
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

const named = (name: string) => Object.assign(new Error('synthetic'), { name });

describe('call video engine', () => {
  it('tries the 720p request again when it is turned down for a moment, and does not settle for less', async () => {
    const { stream } = fakeStream();
    const requests: MediaStreamConstraints[] = [];
    const waits: number[] = [];
    const engine = createVideoEngine({
      send: () => {},
      failed: () => {},
      deps: {
        wait: async (ms) => {
          waits.push(ms);
        },
        getUserMedia: async (constraints) => {
          requests.push(constraints);
          // Turned down twice, as an engine does just after the microphone
          // has opened, then granted.
          if (requests.length < 3) throw named('OverconstrainedError');
          return stream;
        },
        createSender: async () => fakeSender(),
        createReceiver: () => fakeReceiver(),
      },
    });
    await engine.startCamera();
    expect(engine.camera()).toBe('on');
    expect(requests).toHaveLength(3);
    for (const request of requests)
      expect(request.video).toMatchObject({ width: { exact: 1280 } });
    expect(waits).toEqual([250, 250]);
  });

  it('stops waiting on the 720p request as soon as the camera is switched off', async () => {
    const { stream, track } = fakeStream();
    const pause = deferred<void>();
    const getUserMedia = vi.fn(async () => {
      if (getUserMedia.mock.calls.length === 1)
        throw named('OverconstrainedError');
      return stream;
    });
    const engine = createVideoEngine({
      send: () => {},
      failed: () => {},
      deps: {
        wait: () => pause.promise,
        getUserMedia,
        createSender: async () => fakeSender(),
        createReceiver: () => fakeReceiver(),
      },
    });
    const starting = engine.startCamera();
    await vi.waitFor(() => expect(getUserMedia).toHaveBeenCalledTimes(1));
    engine.stopCamera();
    pause.resolve();
    await starting;
    expect(getUserMedia).toHaveBeenCalledTimes(1);
    expect(engine.camera()).toBe('off');
    expect(track.stop).not.toHaveBeenCalled();
  });

  it('stops the camera before anything lets go of its picture', async () => {
    const { stream, track } = fakeStream();
    const sender = fakeSender();
    const engine = createVideoEngine({
      send: () => {},
      failed: () => {},
      deps: {
        getUserMedia: async () => stream,
        createSender: async () => sender,
        createReceiver: () => fakeReceiver(),
      },
    });
    await engine.startCamera();
    engine.stopCamera();
    expect(track.stop).toHaveBeenCalledTimes(1);
    expect(sender.close).toHaveBeenCalledTimes(1);
    // An element that detaches a running camera can lock the window up.
    expect(track.stop.mock.invocationCallOrder[0]).toBeLessThan(
      sender.close.mock.invocationCallOrder[0],
    );
  });

  it('asks for video only, 720p first, and falls back to a modest request when the camera cannot do that', async () => {
    const { stream } = fakeStream();
    const requests: MediaStreamConstraints[] = [];
    const getUserMedia = vi.fn(async (constraints: MediaStreamConstraints) => {
      requests.push(constraints);
      const video = constraints.video as MediaTrackConstraints;
      if (typeof video.width === 'object' && 'exact' in video.width)
        throw named('OverconstrainedError');
      return stream;
    });
    const changed = vi.fn();
    const engine = createVideoEngine({
      send: () => {},
      failed: () => {},
      changed,
      deps: {
        wait: async () => {},
        getUserMedia,
        createSender: async () => fakeSender(),
        createReceiver: () => fakeReceiver(),
      },
    });
    expect(engine.camera()).toBe('off');
    const starting = engine.startCamera();
    expect(engine.camera()).toBe('starting');
    await starting;
    expect(engine.camera()).toBe('on');
    expect(engine.localStream()).toBe(stream);
    // Four tries of the 720p request, then the modest one.
    expect(requests).toHaveLength(5);
    for (const request of requests) expect(request.audio).toBe(false);
    // The rate matters as much as the size: without it a Linux engine picks
    // the camera's slow raw 720p mode, which is granted and stays black.
    for (const request of requests.slice(0, 4))
      expect(request.video).toEqual({
        width: { exact: 1280 },
        height: { exact: 720 },
        frameRate: { ideal: 30 },
      });
    expect(requests[4].video).toMatchObject({
      width: { ideal: 640 },
      frameRate: { ideal: 30 },
    });
    expect(changed).toHaveBeenCalled();
  });

  it('releases the camera when stopped while the permission request is still pending', async () => {
    const pending = deferred<MediaStream>();
    const createSender = vi.fn(async () => fakeSender());
    const failed = vi.fn();
    const engine = createVideoEngine({
      send: () => {},
      failed,
      deps: {
        getUserMedia: () => pending.promise,
        createSender,
        createReceiver: () => fakeReceiver(),
      },
    });
    const starting = engine.startCamera();
    engine.stopCamera(); // hang up, camera off, calls off or lock
    const { stream, track } = fakeStream();
    pending.resolve(stream);
    await starting;
    expect(track.stop).toHaveBeenCalledTimes(1);
    expect(createSender).not.toHaveBeenCalled();
    expect(failed).not.toHaveBeenCalled();
    expect(engine.camera()).toBe('off');
    expect(engine.localStream()).toBeNull();
  });

  it('releases everything when stopped while the encoder is still being built', async () => {
    const building = deferred<VideoSender>();
    const { stream, track } = fakeStream();
    const engine = createVideoEngine({
      send: () => {},
      failed: () => {},
      deps: {
        getUserMedia: async () => stream,
        createSender: () => building.promise,
        createReceiver: () => fakeReceiver(),
      },
    });
    const starting = engine.startCamera();
    await Promise.resolve();
    await Promise.resolve();
    engine.stop();
    expect(track.stop).toHaveBeenCalledTimes(1);
    const sender = fakeSender();
    building.resolve(sender);
    await starting;
    expect(sender.close).toHaveBeenCalledTimes(1);
    expect(engine.camera()).toBe('off');
  });

  it('says why the camera could not be used and leaves nothing open', async () => {
    for (const [error, reason] of [
      [named('NotAllowedError'), 'permission'],
      [named('NotFoundError'), 'no-camera'],
      [named('TypeError'), 'failed'],
      // A desktop that would not ask about the camera: every request is
      // reported as one no camera could satisfy.
      [named('OverconstrainedError'), 'unavailable'],
    ] as const) {
      const failed = vi.fn();
      const createSender = vi.fn(async () => fakeSender());
      const engine = createVideoEngine({
        send: () => {},
        failed,
        deps: {
          getUserMedia: async () => {
            throw error;
          },
          createSender,
          createReceiver: () => fakeReceiver(),
        },
      });
      await engine.startCamera();
      expect(failed).toHaveBeenCalledWith(reason);
      expect(createSender).not.toHaveBeenCalled();
      expect(engine.camera()).toBe('off');
    }
    // Granted, but the camera never delivers a picture, or cannot be encoded.
    for (const [thrown, reason] of [
      [new VideoSetupError('no-picture'), 'no-picture'],
      // A first picture and then nothing new: never sent on as if live.
      [new VideoSetupError('stuck'), 'stuck'],
      [new VideoSetupError('unsupported'), 'unsupported'],
      [new Error('anything else'), 'unsupported'],
    ] as const) {
      const { stream, track } = fakeStream();
      const failed = vi.fn();
      const engine = createVideoEngine({
        send: () => {},
        failed,
        deps: {
          getUserMedia: async () => stream,
          createSender: async () => {
            throw thrown;
          },
          createReceiver: () => fakeReceiver(),
        },
      });
      await engine.startCamera();
      expect(failed).toHaveBeenCalledWith(reason);
      expect(track.stop).toHaveBeenCalledTimes(1);
      expect(engine.camera()).toBe('off');
    }
  });

  it('releases the camera when the running encoder fails', async () => {
    const { stream, track } = fakeStream();
    const sender = fakeSender();
    let breakEncoder: (reason: 'failed') => void = () => {};
    const failed = vi.fn();
    const engine = createVideoEngine({
      send: () => {},
      failed,
      deps: {
        getUserMedia: async () => stream,
        createSender: async (_stream, _send, onFailure) => {
          breakEncoder = onFailure;
          return sender;
        },
        createReceiver: () => fakeReceiver(),
      },
    });
    await engine.startCamera();
    breakEncoder('failed');
    expect(track.stop).toHaveBeenCalledTimes(1);
    expect(sender.close).toHaveBeenCalledTimes(1);
    expect(failed).toHaveBeenCalledWith('failed');
    expect(engine.camera()).toBe('off');
  });

  it('decodes incoming video whether or not this camera is on, asks the encoder for keyframes, and reports the first picture once', async () => {
    const { stream } = fakeStream();
    const sender = fakeSender();
    const receiver = fakeReceiver();
    let painted: () => void = () => {};
    const changed = vi.fn();
    const engine = createVideoEngine({
      send: () => {},
      failed: () => {},
      changed,
      deps: {
        getUserMedia: async () => stream,
        createSender: async () => sender,
        createReceiver: (shown) => {
          painted = shown;
          return receiver;
        },
      },
    });
    // Camera off: incoming video is still decoded, a keyframe request is a no-op.
    engine.receive(9000, true, new Uint8Array([1]));
    expect(receiver.receive).toHaveBeenCalledTimes(1);
    engine.keyframe();
    expect(sender.keyframe).not.toHaveBeenCalled();
    expect(engine.hasPicture()).toBe(false);
    painted();
    painted();
    expect(engine.hasPicture()).toBe(true);
    expect(changed).toHaveBeenCalledTimes(1);
    // Their camera goes off: the picture is blanked and no longer counts,
    // until a fresh one is painted.
    engine.forgetPicture();
    expect(receiver.clear).toHaveBeenCalledTimes(1);
    expect(engine.hasPicture()).toBe(false);
    expect(changed).toHaveBeenCalledTimes(2);
    painted();
    expect(engine.hasPicture()).toBe(true);
    expect(changed).toHaveBeenCalledTimes(3);

    await engine.startCamera();
    engine.keyframe();
    expect(sender.keyframe).toHaveBeenCalledTimes(1);
    const canvas = {} as HTMLCanvasElement;
    engine.setCanvas(canvas);
    expect(receiver.setCanvas).toHaveBeenCalledWith(canvas);
    expect(engine.stats()).toMatchObject({
      camera: 'on',
      captured: 3,
      sent: 2,
      keyframesSent: 1,
      received: 5,
      shown: 4,
    });
    // Camera off keeps the last sent counts for the readout.
    engine.stopCamera();
    expect(engine.stats()).toMatchObject({ camera: 'off', sent: 2 });
    engine.stop();
    engine.stop();
    expect(receiver.close).toHaveBeenCalledTimes(1);
    engine.receive(1, true, new Uint8Array([1]));
    expect(receiver.receive).toHaveBeenCalledTimes(1);
    await engine.startCamera();
    expect(engine.camera()).toBe('off');
  });

  it('carries on without incoming video when the engine cannot decode it', () => {
    const engine = createVideoEngine({
      send: () => {},
      failed: () => {},
      deps: {
        getUserMedia: async () => fakeStream().stream,
        createSender: async () => fakeSender(),
        createReceiver: () => {
          throw new Error('no decoder');
        },
      },
    });
    expect(engine.canReceive()).toBe(false);
    engine.receive(1, true, new Uint8Array([1]));
    expect(engine.stats()).toMatchObject({ received: 0, shown: 0 });
  });
});

describe('VP8 keyframe size', () => {
  it('reads the picture size a keyframe declares and refuses anything that is not a keyframe', () => {
    // The first ten bytes of a real 640x360 VP8 keyframe.
    const real = new Uint8Array([16, 56, 0, 157, 1, 42, 128, 2, 104, 1]);
    expect(vp8KeyframeSize(real)).toEqual({ width: 640, height: 360 });
    // The top two bits of each 16-bit field are scaling hints, not size.
    expect(
      vp8KeyframeSize(
        new Uint8Array([16, 56, 0, 157, 1, 42, 128, 0xc2, 104, 0x81]),
      ),
    ).toEqual({ width: 640, height: 360 });
    for (const bad of [
      real.subarray(0, 9), // too short
      new Uint8Array([17, 56, 0, 157, 1, 42, 128, 2, 104, 1]), // a delta frame
      new Uint8Array([16, 56, 0, 157, 1, 43, 128, 2, 104, 1]), // wrong start code
      new Uint8Array([16, 56, 0, 157, 1, 42, 0, 0, 104, 1]), // zero width
      new Uint8Array([16, 56, 0, 157, 1, 42, 128, 2, 0, 0]), // zero height
      new Uint8Array([16, 56, 0, 157, 1, 42, 0xff, 0x3f, 104, 1]), // absurd width
    ])
      expect(vp8KeyframeSize(bad)).toBeNull();
  });
});

describe('call media stream video frames', () => {
  it('frames an outgoing picture with its keyframe flag and a wrapped 32-bit timestamp', () => {
    expect([...encodeVideo(90000, true, new Uint8Array([7, 8]))]).toEqual([
      2, 1, 0, 1, 95, 144, 7, 8,
    ]);
    expect([...encodeVideo(2 ** 32 + 5, false, new Uint8Array([1]))]).toEqual([
      2, 0, 0, 0, 0, 5, 1,
    ]);
    expect(() => encodeVideo(0, true, new Uint8Array(0))).toThrow();
    expect(() =>
      encodeVideo(0, true, new Uint8Array(maxVideoFrame + 1)),
    ).toThrow();
    expect(encodeVideo(0, true, new Uint8Array(maxVideoFrame))).toHaveLength(
      6 + maxVideoFrame,
    );
  });

  it('tells audio, video and the keyframe signal apart and refuses anything malformed', () => {
    expect(decodeMedia(new Uint8Array([2, 1, 0, 1, 95, 144, 9, 9]))).toEqual({
      kind: 'video',
      key: true,
      timestamp: 90000,
      data: new Uint8Array([9, 9]),
    });
    expect(decodeMedia(new Uint8Array([2, 0, 0, 0, 0, 1, 4]))).toMatchObject({
      kind: 'video',
      key: false,
    });
    expect(decodeMedia(new Uint8Array([3]))).toEqual({ kind: 'keyframe' });
    expect(decodeMedia(new Uint8Array([1, 1, 0, 0, 3, 192, 5]))).toEqual({
      kind: 'audio',
      recovered: true,
      timestamp: 960,
      data: new Uint8Array([5]),
    });
    expect(decodeMedia(new Uint8Array([3]).buffer)).toEqual({
      kind: 'keyframe',
    });
    for (const bad of [
      new Uint8Array([]),
      new Uint8Array([0]),
      new Uint8Array([3, 0]),
      new Uint8Array([2, 0, 0, 0, 0, 1]),
      new Uint8Array([2, 2, 0, 0, 0, 1, 4]),
      new Uint8Array([4, 0, 0, 0, 0, 1, 4]),
      new Uint8Array(7 + maxVideoFrame).fill(2, 0, 1),
    ])
      expect(decodeMedia(bad)).toBeNull();
  });
});
