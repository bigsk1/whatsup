import { hasDesktopBridge } from './backend';
import { validDisplayName } from './names';

export const conversationStates = [
  'unavailable',
  'none',
  'offline',
  'connecting',
  'connected',
  'failed',
  'identity-changed',
  'authentication-failed',
  'update-required',
  'capacity-reached',
  'peer-capacity-reached',
] as const;

export type ConversationState = (typeof conversationStates)[number];

export interface ConversationMessage {
  id: string;
  direction: 'incoming' | 'outgoing';
  text: string;
  createdAt: string;
  delivered: boolean;
}

export interface ConversationSnapshot {
  state: ConversationState;
  revision: number;
  contactID: string;
  peerFingerprint: string;
  peerDisplayName: string;
  localName: string;
  canConnect: boolean;
  canSend: boolean;
  messages: ConversationMessage[];
}

export const unavailableConversation: ConversationSnapshot = Object.freeze({
  state: 'unavailable',
  revision: 0,
  contactID: '',
  peerFingerprint: '',
  peerDisplayName: '',
  localName: '',
  canConnect: false,
  canSend: false,
  messages: [],
});

const idPattern = /^[A-Za-z0-9_-]{21}[AQgw]$/;
const fingerprintPattern = /^[0-9a-f]{64}$/;
const timestamp =
  /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/;
const encoder = new TextEncoder();
const maximumText = 16 * 1024;
const maximumStoredText = 256 * 1024;
// Unicode 18.0 RGI emoji tag sequences. Keep this exact allowlist aligned with Go;
// a black flag followed by arbitrary tag letters could still hide ASCII text.
const emojiTagFlags = [
  '\u{1f3f4}\u{e0067}\u{e0062}\u{e0065}\u{e006e}\u{e0067}\u{e007f}', // England
  '\u{1f3f4}\u{e0067}\u{e0062}\u{e0073}\u{e0063}\u{e0074}\u{e007f}', // Scotland
  '\u{1f3f4}\u{e0067}\u{e0062}\u{e0077}\u{e006c}\u{e0073}\u{e007f}', // Wales
];

function exactFields(value: Record<string, unknown>, names: string[]): boolean {
  return (
    Object.keys(value).length === names.length &&
    names.every((name) => Object.hasOwn(value, name))
  );
}

