import type { Page } from '@playwright/test';
import type { ConversationSnapshot } from '../src/lib/messaging';

export function contactSummary(value: ConversationSnapshot) {
  const { messages: _messages, ...fields } = value;
  return { ...fields, unread: 0 };
}

export function messagePage(value: ConversationSnapshot) {
  return {
    contactID: value.contactID,
    revision: value.revision,
    messages: value.messages,
    nextCursor: '',
    hasMore: false,
  };
}

export async function contactHint(
  page: Page,
  contactID: string,
  revision: number,
) {
  await page.evaluate(
    ({ contactID, revision }) => {
      const host = window as Window & {
        _wails?: { dispatchWailsEvent?: (event: unknown) => void };
      };
      host._wails?.dispatchWailsEvent?.({
        name: 'whatsup:contact-changed',
        data: { contactID, revision },
      });
    },
    { contactID, revision },
  );
}

// Updating the synthetic backend emits the same hint as a native durable
// mutation. Tests still exercise generated HTTP bindings for authoritative data.
export function eventBackedState<T extends { conversation: unknown }>(
  page: Page,
  initial: T,
): T {
  return new Proxy(initial, {
    set(target, key, value) {
      Reflect.set(target, key, value);
      if (
        page.url() !== 'about:blank' &&
        (key === 'conversation' || (key === 'holdPoll' && value))
      ) {
        const current = target.conversation as ConversationSnapshot;
        const id = current.contactID || 'AQEBAQEBAQEBAQEBAQEBAQ';
        void contactHint(page, id, current.revision + 1).catch(() => {});
      }
      return true;
    },
  });
}
