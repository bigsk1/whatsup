import type { ConversationSnapshot } from './messaging';

export type Tone = 'ok' | 'warn' | 'danger' | 'idle';

export function presenceTone(
  state: ConversationSnapshot['state'],
  connecting = false,
): Tone {
  if (connecting || state === 'connecting') return 'warn';
  switch (state) {
    case 'connected':
      return 'ok';
    case 'none':
    case 'unavailable':
      return 'idle';
    case 'offline':
    case 'failed':
    case 'identity-changed':
    case 'authentication-failed':
    case 'update-required':
    case 'route-unavailable':
    case 'capacity-reached':
    case 'peer-capacity-reached':
    case 'peer-removed':
      return 'danger';
    default: {
      const unhandled: never = state;
      return unhandled;
    }
  }
}

export function avatarTone(seed: string): number {
  let hash = 0;
  for (const character of seed)
    hash = (hash * 31 + character.charCodeAt(0)) | 0;
  return (Math.abs(hash) % 6) + 1;
}

const segmenter =
  typeof Intl !== 'undefined' && 'Segmenter' in Intl
    ? new Intl.Segmenter(undefined, { granularity: 'grapheme' })
    : null;

export function initial(name: string): string {
  const trimmed = name.trim();
  if (!trimmed) return '?';
  const first = segmenter
    ? (segmenter.segment(trimmed)[Symbol.iterator]().next().value?.segment ??
      '?')
    : Array.from(trimmed)[0];
  return first.toLocaleUpperCase();
}
