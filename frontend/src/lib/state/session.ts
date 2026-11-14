import { writable } from 'svelte/store';
import { Events } from '@wailsio/runtime';
import {
  hasDesktopBridge,
  requestStatus,
  validateStatus,
  unavailableStatus,
  type StatusMethod,
} from '../backend';

type Subscribe = (
  name: string,
  callback: (value: unknown) => void,
) => () => void;
export function createSession(
  request = requestStatus,
  subscribe: Subscribe = (name, callback) =>
    Events.On(name, (event) => callback(event.data)),
) {
  let native = false;
  let busy = false;
  let generation = 0;
  const state = writable({
    native,
    busy,
    status: { ...unavailableStatus },
    notice: '',
    windowVisible: true,
  });
  function mount(
    onready: () => void,
    callbacks: {
      storage?: () => void;
      visibility?: (visible: boolean) => void;
    } = {},
  ) {
    const activate = () => {
      if (native || !hasDesktopBridge()) return;
      native = true;
      state.update((value) => ({ ...value, native }));
      onready();
    };
    const stopStorage = subscribe('whatsup:storage-changed', (value) => {
      if (
        !native ||
        !value ||
        typeof value !== 'object' ||
        Array.isArray(value)
      )
        return;
      const record = value as Record<string, unknown>;
      if (
        (Object.keys(record).length !== 1 &&
          !(
            Object.keys(record).length === 2 &&
            typeof record.requiresPassphrase === 'boolean'
          )) ||
        !['locked', 'unlocked', 'unavailable'].includes(record.state as string)
      )
        return;
      // A native hint can retire permissions, never grant them. Fetch the full
      // authoritative status before showing an identity or reading messages.
      generation += 1;
      busy = false;
      state.update((current) => ({
        ...current,
        busy,
        status: {
          ...unavailableStatus,
          storageState:
            record.state === 'unavailable' ? 'unavailable' : 'locked',
          requiresPassphrase: record.requiresPassphrase === true,
        },
        notice: '',
      }));
      callbacks.storage?.();
      onready();
    });
    const stopVisibility = subscribe('whatsup:window-visible', (value) => {
      if (typeof value !== 'boolean') return;
      state.update((current) => ({ ...current, windowVisible: value }));
      callbacks.visibility?.(value);
    });
    window.addEventListener('wails:runtime-config-ready', activate);
    activate();
    return () => {
      generation += 1;
      native = false;
      busy = false;
      window.removeEventListener('wails:runtime-config-ready', activate);
      stopStorage();
      stopVisibility();
      state.set({
        native,
        busy,
        status: { ...unavailableStatus },
        notice: '',
        windowVisible: true,
      });
    };
  }
  async function refresh(method: StatusMethod = 'GetStatus') {
    if (!native || busy) return null;
    const epoch = generation;
    busy = true;
    state.update((value) => ({ ...value, busy, notice: '' }));
    try {
      const status = await request(method);
      if (epoch !== generation) return null;
      state.update((value) => ({ ...value, status }));
      return status;
    } catch {
      if (epoch !== generation) return null;
      state.update((value) => ({
        ...value,
        status: { ...unavailableStatus },
        notice:
          method === 'UnlockStorage'
            ? 'Local storage could not be unlocked. Check that your OS credential store is available, then try again.'
            : 'The desktop could not report its status. Local actions are unavailable. Try again.',
      }));
      return { ...unavailableStatus };
    } finally {
      if (epoch === generation) {
        busy = false;
        state.update((value) => ({ ...value, busy }));
      }
    }
  }
  function adopt(input: unknown) {
    const status = validateStatus(input);
    generation += 1;
    busy = false;
    state.update((value) => ({ ...value, busy, status, notice: '' }));
  }
  return { state, mount, refresh, adopt };
}
