import {
  paletteIDs,
  paletteInfo,
  skins,
  textSizes,
  type PaletteColors,
  type PaletteId,
  type Skin,
  type TextSize,
} from './catalog';
import { look } from './look.svelte';

export interface Preferences {
  skin: Skin;
  palette: PaletteId;
  lightPalette: PaletteId;
  followSystem: boolean;
  textSize: TextSize;
  effects: boolean;
}

export const preferenceKey = 'whatsup.appearance.v2';
export const legacyPreferenceKey = 'whatsup.appearance.v1';
export const defaults: Preferences = {
  skin: 'bubble',
  palette: 'whatsup',
  lightPalette: 'daybreak',
  followSystem: false,
  textSize: 'comfortable',
  effects: true,
};

const includes = <T extends string>(list: readonly T[], value: unknown) =>
  typeof value === 'string' && (list as readonly string[]).includes(value);

function parseObject(raw: string | null): Record<string, unknown> | null {
  if (!raw || raw.length > 512) return null;
  try {
    const value: unknown = JSON.parse(raw);
    return value && typeof value === 'object' && !Array.isArray(value)
      ? (value as Record<string, unknown>)
      : null;
  } catch {
    return null;
  }
}

export function parsePreferences(raw: string | null): Preferences {
  const value = parseObject(raw);
  if (!value) return { ...defaults };
  return {
    skin: includes(skins, value.skin) ? (value.skin as Skin) : defaults.skin,
    palette: includes(paletteIDs, value.palette)
      ? (value.palette as PaletteId)
      : defaults.palette,
    lightPalette: includes(paletteIDs, value.lightPalette)
      ? (value.lightPalette as PaletteId)
      : defaults.lightPalette,
    followSystem:
      typeof value.followSystem === 'boolean'
        ? value.followSystem
        : defaults.followSystem,
    textSize: includes(textSizes, value.textSize)
      ? (value.textSize as TextSize)
      : defaults.textSize,
    effects:
      typeof value.effects === 'boolean' ? value.effects : defaults.effects,
  };
}

export function migrateLegacyPreferences(raw: string | null): Preferences {
  const value = parseObject(raw);
  if (value?.theme === 'light') return { ...defaults, palette: 'daybreak' };
  if (value?.theme === 'system') return { ...defaults, followSystem: true };
  return { ...defaults };
}

export function readPreferences(): Preferences {
  try {
    const current = localStorage.getItem(preferenceKey);
    if (current !== null) return parsePreferences(current);
    return migrateLegacyPreferences(localStorage.getItem(legacyPreferenceKey));
  } catch {
    return { ...defaults };
  }
}

export function savePreferences(preferences: Preferences): boolean {
  try {
    // Appearance only. Never persist messages, invites, identities, or endpoints here.
    localStorage.setItem(preferenceKey, JSON.stringify(preferences));
    localStorage.removeItem(legacyPreferenceKey);
    return true;
  } catch {
    return false;
  }
}

export function activePalette(
  preferences: Preferences,
  systemDark: boolean,
): PaletteId {
  return preferences.followSystem && !systemDark
    ? preferences.lightPalette
    : preferences.palette;
}

const variableNames: Record<Exclude<keyof PaletteColors, 'avatars'>, string> = {
  bg: '--bg',
  panel: '--panel',
  surface: '--surface',
  raised: '--raised',
  border: '--border',
  text: '--text',
  muted: '--muted',
  faint: '--faint',
  accent: '--accent',
  onAccent: '--on-accent',
  accent2: '--accent-2',
  bubbleIn: '--bubble-in',
  onBubbleIn: '--on-bubble-in',
  bubbleOut: '--bubble-out',
  onBubbleOut: '--on-bubble-out',
  ok: '--ok',
  warn: '--warn',
  danger: '--danger',
};

export function applyPreferences(preferences: Preferences): void {
  const systemDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
  const palette = paletteInfo(activePalette(preferences, systemDark));
  const root = document.documentElement;
  // CSSOM custom properties are permitted by the strict style-src policy;
  // values come only from the bundled palette catalog, never from storage.
  for (const [key, name] of Object.entries(variableNames))
    root.style.setProperty(
      name,
      palette.colors[key as keyof typeof variableNames],
    );
  palette.colors.avatars.forEach((color, index) =>
    root.style.setProperty(`--avatar-${index + 1}`, color),
  );
  root.dataset.theme = palette.scheme;
  root.dataset.palette = palette.id;
  root.dataset.skin = preferences.skin;
  root.dataset.textSize = preferences.textSize;
  root.dataset.effects = preferences.effects ? 'on' : 'off';
  look.skin = preferences.skin;
  look.palette = palette.id;
}
