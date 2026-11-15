import { get } from 'svelte/store';
import { describe, expect, it, vi } from 'vitest';
import { createFileDrag, validateFileDrag } from './file-drag';

const selected = 'B'.repeat(21) + 'A';
describe('Go-only file drag hint', () => {
  it('retains a failed ACK for bounded focus recovery without a polling loop', async () => {
    let receive: (hint: unknown) => void = () => {};
    const take = vi
      .fn()
      .mockRejectedValueOnce(new Error('Temporary bridge failure'))
      .mockResolvedValue({ active: false, revision: 3 });
    const drag = createFileDrag((callback) => {
      receive = callback;
      return () => {};
    }, take);
    drag.start();
    drag.select(selected, true);
    receive({ active: true, revision: 2 });
    await vi.waitFor(() => expect(take).toHaveBeenCalledTimes(1));
    expect(get(drag.state)).toBe(false);
    await drag.ready();
    expect(take.mock.calls.map((args) => args[0])).toEqual([2, 2]);
    expect(get(drag.state)).toBe(false);
    await drag.ready();
    expect(take).toHaveBeenLastCalledWith(0);
    drag.stop();
  });
  it('accepts only bounded scalar hints and permits zero only for an empty authoritative snapshot', () => {
    expect(validateFileDrag({ active: true, revision: 1 })).toEqual({
      active: true,
      revision: 1,
    });
    expect(validateFileDrag({ active: false, revision: 0 }, true)).toEqual({
      active: false,
      revision: 0,
    });
    for (const hint of [
      null,
      [],
      { active: 'true', revision: 1 },
      { active: true, revision: 0 },
      { active: true, revision: Infinity },
      { active: true, revision: 1, path: '/private' },
    ])
      expect(validateFileDrag(hint)).toBeNull();
  });
  it('reads authoritative state, gates eligible contacts and acknowledges stale delivered revisions', async () => {
    let receive: (hint: unknown) => void = () => {};
    let latest = { active: false, revision: 0 };
    const take = vi.fn(async () => latest),
      unsubscribe = vi.fn();
    const drag = createFileDrag((callback) => {
      receive = callback;
      return unsubscribe;
    }, take);
    drag.start();
    await drag.ready();
    latest = { active: true, revision: 1 };
    receive(latest);
    await vi.waitFor(() => expect(take).toHaveBeenCalledWith(1));
    expect(get(drag.state)).toBe(false);
    drag.select(selected, true);
    latest = { active: true, revision: 2 };
    receive(latest);
    await vi.waitFor(() => expect(get(drag.state)).toBe(true));
    latest = { active: false, revision: 3 };
    receive(latest);
    await vi.waitFor(() => expect(get(drag.state)).toBe(false));
    receive({ active: true, revision: 2 });
    await vi.waitFor(() => expect(take).toHaveBeenLastCalledWith(2));
    expect(get(drag.state)).toBe(false);
    latest = { active: true, revision: 4 };
    receive(latest);
    await vi.waitFor(() => expect(get(drag.state)).toBe(true));
    drag.select('', false);
    expect(get(drag.state)).toBe(false);
    drag.stop();
    receive({ active: true, revision: 5 });
    expect(get(drag.state)).toBe(false);
    expect(unsubscribe).toHaveBeenCalledOnce();
  });
  it('ACKs a notification arriving during startup read and ignores a read completing after stop', async () => {
    let receive: (hint: unknown) => void = () => {};
    let release!: (value: unknown) => void;
    const held = new Promise<unknown>((done) => {
      release = done;
    });
    const take = vi
      .fn()
      .mockReturnValueOnce(held)
      .mockResolvedValue({ active: true, revision: 2 });
    const drag = createFileDrag((callback) => {
      receive = callback;
      return () => {};
    }, take);
    drag.start();
    drag.select(selected, true);
    const ready = drag.ready();
    receive({ active: true, revision: 2 });
    release({ active: false, revision: 1 });
    await ready;
    await vi.waitFor(() => expect(get(drag.state)).toBe(true));
    expect(take.mock.calls.map((args) => args[0])).toEqual([0, 2]);
    let finish!: (value: unknown) => void;
    take.mockReturnValueOnce(
      new Promise((done) => {
        finish = done;
      }),
    );
    receive({ active: true, revision: 3 });
    drag.stop();
    finish({ active: true, revision: 3 });
    await Promise.resolve();
    expect(get(drag.state)).toBe(false);
  });
});
