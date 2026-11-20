import ringtoneURL from '../../assets/ringtone.wav?url';
import { sharedAudioContext } from './context';

export interface Ringer {
  /** Incoming call: loop the ring sound. */
  ringtone(): void;
  /** Outgoing call: a quiet repeating tone while the other side rings. */
  ringback(): void;
  stop(): void;
}

/**
 * Ring sounds. They play through the shared audio context, which the browser
 * keeps suspended until the person has clicked or typed in the window once. If
 * that has not happened yet the call still shows on screen, silently.
 */
export function createRinger(muted: () => boolean): Ringer {
  let generation = 0;
  let cleanup: (() => void) | null = null;
  let ringBuffer: Promise<AudioBuffer> | null = null;

  function stop() {
    generation += 1;
    cleanup?.();
    cleanup = null;
  }

  function context(): AudioContext | null {
    try {
      const shared = sharedAudioContext();
      if (shared.state === 'suspended') void shared.resume().catch(() => {});
      return shared;
    } catch {
      return null;
    }
  }

  return {
    stop,
    ringtone() {
      stop();
      if (muted()) return;
      const audio = context();
      if (!audio) return;
      const epoch = generation;
      ringBuffer ??= fetch(ringtoneURL)
        .then((response) => response.arrayBuffer())
        .then((bytes) => audio.decodeAudioData(bytes));
      ringBuffer
        .then((buffer) => {
          if (epoch !== generation) return;
          const source = audio.createBufferSource();
          source.buffer = buffer;
          source.loop = true;
          const gain = audio.createGain();
          gain.gain.value = 0.9;
          source.connect(gain).connect(audio.destination);
          source.start();
          cleanup = () => {
            try {
              source.stop();
            } catch {
              /* never started */
            }
            source.disconnect();
            gain.disconnect();
          };
        })
        .catch(() => {
          ringBuffer = null;
        });
    },
    ringback() {
      stop();
      if (muted()) return;
      const audio = context();
      if (!audio) return;
      const oscillator = audio.createOscillator();
      oscillator.frequency.value = 425;
      const gain = audio.createGain();
      gain.gain.value = 0;
      oscillator.connect(gain).connect(audio.destination);
      oscillator.start();
      const beep = () => {
        const now = audio.currentTime;
        gain.gain.cancelScheduledValues(now);
        gain.gain.setValueAtTime(0, now);
        gain.gain.linearRampToValueAtTime(0.06, now + 0.05);
        gain.gain.setValueAtTime(0.06, now + 1.0);
        gain.gain.linearRampToValueAtTime(0, now + 1.05);
      };
      beep();
      const timer = setInterval(beep, 4000);
      cleanup = () => {
        clearInterval(timer);
        try {
          oscillator.stop();
        } catch {
          /* never started */
        }
        oscillator.disconnect();
        gain.disconnect();
      };
    },
  };
}
