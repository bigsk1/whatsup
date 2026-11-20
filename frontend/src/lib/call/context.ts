// One audio context for the ring sound and for call audio. Browsers start a
// context suspended until the person has interacted with the page, so the app
// resumes it on the first click or key press and keeps it for later rings.

let shared: AudioContext | null = null;

export function sharedAudioContext(): AudioContext {
  shared ??= new AudioContext({
    sampleRate: 48000,
    latencyHint: 'interactive',
  });
  return shared;
}

/** Call from inside a click or key handler. Safe to call any number of times. */
export function primeAudio(): void {
  try {
    const context = sharedAudioContext();
    if (context.state === 'suspended') void context.resume().catch(() => {});
  } catch {
    /* No audio output is available; calls will report that when they start. */
  }
}

export function audioAvailable(): boolean {
  return (
    typeof AudioContext !== 'undefined' &&
    typeof AudioWorkletNode !== 'undefined' &&
    typeof AudioEncoder !== 'undefined' &&
    typeof AudioDecoder !== 'undefined' &&
    Boolean(navigator.mediaDevices?.getUserMedia)
  );
}
