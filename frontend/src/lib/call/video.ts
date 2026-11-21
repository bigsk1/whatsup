import { maxVideoFrame } from './protocol';

export type VideoFailure =
  | 'permission'
  /** Every way of asking for the camera was turned down without a reason. */
  | 'unavailable'
  | 'no-camera'
  | 'unsupported'
  | 'no-picture'
  /** A first picture arrived and then nothing new did. */
  | 'stuck'
  | 'failed';

export type CameraState = 'off' | 'starting' | 'on';

/**
 * Counters for the call details readout. Numbers only: no picture, no device
 * name, nothing leaves the page.
 */
export interface VideoStats {
  camera: CameraState;
  /** Pictures taken from the camera and handed to the encoder. */
  captured: number;
  /** Encoded pictures handed to the desktop for sending. */
  sent: number;
  keyframesSent: number;
  /** Encoded pictures that arrived from the other person. */
  received: number;
  keyframesReceived: number;
  decoded: number;
  /** Pictures actually painted on screen. */
  shown: number;
  /** True while incoming pictures are being skipped until a keyframe comes. */
  waitingForKeyframe: boolean;
}

/** This device's camera, encoded. */
export interface VideoSender {
  /** Make the next encoded picture a keyframe. */
  keyframe(): void;
  close(): void;
  counts(): { captured: number; sent: number; keyframesSent: number };
}

/** The other person's video, decoded and painted. */
export interface VideoReceiver {
  receive(timestamp: number, key: boolean, data: Uint8Array): void;
  setCanvas(canvas: HTMLCanvasElement | null): void;
  /** Blank the canvas and wait for a keyframe before painting again. */
  clear(): void;
  close(): void;
  counts(): {
    received: number;
    keyframesReceived: number;
    decoded: number;
    shown: number;
    waitingForKeyframe: boolean;
  };
}

export interface VideoDeps {
  /** A pause between two tries of the same camera request. */
  wait(ms: number): Promise<void>;
  getUserMedia(constraints: MediaStreamConstraints): Promise<MediaStream>;
  createSender(
    stream: MediaStream,
    send: (timestamp: number, key: boolean, data: Uint8Array) => void,
    failed: (reason: VideoFailure) => void,
  ): Promise<VideoSender>;
  createReceiver(shown: () => void): VideoReceiver | null;
}

/** A failure while building the sender that already knows its reason. */
export class VideoSetupError extends Error {
  constructor(readonly reason: VideoFailure) {
    super(reason);
  }
}

function release(stream: MediaStream | null) {
  stream?.getTracks().forEach((track) => track.stop());
}

// The first request asks for exactly 1280x720 at 30 pictures a second. On
// some Linux engines only a camera's compressed modes deliver pictures; its
// raw modes are granted and then stay black. A webcam typically offers 720p
// raw at a low rate and compressed at 30, so the size alone is not enough:
// without the rate the engine settles on the raw mode. Both parts were found
// on real hardware. A camera that cannot do 720p gets an ordinary modest
// request.
const cameraRequests: MediaTrackConstraints[] = [
  { width: { exact: 1280 }, height: { exact: 720 }, frameRate: { ideal: 30 } },
  { width: { ideal: 640 }, height: { ideal: 360 }, frameRate: { ideal: 30 } },
];

// An engine can turn the first request down for a moment when the camera is
// asked for just after the microphone has opened, and grant the very same
// request shortly after. Going straight on to the modest request there lands
// in a mode that stays black, so the first request is tried a few times.
const firstRequestTries = 4;
const firstRequestGapMs = 250;
const turnedDownForNow = (error: unknown) => {
  const name = (error as { name?: string } | null)?.name ?? '';
  return (
    name === 'OverconstrainedError' || name === 'ConstraintNotSatisfiedError'
  );
};

