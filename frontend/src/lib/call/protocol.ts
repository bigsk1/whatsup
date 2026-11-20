// Wire format of the call media stream between this page and Go. The stream
// carries encoded audio only; keys, addresses and sealing stay in Go.

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
