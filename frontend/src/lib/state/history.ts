import { writable } from 'svelte/store';
import { hasDesktopBridge } from '../backend';
import { validateConversation, type ConversationSnapshot } from '../messaging';

const id = /^[A-Za-z0-9_-]{21}[AQgw]$/;
export function createHistoryActions() {
  const state = writable({ busy: false, notice: '' });
  let generation = 0;
  let busy = false;
  function reset() {
    generation += 1;
    busy = false;
    state.set({ busy: false, notice: '' });
  }
  async function change(
    contactID: string,
    messageID?: string,
  ): Promise<ConversationSnapshot | null> {
    if (
      busy ||
      !hasDesktopBridge() ||
      !id.test(contactID) ||
      contactID === 'A'.repeat(22) ||
      (messageID !== undefined && !id.test(messageID))
    )
      return null;
    const current = ++generation;
    busy = true;
    state.set({ busy: true, notice: '' });
    try {
      const service =
        await import('../../../bindings/whatsup/internal/desktop/service');
      const value =
        messageID === undefined
          ? await service.ClearHistory(contactID)
          : await service.DeleteMessage(contactID, messageID);
      if (current !== generation) return null;
      const result = validateConversation(value);
      if (result.contactID !== contactID) throw new Error('Contact changed');
      return result;
    } catch {
      if (current === generation)
        state.set({
          busy: false,
          notice:
            'The local history change could not be confirmed. Your contact has not been removed.',
        });
      return null;
    } finally {
      if (current === generation) {
        busy = false;
        state.update((value) => ({ ...value, busy: false }));
      }
    }
  }
  return {
    state,
    reset,
    clear: (contactID: string) => change(contactID),
    remove: (contactID: string, messageID: string) =>
      change(contactID, messageID),
  };
}