const retryWithNextRequest = (error: unknown) => {
  const name = (error as { name?: string } | null)?.name ?? '';
  return (
    name === 'OverconstrainedError' ||
    name === 'ConstraintNotSatisfiedError' ||
    name === 'NotReadableError' ||
    name === 'AbortError'
  );
};

function describeCameraFailure(error: unknown): VideoFailure {
  const name = (error as { name?: string } | null)?.name ?? '';
  if (name === 'NotAllowedError' || name === 'SecurityError')
    return 'permission';
  if (name === 'NotFoundError') return 'no-camera';
  // Some desktops refuse a camera request they could not ask the person
  // about, for example because the window was not in front, and the engine
  // reports that as a request no camera could satisfy.
  if (retryWithNextRequest(error)) return 'unavailable';
  return 'failed';
}

/**
 * The video side of a call: this device's camera going out and the other
 * person's picture coming in. The two halves are independent. Incoming video
 * is decoded whenever it arrives; the camera runs only between startCamera()
 * and stopCamera(), and stopCamera() always releases it, including when it is
 * called while the permission request is still pending.
 */
export function createVideoEngine(options: {
  send(timestamp: number, key: boolean, data: Uint8Array): void;
  /** The camera could not start or stopped working. It is already released. */
  failed(reason: VideoFailure): void;
  /** Camera state or first-picture state changed. */
  changed?(): void;
  deps?: Partial<VideoDeps>;
}) {
  const deps: VideoDeps = {
    wait: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
    getUserMedia: (constraints) =>
      navigator.mediaDevices.getUserMedia(constraints),
    createSender: createBrowserSender,
    createReceiver: createBrowserReceiver,
    ...options.deps,
  };
  let generation = 0;
  let camera: CameraState = 'off';
  let stream: MediaStream | null = null;
  let sender: VideoSender | null = null;
  let closed = false;
  let pictureShown = false;
  let lastSent = { captured: 0, sent: 0, keyframesSent: 0 };

  let receiver: VideoReceiver | null = null;
  try {
    receiver = deps.createReceiver(() => {
      if (pictureShown) return;
      pictureShown = true;
      options.changed?.();
    });
  } catch {
    receiver = null;
  }

  const setCamera = (next: CameraState) => {
    if (camera === next) return;
    camera = next;
    options.changed?.();
  };

  function stopCamera() {
    generation += 1;
    if (sender) lastSent = sender.counts();
    // The camera stops first. Only then does anything let go of its picture:
    // an element that detaches a camera which is still running can lock the
    // whole window up on one engine.
    release(stream);
    sender?.close();
    sender = null;
    stream = null;
    setCamera('off');
  }

  async function open(epoch: number): Promise<MediaStream | null> {
    let last: unknown = null;
    for (const [index, video] of cameraRequests.entries()) {
      const tries = index === 0 ? firstRequestTries : 1;
      for (let attempt = 1; attempt <= tries; attempt += 1) {
        try {
          const captured = await deps.getUserMedia({ audio: false, video });
          if (epoch !== generation) {
            // Hang-up, camera off, calls off or lock while the request was
            // pending: release at once, never reach an encoder.
            release(captured);
            return null;
          }
          return captured;
        } catch (error) {
          if (epoch !== generation) return null;
          last = error;
          if (!retryWithNextRequest(error)) throw last;
          if (attempt === tries || !turnedDownForNow(error)) break;
          await deps.wait(firstRequestGapMs);
          if (epoch !== generation) return null;
        }
      }
    }
    throw last;
  }

  async function startCamera() {
    if (closed || camera !== 'off') return;
    const epoch = ++generation;
    setCamera('starting');
    let captured: MediaStream | null;
    try {
      captured = await open(epoch);
    } catch (error) {
      if (epoch !== generation) return;
      setCamera('off');
      options.failed(describeCameraFailure(error));
      return;
    }
    if (!captured) return;
    stream = captured;
    let built: VideoSender;
    try {
      built = await deps.createSender(captured, options.send, (reason) => {
        if (epoch !== generation) return;
        stopCamera();
        options.failed(reason);
      });
    } catch (error) {
      if (epoch !== generation) return;
      stopCamera();
      options.failed(
        error instanceof VideoSetupError ? error.reason : 'unsupported',
      );
      return;
    }
    if (epoch !== generation) {
      release(captured);
      built.close();
      return;
    }
    sender = built;
    setCamera('on');
  }

  return {
    startCamera,
    stopCamera,
    /** End of the call: release the camera and the decoder. */
    stop() {
      if (closed) return;
      closed = true;
      stopCamera();
      receiver?.close();
      receiver = null;
    },
    /** Go asks for a fresh keyframe: the flow is ready, or the peer asked. */
    keyframe() {
      sender?.keyframe();
    },
    receive(timestamp: number, key: boolean, data: Uint8Array) {
      if (!closed) receiver?.receive(timestamp, key, data);
    },
    /** Where the other person's picture is painted; null detaches. */
    setCanvas(canvas: HTMLCanvasElement | null) {
      receiver?.setCanvas(canvas);
    },
    /** The live camera stream for the local preview, or null. */
    localStream: () => (camera === 'on' ? stream : null),
    camera: () => camera,
    /** A picture from the other person is on screen. */
    hasPicture: () => pictureShown,
    /**
     * Their camera went off: what is on screen is no longer them, now. Blank
     * it, and count nothing as shown until a fresh picture is painted.
     */
    forgetPicture() {
      receiver?.clear();
      if (!pictureShown) return;
      pictureShown = false;
      options.changed?.();
    },
    canReceive: () => receiver !== null,
    stats(): VideoStats {
      const out = sender ? sender.counts() : lastSent;
      const incoming = receiver?.counts() ?? {
        received: 0,
        keyframesReceived: 0,
        decoded: 0,
        shown: 0,
        waitingForKeyframe: false,
      };
      return { camera, ...out, ...incoming };
    },
  };
}

