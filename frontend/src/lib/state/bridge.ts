import { Events } from '@wailsio/runtime';
import { hasDesktopBridge } from '../backend';
import {
  connectContact,
  forgetContact,
  sendMessage,
  validateConversation,
  type ConversationMessage,
  type ConversationSnapshot,
} from '../messaging';

export interface ContactSummary extends Omit<ConversationSnapshot, 'messages'> {
  unread: number;
}

export interface MessagePage {
  contactID: string;
  revision: number;
  messages: ConversationMessage[];
  nextCursor: string;
  hasMore: boolean;
}

export interface ContactHint {
  contactID: string;
  revision: number;
}

export const validMessageID = (value: unknown): value is string =>
  typeof value === 'string' && /^[A-Za-z0-9_-]{21}[AQgw]$/.test(value);

export const validID = (value: unknown): value is string =>
  validMessageID(value) && value !== 'A'.repeat(22);

function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value))
    throw new Error('The desktop returned invalid contact data.');
  return value as Record<string, unknown>;
}

function revision(value: unknown): value is number {
  return Number.isSafeInteger(value) && (value as number) >= 0;
}

export function validateSummary(value: unknown): ContactSummary {
  const record = object(value);
  if (
    Object.keys(record).length !== 9 ||
    !revision(record.unread) ||
    !validID(record.contactID)
  )
    throw new Error('The desktop returned invalid contact data.');
  const { unread, ...fields } = record;
  const { messages: _messages, ...summary } = validateConversation({
    ...fields,
    messages: [],
  });
  return { ...summary, unread: unread as number };
}

export function validateContacts(value: unknown): ContactSummary[] {
  if (!Array.isArray(value) || value.length > 32)
    throw new Error('The desktop returned invalid contact data.');
  const contacts = value.map(validateSummary);
  if (
    new Set(contacts.map((contact) => contact.contactID)).size !==
    contacts.length
  )
    throw new Error('The desktop returned invalid contact data.');
  return contacts;
}

export function validatePage(value: unknown, contactID: string): MessagePage {
  const record = object(value);
  if (
    Object.keys(record).length !== 5 ||
    record.contactID !== contactID ||
    !validID(contactID) ||
    !revision(record.revision) ||
    typeof record.hasMore !== 'boolean' ||
    typeof record.nextCursor !== 'string' ||
    (record.nextCursor !== '' &&
      (!/^[1-9][0-9]{0,18}$/.test(record.nextCursor) ||
        BigInt(record.nextCursor) > 9223372036854775807n)) ||
    record.hasMore !== (record.nextCursor !== '')
  )
    throw new Error('The desktop returned an invalid message page.');
  // Reuse the same strict message validation as mutations. Paging metadata
  // never enters the message DTO and no transport credentials reach this layer.
  const { messages } = validateConversation({
    state: 'offline',
    revision: record.revision,
    contactID,
    peerFingerprint: 'f'.repeat(64),
    peerDisplayName: '',
    localName: '',
    canConnect: true,
    canSend: true,
    messages: record.messages,
  });
  return {
    contactID,
    revision: record.revision as number,
    messages,
    nextCursor: record.nextCursor,
    hasMore: record.hasMore,
  };
}

export function validateHint(value: unknown): ContactHint | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  return Object.keys(record).length === 2 &&
    validID(record.contactID) &&
    revision(record.revision)
    ? { contactID: record.contactID, revision: record.revision }
    : null;
}

// The generated service is replaced by the pinned generator when Go contracts
// change. Keeping its use here leaves UI components independent of its shape.
async function service() {
  if (!hasDesktopBridge())
    throw new Error('Open the desktop app to use contacts.');
  return import('../../../bindings/whatsup/internal/desktop/service');
}

export interface ContactsBridge {
  contacts(): Promise<ContactSummary[]>;
  page(contactID: string, cursor: string, limit: number): Promise<MessagePage>;
  markRead(contactID: string, messageID: string): Promise<ContactSummary>;
  connect(contactID: string): Promise<ConversationSnapshot>;
  send(contactID: string, text: string): Promise<ConversationSnapshot>;
  forget(contactID: string): Promise<ConversationSnapshot>;
  subscribe(callback: (hint: unknown) => void): () => void;
}

export const contactsBridge: ContactsBridge = {
  async contacts() {
    return validateContacts(await (await service()).GetContacts());
  },
  async page(contactID, cursor, limit) {
    if (
      !validID(contactID) ||
      !Number.isInteger(limit) ||
      limit < 1 ||
      limit > 100
    )
      throw new Error('This conversation is no longer available.');
    return validatePage(
      await (await service()).GetMessages(contactID, cursor, limit),
      contactID,
    );
  },
  async markRead(contactID, messageID) {
    if (!validID(contactID) || !validMessageID(messageID))
      throw new Error('This conversation is no longer available.');
    const summary = validateSummary(
      await (await service()).MarkRead(contactID, messageID),
    );
    if (summary.contactID !== contactID)
      throw new Error('The contact changed.');
    return summary;
  },
  connect: connectContact,
  send: sendMessage,
  forget: forgetContact,
  subscribe(callback) {
    return Events.On('whatsup:contact-changed', (event) =>
      callback(event.data),
    );
  },
};
