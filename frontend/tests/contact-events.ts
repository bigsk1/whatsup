import { readFileSync } from 'node:fs';
import type { Page, Route } from '@playwright/test';
import type { ConversationSnapshot } from '../src/lib/messaging';
import { defaultAppPreferences } from '../src/lib/state/preferences';

const generated = readFileSync(
  new URL('../bindings/whatsup/internal/desktop/service.ts', import.meta.url),
  'utf8',
);
const preferenceBinding = generated.match(
  /export function GetAppPreferences\([\s\S]*?\$Call\.ByID\((\d+)/,
);
if (!preferenceBinding)
  throw new Error('Missing generated app preferences binding');
const preferencesID = Number(preferenceBinding[1]);
const nativeGenerated = readFileSync(
  new URL('../bindings/whatsup/nativelifecycle.ts', import.meta.url),
  'utf8',
);
export const notificationActivationID = Number(
  nativeGenerated.match(
    /export function TakeNotificationActivation\([\s\S]*?\$Call\.ByID\((\d+)/,
  )?.[1],
);
const securityID = Number(
  generated.match(
    /export function GetStorageSecurity\([\s\S]*?\$Call\.ByID\((\d+)/,
  )?.[1],
);
const relayID = Number(
  generated.match(
    /export function GetRelayChange\([\s\S]*?\$Call\.ByID\((\d+)/,
  )?.[1],
);
export async function serveDefaultRelayChange(route: Route): Promise<boolean> {
  const body = route.request().postDataJSON();
  if (body.args?.methodID !== relayID) return false;
  const contactID = body.args.args[0];
  await route.fulfill({
    json: {
      contactID,
      revision: 0,
      requestID: '',
      state: 'idle',
      currentServiceURL: 'https://relay.example/map.json',
      proposedServiceURL: '',
      proposedHost: '',
      localApproved: false,
      peerApproved: false,
      canPropose: true,
      canApprove: false,
    },
  });
  return true;
}

export async function serveDefaultAppPreferences(
  route: Route,
): Promise<boolean> {
  if (
    route.request().postDataJSON().args?.methodID === notificationActivationID
  ) {
    await route.fulfill({ json: '' });
    return true;
  }
  if (await serveDefaultRelayChange(route)) return true;
  if (route.request().postDataJSON().args?.methodID === securityID) {
    await route.fulfill({
      json: { requiresPassphrase: false, recoveryAvailable: true, busy: false },
    });
    return true;
  }
  if (route.request().postDataJSON().args?.methodID !== preferencesID)
    return false;
  await route.fulfill({ json: defaultAppPreferences });
  return true;
}

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

export async function nativeHint(page: Page, name: string, data: unknown) {
  await page.evaluate(
    ({ name, data }) => {
      const host = window as Window & {
        _wails?: { dispatchWailsEvent?: (event: unknown) => void };
      };
      host._wails?.dispatchWailsEvent?.({ name, data });
    },
    { name, data },
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
