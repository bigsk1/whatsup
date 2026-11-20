// no-inline: the worklets must be emitted as files. Small assets are otherwise
// packed into the bundle as data: URLs, which the application's Content
// Security Policy (script-src 'self') refuses to load.
import captureWorkletURL from './capture.worklet.js?url&no-inline';
import playbackWorkletURL from './playback.worklet.js?url&no-inline';
import { sharedAudioContext } from './context';
import { audioFrameSamples, maxAudioFrame } from './protocol';

export type AudioFailure =
  'permission' | 'no-microphone' | 'unsupported' | 'failed';

/**
 * The step of building the audio chain that failed. It is shown with the
 * "cannot run call audio" notice so a failure on a real device can be reported
 * without a debugger. It names a step only: no device, error text or data.
 */
export type AudioStage = 'output' | 'worklet' | 'codec' | 'graph';

class StageError extends Error {
  constructor(readonly stage: AudioStage) {
    super(stage);
  }
}

/**
 * Counters for the call details readout. They say how far audio got in each
 * direction so a silent call can be diagnosed from the screen. Numbers only:
 * no audio, no device names, nothing leaves the page.
 */
export interface AudioStats {
  /** 20 ms frames delivered by the microphone. */
  captured: number;
  /** Encoded frames handed to the desktop for sending. */
  sent: number;
  /** Encoded frames that arrived from the other person. */
  received: number;
  /** Frames the decoder turned back into sound. */
  decoded: number;
  /** Seconds of sound the output has actually played. */
  playedSeconds: number;
  /** Loudest recent microphone sample, 0 to 100. */
  micLevel: number;
  /** Loudest recent incoming sample, 0 to 100. */
  incomingLevel: number;
  /** Whether the audio output is running at all. */
  output: 'running' | 'stopped';
}

/** The running capture, encode, decode and playback chain for one call. */
export interface AudioPipeline {
  setMuted(muted: boolean): void;
  receive(timestamp: number, data: Uint8Array): void;
  close(): void;
  stats?(): AudioStats;
}

export interface AudioDeps {
  getUserMedia(constraints: MediaStreamConstraints): Promise<MediaStream>;
  createPipeline(
    stream: MediaStream,
    send: (timestamp: number, data: Uint8Array) => void,
    failed: (reason: AudioFailure) => void,
  ): Promise<AudioPipeline>;
}

function release(stream: MediaStream | null) {
  stream?.getTracks().forEach((track) => track.stop());
}

function describeCaptureFailure(error: unknown): AudioFailure {
  const name = (error as { name?: string } | null)?.name ?? '';
  if (name === 'NotAllowedError' || name === 'SecurityError')
    return 'permission';
  if (name === 'NotFoundError' || name === 'OverconstrainedError')
    return 'no-microphone';
  return 'failed';
}

/**
 * The microphone side of a call. start() is only ever called for an answered
 * call. stop() always releases the microphone, including when it is called
 * while the permission prompt is still open: the stream that arrives later is
 * released at once and never reaches an encoder.
 */
export function createAudioEngine(options: {
  send(timestamp: number, data: Uint8Array): void;
  failed(reason: AudioFailure, stage?: AudioStage): void;
  deps?: Partial<AudioDeps>;
}) {
  const deps: AudioDeps = {
    getUserMedia: (constraints) =>
      navigator.mediaDevices.getUserMedia(constraints),
    createPipeline: createBrowserPipeline,
    ...options.deps,
  };
  let generation = 0;
  let running = false;
  let muted = false;
  let stream: MediaStream | null = null;
  let pipeline: AudioPipeline | null = null;

  function stop() {
    generation += 1;
    running = false;
    pipeline?.close();
    pipeline = null;
    release(stream);
    stream = null;
  }

  async function start() {
    if (running) return;
    running = true;
    const epoch = ++generation;
    let captured: MediaStream;
    try {
      captured = await deps.getUserMedia({
        audio: {
          echoCancellation: true,
          noiseSuppression: true,
          autoGainControl: true,
          channelCount: 1,
        },
        video: false,
      });
    } catch (error) {
      if (epoch !== generation) return;
      running = false;
      options.failed(describeCaptureFailure(error));
      return;
    }
    if (epoch !== generation) {
      // The call ended, calls were switched off or the app locked while the
      // request was pending.
      release(captured);
      return;
    }
    stream = captured;
    let built: AudioPipeline;
    try {
      built = await deps.createPipeline(captured, options.send, (reason) => {
        if (epoch !== generation) return;
        stop();
        options.failed(reason);
      });
    } catch (error) {
      if (epoch !== generation) return;
      stop();
      options.failed(
        'unsupported',
        error instanceof StageError ? error.stage : undefined,
      );
      return;
    }
    if (epoch !== generation) {
      built.close();
      release(captured);
      return;
    }
    pipeline = built;
    pipeline.setMuted(muted);
  }

  return {
    start,
    stop,
    receive(timestamp: number, data: Uint8Array) {
      pipeline?.receive(timestamp, data);
    },
    setMuted(value: boolean) {
      muted = value;
      pipeline?.setMuted(value);
    },
    isRunning: () => running,
    stats: (): AudioStats | null => pipeline?.stats?.() ?? null,
  };
}