export type VideoEngine = ReturnType<typeof createVideoEngine>;

// ---------------------------------------------------------------------------
// Browser implementation

const pictureWidth = 640;
const pictureHeight = 360;
const picturesPerSecond = 20;
const keyframeEveryMs = 4000;
const encoderConfig: VideoEncoderConfig = {
  codec: 'vp8',
  width: pictureWidth,
  height: pictureHeight,
  bitrate: 500_000,
  framerate: picturesPerSecond,
  latencyMode: 'realtime',
};

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

// How long a camera gets to show that its picture keeps changing, and how
// many new pictures prove it.
const stuckAfterMs = 6000;
const freshPicturesNeeded = 3;

/**
 * Whether new pictures keep arriving after the first.
 *
 * One family of Linux system software (WebKitGTK before 2.54.3 together with
 * GStreamer 1.28 or newer) hands over a camera's first picture and then the
 * same one again for minutes: the longer the computer has been on, the
 * longer. Nothing here can repair that, but it must not be sent to the other
 * person as if it were live. The element itself is asked how many new
 * pictures it has shown, so a covered lens or a still room is not mistaken
 * for it.
 *
 * Where the question cannot be asked, or nobody can see the window (engines
 * may hold pictures back there), nothing is concluded and the camera carries
 * on.
 */
async function picturesKeepComing(
  source: HTMLVideoElement,
  stream: MediaStream,
): Promise<boolean> {
  const ask = (
    source as HTMLVideoElement & {
      requestVideoFrameCallback?: (callback: () => void) => number;
    }
  ).requestVideoFrameCallback;
  if (typeof ask !== 'function') return true;
  let fresh = 0;
  let watching = true;
  const next = () => {
    if (!watching) return;
    fresh += 1;
    ask.call(source, next);
  };
  ask.call(source, next);
  try {
    for (let waited = 0; waited < stuckAfterMs; waited += 50) {
      if (fresh >= freshPicturesNeeded) return true;
      if (document.visibilityState !== 'visible') return true;
      // Switched off meanwhile: whoever did that deals with the rest.
      if (stream.getVideoTracks().every((track) => track.readyState !== 'live'))
        return true;
      await sleep(50);
    }
    return fresh >= freshPicturesNeeded;
  } finally {
    watching = false;
  }
}

