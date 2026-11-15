import { writable } from 'svelte/store';
import { Events } from '@wailsio/runtime';
import { validID } from './bridge';
import { hasDesktopBridge } from '../backend';

export interface FileDragHint {
  active: boolean;
  revision: number;
}
export function validateFileDrag(
  input: unknown,
  snapshot = false,
): FileDragHint | null {
  if (!input || typeof input !== 'object' || Array.isArray(input)) return null;
  const value = input as Record<string, unknown>;
  return Object.keys(value).length === 2 &&
    typeof value.active === 'boolean' &&
    Number.isSafeInteger(value.revision) &&
    ((value.revision as number) > 0 ||
      (snapshot && value.revision === 0 && value.active === false))
    ? { active: value.active, revision: value.revision as number }
    : null;
}
type Subscribe = (callback: (hint: unknown) => void) => () => void;
const subscribe: Subscribe = (callback) =>
  Events.On('whatsup:file-drag', (event) => callback(event.data));
async function takeHint(ack: number): Promise<unknown> {
  if (!hasDesktopBridge()) throw new Error('Native drag state is unavailable.');
  return (
    await import('../../../bindings/whatsup/nativelifecycle')
  ).TakeFileDragHint(ack);
}

// A display-only signal. File paths and staging stay entirely in Go.
export function createFileDrag(
  listen: Subscribe = subscribe,
  take: (ack: number) => Promise<unknown> = takeHint,
) {
  const state = writable(false);
  let stopEvents: (() => void) | undefined,
    revision = 0,
    generation = 0;
  let selected = '',
    enabled = false;
  let loading = false,
    pendingAck = 0,
    retryAck = 0;
  async function read(ack: number) {
    if (loading) {
      pendingAck = Math.max(pendingAck, ack);
      return;
    }
    const epoch = generation;
    loading = true;
    try {
      const hint = validateFileDrag(await take(ack), true);
      if (!hint) throw new Error('Invalid native drag snapshot.');
      if (epoch === generation && ack === retryAck) retryAck = 0;
      if (
        epoch === generation &&
        (hint.revision === 0 || hint.revision >= revision)
      ) {
        revision = Math.max(revision, hint.revision);
        state.set(enabled && hint.active);
      }
    } catch {
      if (epoch === generation) {
        if (ack) retryAck = ack;
        state.set(false);
      }
    } finally {
      if (epoch === generation) {
        loading = false;
        if (pendingAck) {
          const next = pendingAck;
          pendingAck = 0;
          void read(next);
        }
      }
    }
  }
  function select(id: string, allowed: boolean) {
    const next = allowed && validID(id) ? id : '';
    if (next !== selected || !allowed) state.set(false);
    selected = next;
    enabled = next !== '';
  }
  function start() {
    if (stopEvents) return;
    const epoch = ++generation;
    stopEvents = listen((input) => {
      const hint = validateFileDrag(input);
      if (epoch !== generation || !hint) return;
      // ACK even if a snapshot already observed this revision: Go returns the
      // newest state and keeps at most one outstanding event downstream.
      void read(hint.revision);
    });
  }
  function stop() {
    generation++;
    stopEvents?.();
    stopEvents = undefined;
    selected = '';
    enabled = false;
    loading = false;
    pendingAck = 0;
    retryAck = 0;
    state.set(false);
  }
  return { state, select, start, ready: () => read(retryAck || 0), stop };
}