const worklets = new WeakSet<AudioContext>();

async function loadWorklets(context: AudioContext) {
  if (worklets.has(context)) return;
  await context.audioWorklet.addModule(captureWorkletURL);
  await context.audioWorklet.addModule(playbackWorkletURL);
  worklets.add(context);
}

const opusConstant: AudioEncoderConfig = {
  codec: 'opus',
  sampleRate: 48000,
  numberOfChannels: 1,
  bitrate: 32000,
  // Constant bitrate and no silence suppression: packet sizes and timing then
  // say nothing about when or how someone is speaking.
  bitrateMode: 'constant',
  opus: { frameDuration: 20000, usedtx: false, useinbandfec: false },
} as AudioEncoderConfig;
const opusPlain: AudioEncoderConfig = {
  codec: 'opus',
  sampleRate: 48000,
  numberOfChannels: 1,
  bitrate: 32000,
};
const opusDecode: AudioDecoderConfig = {
  codec: 'opus',
  sampleRate: 48000,
  numberOfChannels: 1,
};

async function createBrowserPipeline(
  stream: MediaStream,
  send: (timestamp: number, data: Uint8Array) => void,
  failed: (reason: AudioFailure) => void,
): Promise<AudioPipeline> {
  let stage: AudioStage = 'output';
  try {
    return await buildBrowserPipeline(stream, send, failed, (next) => {
      stage = next;
    });
  } catch {
    throw new StageError(stage);
  }
}

/**
 * Builds the pipeline, and if any step fails part-way, closes whatever the
 * earlier steps opened before the failure is reported. Without that a failed
 * setup would leave an encoder, a decoder or a worklet node open with nothing
 * holding them: the engine only gets something to close when setup succeeds.
 */
async function buildBrowserPipeline(
  stream: MediaStream,
  send: (timestamp: number, data: Uint8Array) => void,
  failed: (reason: AudioFailure) => void,
  entering: (stage: AudioStage) => void,
): Promise<AudioPipeline> {
  const opened: (() => void)[] = [];
  try {
    return await assembleBrowserPipeline(
      stream,
      send,
      failed,
      entering,
      opened,
    );
  } catch (error) {
    releaseAll(opened);
    throw error;
  }
}

/** Runs every recorded release, newest first, each one on its own. */
function releaseAll(opened: (() => void)[]) {
  for (const release of opened.splice(0).reverse()) {
    try {
      release();
    } catch {
      /* already released */
    }
  }
}