/**
 * Starts an element showing a camera.
 *
 * The element is left unmuted. A camera stream carries no sound, so nothing
 * is heard, but it matters to one engine: WebKitGTK from 2.54 suspends a muted
 * video it does not consider in view, and such an element never receives a
 * single picture. An element that is not muted is left alone. If an engine
 * will not start an unmuted element without a click first, it is muted and
 * started again; those engines do not suspend it.
 */
export async function showCamera(
  element: HTMLVideoElement,
  stream: MediaStream,
): Promise<void> {
  element.muted = false;
  element.srcObject = stream;
  try {
    await element.play();
  } catch (error) {
    if ((error as { name?: string } | null)?.name !== 'NotAllowedError')
      throw error;
    element.muted = true;
    await element.play();
  }
}

async function createBrowserSender(
  stream: MediaStream,
  send: (timestamp: number, key: boolean, data: Uint8Array) => void,
  failed: (reason: VideoFailure) => void,
): Promise<VideoSender> {
  if (typeof VideoEncoder === 'undefined' || typeof VideoFrame === 'undefined')
    throw new VideoSetupError('unsupported');
  const support = await VideoEncoder.isConfigSupported(encoderConfig).catch(
    () => ({ supported: false }),
  );
  if (!support.supported) throw new VideoSetupError('unsupported');

  // The camera plays into an element that stays in the document but out of
  // sight; pictures are taken from it at a steady rate.
  const source = document.createElement('video');
  source.playsInline = true;
  source.setAttribute('aria-hidden', 'true');
  source.style.cssText =
    'position:fixed;left:0;top:0;width:2px;height:2px;opacity:0;pointer-events:none';
  document.body.appendChild(source);
  let closed = false;
  let timer: ReturnType<typeof setInterval> | null = null;
  let encoder: VideoEncoder | null = null;
  const dispose = () => {
    closed = true;
    if (timer) clearInterval(timer);
    timer = null;
    try {
      if (encoder && encoder.state !== 'closed') encoder.close();
    } catch {
      /* already closed */
    }
    // Stop the camera before the element lets go of it, whoever else stops
    // it too. Detaching a camera that is still running can lock the whole
    // window up on one engine; with the camera stopped first it never has.
    release(stream);
    source.srcObject = null;
    source.remove();
  };

  try {
    await Promise.race([showCamera(source, stream), sleep(6000)]);
    // Granted but no picture is a real case: wait for one before trusting it.
    for (let waited = 0; !source.videoWidth && waited < 5000; waited += 50)
      await sleep(50);
    if (!source.videoWidth) throw new VideoSetupError('no-picture');
    if (!(await picturesKeepComing(source, stream)))
      throw new VideoSetupError('stuck');
  } catch (error) {
    dispose();
    throw error instanceof VideoSetupError
      ? error
      : new VideoSetupError('failed');
  }

  const canvas = document.createElement('canvas');
  canvas.width = pictureWidth;
  canvas.height = pictureHeight;
  const context = canvas.getContext('2d', { alpha: false });
  const counts = { captured: 0, sent: 0, keyframesSent: 0 };
  let wantKey = true;
  let lastKey = 0;
  let viaCanvas = context !== null;

  try {
    encoder = new VideoEncoder({
      output: (chunk) => {
        if (
          closed ||
          chunk.byteLength === 0 ||
          chunk.byteLength > maxVideoFrame
        )
          return;
        const data = new Uint8Array(chunk.byteLength);
        chunk.copyTo(data);
        const key = chunk.type === 'key';
        counts.sent += 1;
        if (key) counts.keyframesSent += 1;
        // Microseconds to the 90 kHz video clock, wrapped to 32 bits.
        send(Math.round((chunk.timestamp * 9) / 100) >>> 0, key, data);
      },
      error: () => {
        if (!closed) failed('failed');
      },
    });
    encoder.configure(encoderConfig);
  } catch {
    // The camera is already playing into an element in the document. An
    // encoder that will not start must not leave either behind.
    dispose();
    throw new VideoSetupError('unsupported');
  }

  const take = (): VideoFrame | null => {
    const timestamp = Math.round(performance.now() * 1000);
    if (viaCanvas && context) {
      // Fit the whole camera picture inside 16:9, with bars if it is 4:3.
      const scale = Math.min(
        pictureWidth / source.videoWidth,
        pictureHeight / source.videoHeight,
      );
      const width = source.videoWidth * scale;
      const height = source.videoHeight * scale;
      context.fillStyle = '#000';
      context.fillRect(0, 0, pictureWidth, pictureHeight);
      context.drawImage(
        source,
        (pictureWidth - width) / 2,
        (pictureHeight - height) / 2,
        width,
        height,
      );
      try {
        return new VideoFrame(canvas, { timestamp });
      } catch {
        // Some engines cannot build a frame from a canvas. Take it from the
        // element instead and let the encoder scale.
        viaCanvas = false;
      }
    }
    try {
      return new VideoFrame(source, { timestamp });
    } catch {
      return null;
    }
  };

  timer = setInterval(() => {
    if (closed || !encoder || encoder.state !== 'configured') return;
    if (!source.videoWidth || encoder.encodeQueueSize > 2) return;
    const frame = take();
    if (!frame) return;
    const now = performance.now();
    const key = wantKey || now - lastKey > keyframeEveryMs;
    try {
      encoder.encode(frame, { keyFrame: key });
      counts.captured += 1;
      if (key) {
        wantKey = false;
        lastKey = now;
      }
    } catch {
      if (!closed) failed('failed');
    } finally {
      frame.close();
    }
  }, 1000 / picturesPerSecond);

  return {
    keyframe() {
      wantKey = true;
    },
    close() {
      if (!closed) dispose();
    },
    counts: () => ({ ...counts }),
  };
}

