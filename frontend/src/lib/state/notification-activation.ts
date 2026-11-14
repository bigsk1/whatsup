import { writable } from 'svelte/store';
import { Events } from '@wailsio/runtime';
import { hasDesktopBridge } from '../backend';
import { validID } from './bridge';

type Subscribe = (callback: (hint: unknown) => void) => () => void;
export function createNotificationActivation(
  select: (id: string) => void,
  take = async (): Promise<unknown> => {
    if (!hasDesktopBridge()) return '';
    return (
      await import('../../../bindings/whatsup/nativelifecycle')
    ).TakeNotificationActivation();
  },
  subscribe: Subscribe = (callback) =>
    Events.On('whatsup:notification-activate', (event) => callback(event.data)),
) {
  const state = writable('');
  let target = '',
    generation = 0,
    mounted = false,
    taking = false,
    again = false,
    wasEnabled = false;
  let stop: (() => void) | undefined;
  const publish = (id: string) => {
    target = id;
    state.set(id);
  };
  function clear() {
    generation++;
    publish('');
  }
  async function receive() {
    if (!mounted) return;
    if (taking) {
      again = true;
      return;
    }
    taking = true;
    const epoch = generation;
    try {
      const id = await take();
      if (mounted && epoch === generation && validID(id)) publish(id);
    } catch {
      /* Notification activation grants no access on a bridge failure. */
    } finally {
      taking = false;
      if (again && mounted) {
        again = false;
        void receive();
      }
    }
  }
  return {
    state,
    clear,
    ready: receive,
    mount() {
      mounted = true;
      stop = subscribe((hint) => {
        if (!hint || typeof hint !== 'object' || Array.isArray(hint)) return;
        const value = hint as Record<string, unknown>;
        if (Object.keys(value).length === 1 && validID(value.contactID))
          void receive();
      });
      return () => {
        mounted = false;
        stop?.();
        stop = undefined;
        clear();
      };
    },
    resolve(enabled: boolean, loaded: boolean, ids: string[]) {
      if (wasEnabled && !enabled) clear();
      wasEnabled = enabled;
      if (!enabled || !loaded || !target) return;
      const id = target;
      publish('');
      // The notification is a hint, never a contact or unlock capability.
      if (ids.includes(id)) select(id);
    },
  };
}
