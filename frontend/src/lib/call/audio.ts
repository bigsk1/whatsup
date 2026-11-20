import captureWorkletURL from './capture.worklet.js?url';
import playbackWorkletURL from './playback.worklet.js?url';
import { sharedAudioContext } from './context';
import { audioFrameSamples, maxAudioFrame } from './protocol';

export type AudioFailure =
  'permission' | 'no-microphone' | 'unsupported' | 'failed';

/** The running capture, encode, decode and playback chain for one call. */
export interface AudioPipeline {
  setMuted(muted: boolean): void;
  receive(timestamp: number, data: Uint8Array): void;
  close(): void;
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
  failed(reason: AudioFailure): void;
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
    } catch {
      if (epoch !== generation) return;
      stop();
      options.failed('unsupported');
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
  const context = sharedAudioContext();
  if (context.state !== 'running') await context.resume();
  if (context.sampleRate !== 48000) throw new Error('unsupported sample rate');
  await loadWorklets(context);

  let closed = false;
  const fail = (reason: AudioFailure) => {
    if (!closed) failed(reason);
  };

  const encoder = new AudioEncoder({
    output: (chunk) => {
      if (closed || chunk.byteLength === 0 || chunk.byteLength > maxAudioFrame)
        return;
      const data = new Uint8Array(chunk.byteLength);
      chunk.copyTo(data);
      // Microseconds to 48 kHz sample units, wrapped to 32 bits.
      send(Math.round((chunk.timestamp * 48) / 1000) >>> 0, data);
    },
    error: () => fail('failed'),
  });
  const constant = await AudioEncoder.isConfigSupported(opusConstant).catch(
    () => ({ supported: false }),
  );
  encoder.configure(constant.supported ? opusConstant : opusPlain);

  const playback = new AudioWorkletNode(context, 'whatsup-call-playback', {
    numberOfInputs: 0,
    numberOfOutputs: 1,
    outputChannelCount: [1],
  });
  playback.connect(context.destination);
  const decoder = new AudioDecoder({
    output: (audio) => {
      if (closed) {
        audio.close();
        return;
      }
      const samples = new Float32Array(audio.numberOfFrames);
      audio.copyTo(samples, { planeIndex: 0, format: 'f32-planar' });
      audio.close();
      playback.port.postMessage(samples, [samples.buffer]);
    },
    error: () => fail('failed'),
  });
  decoder.configure(opusDecode);

  const source = context.createMediaStreamSource(stream);
  const capture = new AudioWorkletNode(context, 'whatsup-call-capture', {
    numberOfInputs: 1,
    numberOfOutputs: 1,
    outputChannelCount: [1],
  });
  // The capture node needs a path to the output to be processed; a silent
  // gain keeps the microphone from being heard locally.
  const silent = context.createGain();
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
    close() {
      if (closed) return;
      closed = true;
      capture.port.onmessage = null;
      for (const node of [source, capture, silent, playback]) {
        try {
          node.disconnect();
        } catch {
          /* already disconnected */
        }
      }
      for (const codec of [encoder, decoder]) {
        try {
          if (codec.state !== 'closed') codec.close();
        } catch {
          /* already closed */
        }
      }
    },
  };
}
