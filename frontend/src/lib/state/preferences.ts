import { writable } from 'svelte/store';
import { hasDesktopBridge } from '../backend';

export interface AppPreferences {
  closeToTray: boolean;
  startAtLogin: boolean;
  notifications: boolean;
  notificationName: boolean;
  notificationPreview: boolean;
  voiceCalls: boolean;
  callRingMuted: boolean;
}

export const defaultAppPreferences: AppPreferences = Object.freeze({
  closeToTray: true,
  startAtLogin: false,
  notifications: true,
  notificationName: false,
  notificationPreview: false,
  voiceCalls: false,
  callRingMuted: false,
});

export function validateAppPreferences(value: unknown): AppPreferences {
  if (!value || typeof value !== 'object' || Array.isArray(value))
    throw new Error('Invalid app preferences.');
  const record = value as Record<string, unknown>;
  const keys = Object.keys(defaultAppPreferences) as (keyof AppPreferences)[];
  if (
    Object.keys(record).length !== keys.length ||
    keys.some((key) => typeof record[key] !== 'boolean')
  )
    throw new Error('Invalid app preferences.');
  return Object.fromEntries(
    keys.map((key) => [key, record[key]]),
  ) as unknown as AppPreferences;
}

async function service() {
  if (!hasDesktopBridge())
    throw new Error('Open the desktop app to change its preferences.');
  return import('../../../bindings/whatsup/internal/desktop/service');
}

export function createAppPreferences(
  bridge = {
    get: async (): Promise<unknown> => (await service()).GetAppPreferences(),
    set: async (value: AppPreferences): Promise<unknown> =>
      (await service()).SetAppPreferences(value),
  },
  /** Runs with the values about to be saved, before the desktop is asked. */
  beforeSave: (next: AppPreferences) => void = () => {},
) {
  let current = {
    value: { ...defaultAppPreferences },
    loaded: false,
    busy: false,
    notice: '',
  };
  const state = writable(current);
  let generation = 0;
  const publish = (value: Partial<typeof current>) => {
    current = { ...current, ...value };
    state.set(current);
  };
  async function load() {
    if (current.busy) return;
    const epoch = ++generation;
    publish({ busy: true, notice: '' });
    try {
      const value = validateAppPreferences(await bridge.get());
      if (epoch === generation) publish({ value, loaded: true });
    } catch {
      if (epoch === generation)
        publish({
          loaded: false,
          notice: 'App preferences could not be loaded. Try again.',
        });
    } finally {
      if (epoch === generation) publish({ busy: false });
    }
  }
  async function save(input: AppPreferences) {
    if (!current.loaded || current.busy) return;
    const epoch = ++generation;
    publish({ busy: true, notice: '' });
    try {
      const next = validateAppPreferences(input);
      beforeSave(next);
      const value = validateAppPreferences(await bridge.set(next));
      if (epoch === generation)
        publish({ value, notice: 'App preferences saved on this device.' });
    } catch {
      if (epoch === generation)
        publish({
          loaded: false,
          notice:
            'App preferences could not be saved. Reload them before trying again.',
        });
    } finally {
      if (epoch === generation) publish({ busy: false });
    }
  }
  return {
    state,
    load,
    save,
    stop() {
      generation += 1;
      current = {
        value: { ...defaultAppPreferences },
        loaded: false,
        busy: false,
        notice: '',
      };
      state.set(current);
    },
  };
}
