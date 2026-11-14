import { writable } from 'svelte/store';
import {
  hasDesktopBridge,
  requestStatus,
  unavailableStatus,
  type StatusMethod,
} from '../backend';

export function createSession(request = requestStatus) {
  let native = false;
  let busy = false;
  let generation = 0;
  const state = writable({
    native,
    busy,
    status: { ...unavailableStatus },
    notice: '',
  });
  function mount(onready: () => void) {
    const activate = () => {
      if (native || !hasDesktopBridge()) return;
      native = true;
      state.update((value) => ({ ...value, native }));
      onready();
    };
    window.addEventListener('wails:runtime-config-ready', activate);
    activate();
    return () => {
      generation += 1;
      native = false;
      busy = false;
      window.removeEventListener('wails:runtime-config-ready', activate);
      state.set({ native, busy, status: { ...unavailableStatus }, notice: '' });
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
  return { state, mount, refresh };
}