function displayText(value: string): boolean {
  for (let index = 0; index < value.length;) {
    const point = value.codePointAt(index)!;
    if (point === 0x1f3f4) {
      const flag = emojiTagFlags.find((candidate) =>
        value.startsWith(candidate, index),
      );
      if (flag) {
        index += flag.length;
        continue;
      }
    }
    const character = String.fromCodePoint(point);
    index += character.length;
    // Match Go's explicit shaping/hyphenation exceptions. The message
    // element chooses its own direction and cannot change surrounding UI text.
    if (point === 0x00ad || point === 0x200c || point === 0x200d) continue;
    if (
      (point >= 0xd800 && point <= 0xdfff) ||
      (character !== '\n' &&
        character !== '\t' &&
        /[\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/u.test(character)) ||
      [0x034f, 0x115f, 0x1160, 0x17b4, 0x17b5, 0x2065, 0x3164, 0xffa0].includes(
        point,
      ) ||
      (point >= 0xfff0 && point <= 0xfff8) ||
      (point >= 0xe0000 && point <= 0xe001f) ||
      (point >= 0xe0080 && point <= 0xe00ff) ||
      (point >= 0xe01f0 && point <= 0xe0fff)
    )
      return false;
  }
  return true;
}

export function validMessageText(value: string): boolean {
  return (
    value.trim().length > 0 &&
    encoder.encode(value).length <= maximumText &&
    displayText(value)
  );
}

export function validateConversation(value: unknown): ConversationSnapshot {
  const invalid = () => {
    throw new Error('The desktop returned an invalid conversation.');
  };
  if (!value || typeof value !== 'object' || Array.isArray(value))
    return invalid();
  const record = value as Record<string, unknown>;
  if (
    !exactFields(record, [
      'state',
      'revision',
      'contactID',
      'peerFingerprint',
      'peerDisplayName',
      'localName',
      'canConnect',
      'canSend',
      'messages',
    ]) ||
    !conversationStates.includes(record.state as ConversationState) ||
    !Number.isSafeInteger(record.revision) ||
    (record.revision as number) < 0 ||
    typeof record.contactID !== 'string' ||
    typeof record.peerFingerprint !== 'string' ||
    typeof record.peerDisplayName !== 'string' ||
    typeof record.localName !== 'string' ||
    !validDisplayName(record.peerDisplayName) ||
    !validDisplayName(record.localName) ||
    typeof record.canConnect !== 'boolean' ||
    typeof record.canSend !== 'boolean' ||
    !Array.isArray(record.messages) ||
    record.messages.length > 256
  )
    return invalid();
  const hasContact = !['unavailable', 'none'].includes(record.state as string);
  if (
    (hasContact &&
      (!idPattern.test(record.contactID) ||
        record.contactID === 'A'.repeat(22) ||
        !fingerprintPattern.test(record.peerFingerprint))) ||
    (!hasContact &&
      (record.contactID !== '' ||
        record.peerFingerprint !== '' ||
        record.peerDisplayName !== '' ||
        record.localName !== '' ||
        record.canConnect ||
        record.canSend ||
        record.messages.length !== 0))
  )
    return invalid();

  const ids = new Set<string>();
  let storedBytes = 0;
  const messages: ConversationMessage[] = [];
  for (const item of record.messages) {
    if (!item || typeof item !== 'object' || Array.isArray(item))
      return invalid();
    const message = item as Record<string, unknown>;
    if (
      !exactFields(message, [
        'id',
        'direction',
        'text',
        'createdAt',
        'delivered',
      ]) ||
      typeof message.id !== 'string' ||
      !idPattern.test(message.id) ||
      ids.has(message.id) ||
      !['incoming', 'outgoing'].includes(message.direction as string) ||
      typeof message.text !== 'string' ||
      message.text.length === 0 ||
      encoder.encode(message.text).length > maximumText ||
      !displayText(message.text) ||
      typeof message.createdAt !== 'string' ||
      message.createdAt.length > 64 ||
      !timestamp.test(message.createdAt) ||
      !Number.isFinite(Date.parse(message.createdAt)) ||
      typeof message.delivered !== 'boolean'
    )
      return invalid();
    storedBytes += encoder.encode(message.text).length;
    if (storedBytes > maximumStoredText) return invalid();
    ids.add(message.id);
    messages.push({
      id: message.id,
      direction: message.direction as 'incoming' | 'outgoing',
      text: message.text,
      createdAt: message.createdAt,
      delivered: message.delivered,
    });
  }
  return {
    state: record.state as ConversationState,
    revision: record.revision as number,
    contactID: record.contactID,
    peerFingerprint: record.peerFingerprint,
    peerDisplayName: record.peerDisplayName,
    localName: record.localName,
    canConnect: record.canConnect,
    canSend: record.canSend,
    messages,
  };
}

async function service() {
  if (!hasDesktopBridge())
    throw new Error('Open the desktop app to use messaging.');
  return import('../../bindings/whatsup/internal/desktop/service');
}

export async function requestConversation(): Promise<ConversationSnapshot> {
  try {
    return validateConversation(await (await service()).GetConversation());
  } catch {
    throw new Error('Conversation status could not be verified.');
  }
}

export async function connectContact(
  contactID: string,
): Promise<ConversationSnapshot> {
  if (!idPattern.test(contactID))
    throw new Error('This contact is no longer available.');
  try {
    const result = validateConversation(
      await (await service()).ConnectContact(contactID),
    );
    if (result.contactID !== contactID)
      throw new Error('The contact changed while connecting.');
    return result;
  } catch {
    throw new Error('The contact could not be connected.');
  }
}

export async function forgetContact(
  contactID: string,
): Promise<ConversationSnapshot> {
  if (!idPattern.test(contactID) || contactID === 'A'.repeat(22))
    throw new Error('This contact is no longer available.');
  try {
    const result = validateConversation(
      await (await service()).ForgetContact(contactID),
    );
    if (result.state !== 'none')
      throw new Error('Contact removal could not be confirmed.');
    return result;
  } catch {
    throw new Error('Contact removal could not be confirmed.');
  }
}

export async function sendMessage(
  contactID: string,
  text: string,
): Promise<ConversationSnapshot> {
  if (!idPattern.test(contactID) || !validMessageText(text))
    throw new Error('Enter a message within the supported text limit.');
  try {
    const result = validateConversation(
      await (await service()).SendMessage(contactID, text),
    );
    if (result.contactID !== contactID)
      throw new Error('The contact changed while saving the message.');
    return result;
  } catch {
    throw new Error('The message could not be saved to the local queue.');
  }
}