async function assembleBrowserPipeline(
  stream: MediaStream,
  send: (timestamp: number, data: Uint8Array) => void,
  failed: (reason: AudioFailure) => void,
  entering: (stage: AudioStage) => void,
  // What has been opened so far, as functions that release it. Each step adds
  // its own as soon as the thing exists.
  opened: (() => void)[],
): Promise<AudioPipeline> {
  const context = sharedAudioContext();
  if (context.state !== 'running') await context.resume();
  if (context.sampleRate !== 48000) throw new Error('unsupported sample rate');
  entering('worklet');
  await loadWorklets(context);

  let closed = false;
  // Released last: nothing reports a failure once the pipeline is going away.
  opened.push(() => {
    closed = true;
  });
  const fail = (reason: AudioFailure) => {
    if (!closed) failed(reason);
  };
  const closeCodec = (codec: AudioEncoder | AudioDecoder) => () => {
    if (codec.state !== 'closed') codec.close();
  };
  const counts = { captured: 0, sent: 0, received: 0, decoded: 0 };
  let playedSamples = 0;
  let outputQuanta = 0;
  let outputQuantaSeen = 0;
  let micPeak = 0;
  let incomingPeak = 0;
  const peakOf = (samples: Float32Array) => {
    let peak = 0;
    for (let i = 0; i < samples.length; i += 8) {
      const value = Math.abs(samples[i]);
      if (value > peak) peak = value;
    }
    return peak;
  };

  entering('codec');
  const encoder = new AudioEncoder({
    output: (chunk) => {
      if (closed || chunk.byteLength === 0 || chunk.byteLength > maxAudioFrame)
        return;
      const data = new Uint8Array(chunk.byteLength);
      chunk.copyTo(data);
      counts.sent += 1;
      // Microseconds to 48 kHz sample units, wrapped to 32 bits.
      send(Math.round((chunk.timestamp * 48) / 1000) >>> 0, data);
    },
    error: () => fail('failed'),
  });
  opened.push(closeCodec(encoder));
  const constant = await AudioEncoder.isConfigSupported(opusConstant).catch(
    () => ({ supported: false }),
  );
  encoder.configure(constant.supported ? opusConstant : opusPlain);

  entering('graph');
  const playback = new AudioWorkletNode(context, 'whatsup-call-playback', {
    numberOfInputs: 0,
    numberOfOutputs: 1,
    outputChannelCount: [1],
  });
  opened.push(() => {
    playback.port.onmessage = null;
    playback.disconnect();
  });
  playback.connect(context.destination);
  playback.port.onmessage = (event) => {
    const report = event.data as { played?: unknown; quanta?: unknown } | null;
    if (typeof report?.played === 'number') playedSamples = report.played;
    if (typeof report?.quanta === 'number') outputQuanta = report.quanta;
  };
  entering('codec');
  const decoder = new AudioDecoder({
    output: (audio) => {
      if (closed) {
        audio.close();
        return;
      }
      const samples = new Float32Array(audio.numberOfFrames);
      audio.copyTo(samples, { planeIndex: 0, format: 'f32-planar' });
      audio.close();
      counts.decoded += 1;
      incomingPeak = Math.max(incomingPeak, peakOf(samples));
      playback.port.postMessage(samples, [samples.buffer]);
    },
    error: () => fail('failed'),
  });
  opened.push(closeCodec(decoder));
  decoder.configure(opusDecode);

  entering('graph');
  const source = context.createMediaStreamSource(stream);
  opened.push(() => source.disconnect());
  const capture = new AudioWorkletNode(context, 'whatsup-call-capture', {
    numberOfInputs: 1,
    numberOfOutputs: 1,
    outputChannelCount: [1],
  });
  opened.push(() => {
    capture.port.onmessage = null;
    capture.disconnect();
  });
  // The capture node needs a path to the output to be processed; a silent
  // gain keeps the microphone from being heard locally.
  const silent = context.createGain();
  opened.push(() => silent.disconnect());
  silent.gain.value = 0;
  capture.connect(silent).connect(context.destination);
  let frame = 0;
  capture.port.onmessage = (event) => {
    const samples = event.data;
    if (
      closed ||
      !(samples instanceof Float32Array) ||
      samples.length !== audioFrameSamples ||
      encoder.state !== 'configured'
    )
      return;
    counts.captured += 1;
    micPeak = Math.max(micPeak, peakOf(samples));
    const data = new AudioData({
      format: 'f32-planar',
      sampleRate: 48000,
      numberOfFrames: audioFrameSamples,
      numberOfChannels: 1,
      timestamp: frame * 20000,
      data: samples as Float32Array<ArrayBuffer>,
    });
    frame += 1;
    encoder.encode(data);
    data.close();
  };
  source.connect(capture);

  let lastTimestamp = -1;
  return {
    setMuted(muted) {
      capture.port.postMessage({ muted });
      stream.getAudioTracks().forEach((track) => {
        track.enabled = !muted;
      });
    },
    receive(timestamp, data) {
      counts.received += 1;
      if (closed || decoder.state !== 'configured') return;
      // Decode strictly forward in time. Anything older than what was
      // already decoded has missed its moment.
      if (lastTimestamp >= 0) {
        const ahead = (timestamp - lastTimestamp) >>> 0;
        if (ahead === 0 || ahead > 0x7fffffff) return;
      }
      lastTimestamp = timestamp;
      decoder.decode(
        new EncodedAudioChunk({
          type: 'key',
          timestamp: Math.round((timestamp * 1000) / 48),
          duration: 20000,
          data,
        }),
      );
    },
    stats() {
      // Levels cover the time since the last reading. The output counts as
      // running only if the audio thread advanced since then.
      const level = (peak: number) => Math.min(100, Math.round(peak * 100));
      const result: AudioStats = {
        ...counts,
        playedSeconds: Math.round((playedSamples / 48000) * 10) / 10,
        micLevel: level(micPeak),
        incomingLevel: level(incomingPeak),
        output:
          context.state === 'running' && outputQuanta !== outputQuantaSeen
            ? 'running'
            : 'stopped',
      };
      outputQuantaSeen = outputQuanta;
      micPeak = 0;
      incomingPeak = 0;
      return result;
    },
    close() {
      if (closed) return;
      closed = true;
      // The same releases a failed setup runs, so the two cannot drift.
      releaseAll(opened);
    },
  };
}
