import { get } from 'svelte/store';
import { describe, it, expect, vi } from 'vitest';
import { createNotificationActivation } from './notification-activation';

const first = 'AQEBAQEBAQEBAQEBAQEBAQ';
const other = 'AgICAgICAgICAgICAgICAg';
describe('notification activation authority', () => {
  it('waits for unlock and an authoritative contact list without unlocking or adding contacts', async () => {
    const select = vi.fn(),
      take = vi.fn(async () => first);
    const store = createNotificationActivation(select, take, () => () => {});
    const stop = store.mount();
    await store.ready();
    store.resolve(false, false, []);
    store.resolve(true, false, []);
    expect(select).not.toHaveBeenCalled();
    expect(get(store.state)).toBe(first);
    store.resolve(true, true, [first]);
    expect(select).toHaveBeenCalledExactlyOnceWith(first);
    expect(get(store.state)).toBe('');
    stop();
  });
  it('drops unknown/forgotten contacts and malformed event shapes', async () => {
    const select = vi.fn(),
      take = vi.fn(async () => other);
    let emit!: (hint: unknown) => void;
    const store = createNotificationActivation(select, take, (cb) => {
      emit = cb;
      return () => {};
    });
    const stop = store.mount();
    emit({ contactID: other, title: 'untrusted extra' });
    expect(take).not.toHaveBeenCalled();
    await store.ready();
    store.resolve(true, true, [first]);
    expect(select).not.toHaveBeenCalled();
    expect(get(store.state)).toBe('');
    stop();
  });
  it('does not restore an activation after explicit offline or unmount while its read is pending', async () => {
    let release!: (value: string) => void;
    const select = vi.fn();
    const store = createNotificationActivation(
      select,
      () => new Promise((done) => (release = done)),
      () => () => {},
    );
    const stop = store.mount();
    const pending = store.ready();
    store.clear();
    release(first);
    await pending;
    store.resolve(true, true, [first]);
    expect(select).not.toHaveBeenCalled();
    stop();
    expect(get(store.state)).toBe('');
  });
});
