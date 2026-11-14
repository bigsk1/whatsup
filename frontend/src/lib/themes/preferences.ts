export const themes = ['dark', 'light', 'system'] as const;
export const accents = ['teal', 'blue', 'rose'] as const;
export type Theme = (typeof themes)[number];
export type Accent = (typeof accents)[number];
export interface Preferences {
  theme: Theme;
  accent: Accent;
}

export const preferenceKey = 'whatsup.appearance.v1';
export const defaults: Preferences = { theme: 'dark', accent: 'teal' };

export function parsePreferences(raw: string | null): Preferences {
  if (!raw || raw.length > 256) return { ...defaults };
  try {
    const value: unknown = JSON.parse(raw);
    if (!value || typeof value !== 'object') return { ...defaults };
    const { theme, accent } = value as Record<string, unknown>;
    return {
      theme: themes.includes(theme as Theme)
        ? (theme as Theme)
        : defaults.theme,
      accent: accents.includes(accent as Accent)
        ? (accent as Accent)
        : defaults.accent,
    };
  } catch {
    return { ...defaults };
  }
}

export function readPreferences(): Preferences {
  try {
    return parsePreferences(localStorage.getItem(preferenceKey));
  } catch {
    return { ...defaults };
  }
}

export function savePreferences(preferences: Preferences): boolean {
  try {
    // Appearance only. Never persist messages, invites, identities, or endpoints here.
    localStorage.setItem(preferenceKey, JSON.stringify(preferences));
    return true;
  } catch {
    return false;
  }
}

export function applyPreferences(preferences: Preferences): void {
  const dark =
    preferences.theme === 'dark' ||
    (preferences.theme === 'system' &&
      window.matchMedia('(prefers-color-scheme: dark)').matches);
  document.documentElement.dataset.theme = dark ? 'dark' : 'light';
  document.documentElement.dataset.accent = preferences.accent;
}
