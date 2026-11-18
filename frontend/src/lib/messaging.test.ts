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
    peerProtocol: 0,
    attachmentsSupported: false,
    contactControlsSupported: false,
    state: 'offline',
    path: 'Unknown',
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
  it('validates protocol capabilities without granting features to legacy peers and keeps removed contact history', () => {
    const value = approved();
    for (const fields of [
      {
        peerProtocol: 3,
        attachmentsSupported: false,
        contactControlsSupported: false,
      },
      {
        peerProtocol: 4,
        attachmentsSupported: true,
        contactControlsSupported: false,
      },
      {
        peerProtocol: 4,
        attachmentsSupported: true,
        contactControlsSupported: true,
      },
    ])
      expect(
        validateConversation({ ...value, state: 'connected', ...fields })
          .peerProtocol,
      ).toBe(fields.peerProtocol);
    for (const fields of [
      { peerProtocol: 2 },
      { peerProtocol: 3, attachmentsSupported: true },
      { peerProtocol: 3, contactControlsSupported: true },
      {
        peerProtocol: 4,
        attachmentsSupported: false,
        contactControlsSupported: true,
      },
    ])
      expect(() => validateConversation({ ...value, ...fields })).toThrow();
    const removed = validateConversation({
      ...value,
      state: 'peer-removed',
      canConnect: false,
      canSend: false,
    });
    expect(removed.messages).toEqual(value.messages);
    expect(removed.peerFingerprint).toBe(value.peerFingerprint);
    expect(() => validateConversation({ ...removed, canSend: true })).toThrow();
    expect(() =>
      validateConversation({ ...removed, canConnect: true }),
    ).toThrow();
    const paused = validateConversation({ ...removed, state: 'paused' });
    expect(paused.messages).toEqual(value.messages);
    for (const fields of [
      { canSend: true },
      { canConnect: true },
      { peerProtocol: 4, attachmentsSupported: true },
    ])
      expect(() => validateConversation({ ...paused, ...fields })).toThrow();
  });
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

  it('preserves legitimate shaping, emoji tags and variation selectors in outgoing and received text', () => {
    for (const text of [
      'Family 👨\u200d👩\u200d👧\u200d👦 and ❤️\u200d🔥',
      'Flag 🏳️\u200d🌈 and 🏴\u{e0067}\u{e0062}\u{e0065}\u{e006e}\u{e0067}\u{e007f}',
      'Scotland 🏴\u{e0067}\u{e0062}\u{e0073}\u{e0063}\u{e0074}\u{e007f}',
      'Wales 🏴\u{e0067}\u{e0062}\u{e0077}\u{e006c}\u{e0073}\u{e007f}',
      'Bare black flag 🏴 without tags',
      'می\u200cروم',
      'क्\u200dष and क्\u200cष',
      'Copy co\u00adoperate',
      'Text \u2764\ufe0e and emoji \u2764\ufe0f',
    ]) {
      expect(validMessageText(text)).toBe(true);
      const source = approved();
      source.messages[0].text = text;
      expect(validateConversation(source).messages[0].text).toBe(text);
    }
  });

  it('rejects stray tags and hidden or malformed flag payloads at both boundaries', () => {
    const tags = (ascii: string) =>
      [...ascii]
        .map((character) =>
          String.fromCodePoint(0xe0000 + character.codePointAt(0)!),
        )
        .join('');
    const blackFlag = '\u{1f3f4}';
    const cancel = '\u{e007f}';
    const england = blackFlag + tags('gbeng') + cancel;
    const invalid = [
      tags('synthetic text') + cancel,
      blackFlag + tags('synthetic text') + cancel,
      tags('gbeng') + cancel,
      '😀' + tags('gbeng') + cancel,
      blackFlag + tags('gbeng'),
      blackFlag + cancel,
      blackFlag + tags('gbe') + cancel,
      blackFlag + tags('gbabc') + cancel,
      blackFlag + tags('GBENG') + cancel,
      blackFlag + tags('12345') + cancel,
      blackFlag + tags('gb') + 'x' + tags('eng') + cancel,
      blackFlag + tags('gb') + '\u200d' + tags('eng') + cancel,
      blackFlag + tags('gb') + blackFlag + tags('eng') + cancel,
      blackFlag + '\ufe0f' + tags('gbeng') + cancel,
      blackFlag + tags('gbengx') + cancel,
      england + tags('synthetic') + cancel,
      england + cancel,
      blackFlag + tags('a'.repeat(1000)) + cancel,
    ];
    for (let point = 0xe0020; point <= 0xe007f; point++)
      invalid.push(`visible${String.fromCodePoint(point)}text`);
    for (const text of invalid) {
      expect(validMessageText(text)).toBe(false);
      const source = approved();
      source.messages[0].text = text;
      expect(() => validateConversation(source)).toThrow();
    }
    for (const text of [
      england + england,
      `مرحبا ${england} hello`,
      'x'.repeat(16 * 1024 - new TextEncoder().encode(england).length) +
        england,
    ]) {
      expect(validMessageText(text)).toBe(true);
      const source = approved();
      source.messages[0].text = text;
      expect(validateConversation(source).messages[0].text).toBe(text);
    }
  });

  it('continues rejecting direction overrides, controls and invisible fillers at both boundaries', () => {
    for (const point of [
      0x0000, 0x001b, 0x0085, 0x009b, 0x034f, 0x061c, 0x115f, 0x1160, 0x17b4,
      0x17b5, 0x180e, 0x200b, 0x200e, 0x200f, 0x2028, 0x2029, 0x202a, 0x202b,
      0x202c, 0x202d, 0x202e, 0x2060, 0x2065, 0x2066, 0x2067, 0x2068, 0x2069,
      0x3164, 0xfeff, 0xffa0, 0xfff0, 0xe0000, 0xe0001, 0xe0080, 0xe01f0,
    ]) {
      const text = `Synthetic${String.fromCodePoint(point)}interface label`;
      expect(validMessageText(text)).toBe(false);
      const source = approved();
      source.messages[0].text = text;
      expect(() => validateConversation(source)).toThrow();
    }
  });
});