/**
 * The picture size a VP8 keyframe declares, or null if these bytes are not a
 * VP8 keyframe. A keyframe is a 3-byte frame tag, the start code 9d 01 2a, then
 * 14-bit width and height. The decoder is configured from this: some engines
 * will not decode VP8 without being told the size first.
 */
export function vp8KeyframeSize(
  data: Uint8Array,
): { width: number; height: number } | null {
  if (
    data.length < 10 ||
    (data[0] & 1) !== 0 ||
    data[3] !== 0x9d ||
    data[4] !== 0x01 ||
    data[5] !== 0x2a
  )
    return null;
  const width = (data[6] | (data[7] << 8)) & 0x3fff;
  const height = (data[8] | (data[9] << 8)) & 0x3fff;
  return width > 0 && height > 0 && width <= 1920 && height <= 1920
    ? { width, height }
    : null;
}

// The most pictures left waiting at the decoder. Beyond this the picture on
// screen is too far behind to be worth showing, and whatever is waiting is
// thrown away instead of being allowed to pile up.
const maxVideoDecodeBacklog = 8;
// How many decoders may be started within a stretch of time. Recovery from a
// fault or a backlog starts a new one, and the engine can still be setting up
// or tearing down the last while that happens. A few in a row are normal: a
// camera switched off and on, a change of picture size. A steady stream of
// them is not, so beyond this the receiver waits, without a decoder, for a
// keyframe that arrives once the stretch has passed.
const maxDecoderStarts = 3;
const decoderStartWindowMs = 3000;

