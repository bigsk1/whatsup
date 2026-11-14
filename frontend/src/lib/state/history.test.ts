import { get } from 'svelte/store';
import { afterEach, expect, it, vi } from 'vitest';
import { unavailableConversation } from '../messaging';
import { createHistoryActions } from './history';

const bridge = vi.hoisted(() => ({
  ClearHistory: vi.fn(),
  DeleteMessage: vi.fn(),
}));
vi.mock('../../../bindings/whatsup/internal/desktop/service', () => bridge);
afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

it('ignores an in-flight history result after the runtime is locked or replaced', async () => {
  vi.stubGlobal('window', { _wails: { environment: {} } });
  let finish: (value: unknown) => void = () => {};
  bridge.ClearHistory.mockReturnValue(
    new Promise((resolve) => {
      finish = resolve;
    }),
  );
  const actions = createHistoryActions();
  const pending = actions.clear('AQEBAQEBAQEBAQEBAQEBAQ');
  await vi.waitFor(() => expect(bridge.ClearHistory).toHaveBeenCalledOnce());
  expect(get(actions.state).busy).toBe(true);
  actions.reset();
  finish({ ...unavailableConversation, state: 'none', revision: 1 });
  expect(await pending).toBeNull();
  expect(get(actions.state)).toEqual({ busy: false, notice: '' });
});

it('rejects invalid targets before desktop access and never displays raw bridge errors', async () => {
  vi.stubGlobal('window', { _wails: { environment: {} } });
  const actions = createHistoryActions();
  expect(await actions.clear('../other-contact')).toBeNull();
  expect(bridge.ClearHistory).not.toHaveBeenCalled();
  bridge.ClearHistory.mockRejectedValue(
    new Error('SYNTHETIC-PRIVATE-ENDPOINT'),
  );
  expect(await actions.clear('AQEBAQEBAQEBAQEBAQEBAQ')).toBeNull();
  expect(get(actions.state).notice).not.toContain('SYNTHETIC-PRIVATE-ENDPOINT');
  expect(get(actions.state).notice).toContain('could not be confirmed');
});
