import { describe, expect, it } from 'vitest';
import {
  unavailableConversation,
  forgetContact,
  validateConversation,
  validMessageText,
  type ConversationSnapshot,
} from './messaging';

const contactID = 'AQEBAQEBAQEBAQEBAQEBAQ';
const id = 'AgICAgICAgICAgICAgICAg';

function approved(): ConversationSnapshot {
  return {
    state: 'offline',
    revision: 1,
    contactID,
    peerFingerprint: 'b'.repeat(64),
    peerDisplayName: '',
    localName: '',
    canConnect: true,
    canSend: true,
    messages: [
      {
        id,
        direction: 'outgoing',
        text: 'Synthetic queued text',
        createdAt: '2026-11-14T12:00:00Z',
        delivered: false,
      },
    ],
  };
}

describe('public conversation boundary', () => {
  it('rejects invalid forget targets before native access and fails closed in a browser preview', async () => {
    for (const invalid of [
      '',
      'A'.repeat(22),
      '../another-contact',
      `${contactID}extra`,
    ])
      await expect(forgetContact(invalid)).rejects.toThrow(
        'This contact is no longer available.',
      );
    await expect(forgetContact(contactID)).rejects.toThrow(
      'Contact removal could not be confirmed.',
    );
  });

  it('keeps offline local approval independent from a live connection', () => {
    const source = approved();
    const value = validateConversation(source);
    expect(value.state).toBe('offline');
    expect(value.canSend).toBe(true);
    expect(value.messages[0].delivered).toBe(false);
    source.messages[0].text = 'Changed input';
    expect(value.messages[0].text).toBe('Synthetic queued text');
    expect(
      validateConversation({
        ...approved(),
        state: 'identity-changed',
        canConnect: false,
      }).canSend,
    ).toBe(true);
  });

  it('rejects accidental secrets, malformed contact bindings and unsafe revisions', () => {
    const source = approved();
    for (const invalid of [
      { ...source, presharedKey: 'SYNTHETIC-PRIVATE-VALUE' },
      { ...source, contactID: 'A'.repeat(22) },
      { ...source, peerFingerprint: 'SYNTHETIC-NOT-A-FINGERPRINT' },
      { ...source, revision: Number.MAX_SAFE_INTEGER + 1 },
      { ...source, revision: 1.5 },
      { ...source, revision: -1 },
      {
        ...source,
        messages: [{ ...source.messages[0], privateKey: 'hidden' }],
      },
      { ...unavailableConversation, contactID, canSend: true },
    ])
      expect(() => validateConversation(invalid)).toThrow(
        'The desktop returned an invalid conversation.',
      );
  });

  it('rejects duplicate IDs and oversized or unsafe display text', () => {
    const source = approved();
    for (const messages of [
      [source.messages[0], source.messages[0]],
      [{ ...source.messages[0], text: 'x'.repeat(16 * 1024 + 1) }],
      [{ ...source.messages[0], text: 'Spoof\u202einterface' }],
      [{ ...source.messages[0], text: 'Bad\u0000control' }],
      [{ ...source.messages[0], text: '\ud800' }],
    ])
      expect(() => validateConversation({ ...source, messages })).toThrow();
    expect(
      validateConversation({
        ...source,
        messages: [
          {
            ...source.messages[0],
            text: '<strong>inert HTML</strong>\nRTL: مرحبا',
          },
        ],
      }).messages[0].text,
    ).toBe('<strong>inert HTML</strong>\nRTL: مرحبا');
  });

  it('bounds send text by UTF-8 bytes while preserving ordinary multiline content', () => {
    expect(validMessageText(' \n\t')).toBe(false);
    expect(validMessageText('x'.repeat(16 * 1024))).toBe(true);
    expect(validMessageText('é'.repeat(8 * 1024 + 1))).toBe(false);
    expect(validMessageText('First line\nSecond line\tالعربية')).toBe(true);
    expect(validMessageText('Spoof\u202einterface')).toBe(false);
  });
});