function createBrowserReceiver(shown: () => void): VideoReceiver | null {
  if (typeof VideoDecoder === 'undefined') return null;
  const counts = { received: 0, keyframesReceived: 0, decoded: 0, shown: 0 };
  let closed = false;
  let canvas: HTMLCanvasElement | null = null;
  let context: CanvasRenderingContext2D | null = null;
  let decoder: VideoDecoder | null = null;
  let size = '';
  // Nothing can be decoded until a keyframe arrives; after a decoder fault or
  // a backlog, wait for the next one.
  let waiting = true;
  // When recent decoders were started, oldest first.
  const starts: number[] = [];
  const mayStartDecoder = () => {
    const now = performance.now();
    while (starts.length && now - starts[0] >= decoderStartWindowMs)
      starts.shift();
    if (starts.length >= maxDecoderStarts) return false;
    starts.push(now);
    return true;
  };

  const paint = (frame: VideoFrame) => {
    counts.decoded += 1;
    try {
      if (canvas && context) {
        if (canvas.width !== frame.displayWidth)
          canvas.width = frame.displayWidth;
        if (canvas.height !== frame.displayHeight)
          canvas.height = frame.displayHeight;
        context.drawImage(frame, 0, 0);
        counts.shown += 1;
        shown();
      }
    } catch {
      /* a frame that cannot be painted is skipped */
    } finally {
      frame.close();
    }
  };

  const discard = () => {
    try {
      if (decoder && decoder.state !== 'closed') decoder.close();
    } catch {
      /* already closed */
    }
    decoder = null;
    waiting = true;
  };

  const build = (width: number, height: number) => {
    const next = new VideoDecoder({
      output: (frame) => {
        if (closed || decoder !== next) frame.close();
        else paint(frame);
      },
      error: () => {
        if (decoder === next) discard();
      },
    });
    next.configure({ codec: 'vp8', codedWidth: width, codedHeight: height });
    decoder = next;
    size = width + 'x' + height;
  };

  return {
    receive(timestamp, key, data) {
      if (closed) return;
      counts.received += 1;
      if (key) counts.keyframesReceived += 1;
      if (waiting && !key) return;
      try {
        // The limit holds for every kind of frame. A backlog is dropped
        // whole; a keyframe then starts again from itself, and anything else
        // waits for the next keyframe.
        if (
          decoder &&
          decoder.state === 'configured' &&
          decoder.decodeQueueSize > maxVideoDecodeBacklog
        ) {
          discard();
          if (!key) return;
        }
        if (key) {
          // Every keyframe says how large its pictures are. Start a decoder
          // for that size if there is none, or the size has changed.
          const declared = vp8KeyframeSize(data);
          if (!declared) {
            discard();
            return;
          }
          if (
            !decoder ||
            decoder.state !== 'configured' ||
            size !== declared.width + 'x' + declared.height
          ) {
            discard();
            // Too many decoders lately: stay without one for now.
            if (!mayStartDecoder()) return;
            build(declared.width, declared.height);
          }
        } else if (!decoder || decoder.state !== 'configured') {
          discard();
          return;
        }
        decoder!.decode(
          new EncodedVideoChunk({
            type: key ? 'key' : 'delta',
            timestamp: Math.round((timestamp * 100) / 9),
            data,
          }),
        );
        waiting = false;
      } catch {
        discard();
      }
    },
    setCanvas(next) {
      canvas = next;
      context = next ? next.getContext('2d', { alpha: false }) : null;
    },
    clear() {
      // Pictures still in flight from before must not repaint it either.
      discard();
      if (canvas && context) {
        context.fillStyle = '#000';
        context.fillRect(0, 0, canvas.width, canvas.height);
      }
    },
    close() {
      closed = true;
      discard();
      canvas = null;
      context = null;
    },
    counts: () => ({ ...counts, waitingForKeyframe: waiting }),
  };
}
