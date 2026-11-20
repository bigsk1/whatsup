// Wire format of the call media stream between this page and Go. The stream
// carries encoded audio and video; keys, addresses and sealing stay in Go.

export const callMediaStream = 'whatsup-call-media';

const kindHello = 0;
const kindAudio = 1;
const flagRecovered = 1;

export const maxAudioFrame = 500;
export const audioFrameSamples = 960;

const callIDPattern = /^[A-Za-z0-9_-]{21}[AQgw]$/;

export const validCallID = (value: unknown): value is string =>
  typeof value === 'string' &&
  callIDPattern.test(value) &&
  value !== 'A'.repeat(22);

/** The first frame on the stream names the answered call it belongs to. */
export function encodeHello(callID: string): Uint8Array {
  if (!validCallID(callID)) throw new Error('Invalid call handle.');
  const frame = new Uint8Array(1 + 22);
  frame[0] = kindHello;
  for (let i = 0; i < 22; i++) frame[1 + i] = callID.charCodeAt(i);
  return frame;
}

/** One encoded audio frame from this device's microphone. */
export function encodeAudio(timestamp: number, data: Uint8Array): Uint8Array {
  if (data.length === 0 || data.length > maxAudioFrame)
    throw new Error('Invalid audio frame.');
  const frame = new Uint8Array(5 + data.length);
  frame[0] = kindAudio;
  new DataView(frame.buffer).setUint32(1, timestamp >>> 0);
  frame.set(data, 5);
  return frame;
}

export interface ReceivedAudio {
  timestamp: number;
  recovered: boolean;
  data: Uint8Array;
}

/** One encoded audio frame from the other person, or null if not audio. */
export function decodeAudio(
  input: ArrayBuffer | Uint8Array,
): ReceivedAudio | null {
  const frame = input instanceof Uint8Array ? input : new Uint8Array(input);
  if (
    frame.length <= 6 ||
    frame.length > 6 + maxAudioFrame ||
    frame[0] !== kindAudio ||
    (frame[1] & ~flagRecovered) !== 0
  )
    return null;
  return {
    recovered: (frame[1] & flagRecovered) !== 0,
    timestamp: new DataView(
      frame.buffer,
      frame.byteOffset,
      frame.byteLength,
    ).getUint32(2),
    data: frame.subarray(6),
  };
}

// Video rides the same stream. A video frame is one whole encoded picture; Go
// splits it into sealed packets and reassembles it on the other device.
const kindVideo = 2;
const kindKeyframe = 3;
const flagKey = 1;

/** The largest encoded picture Go accepts (256 KiB). */
export const maxVideoFrame = 256 * 1024;

/** One encoded picture from this device's camera. */
export function encodeVideo(
  timestamp: number,
  key: boolean,
  data: Uint8Array,
): Uint8Array {
  if (data.length === 0 || data.length > maxVideoFrame)
    throw new Error('Invalid video frame.');
  const frame = new Uint8Array(6 + data.length);
  frame[0] = kindVideo;
  frame[1] = key ? flagKey : 0;
  new DataView(frame.buffer).setUint32(2, timestamp >>> 0);
  frame.set(data, 6);
  return frame;
}

export type ReceivedMedia =
  | ({ kind: 'audio' } & ReceivedAudio)
  | { kind: 'video'; timestamp: number; key: boolean; data: Uint8Array }
  /** Go asks this device's video encoder for a keyframe now. */
  | { kind: 'keyframe' };

/** Anything Go sends on the stream, or null if it is not well formed. */
export function decodeMedia(
  input: ArrayBuffer | Uint8Array,
): ReceivedMedia | null {
  const frame = input instanceof Uint8Array ? input : new Uint8Array(input);
  if (frame.length === 0) return null;
  if (frame[0] === kindKeyframe)
    return frame.length === 1 ? { kind: 'keyframe' } : null;
  if (frame[0] === kindVideo) {
    if (
      frame.length <= 6 ||
      frame.length > 6 + maxVideoFrame ||
      (frame[1] & ~flagKey) !== 0
    )
      return null;
    return {
      kind: 'video',
      key: (frame[1] & flagKey) !== 0,
      timestamp: new DataView(
        frame.buffer,
        frame.byteOffset,
        frame.byteLength,
      ).getUint32(2),
      data: frame.subarray(6),
    };
  }
  const audio = decodeAudio(frame);
  return audio ? { kind: 'audio', ...audio } : null;
}
