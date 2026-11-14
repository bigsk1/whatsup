import { describe, expect, it } from 'vitest';
import {
  saveContactName,
  saveProfile,
  validateProfile,
  validDisplayName,
} from './profile';

describe('private profile boundary', () => {
  it('accepts bounded visible names and an explicitly cleared name', () => {
    for (const name of [
      '',
      'Alex',
      'Zoë',
      'مرحبا',
      'Cafe\u0301',
      'x'.repeat(64),
    ])
      expect(validDisplayName(name)).toBe(true);
    for (const name of [
      ' leading',
      'trailing ',
      'x'.repeat(65),
      'Line\nbreak',
      'Tab\tname',
      'Hidden\u202ename',
      'Zero\u200bwidth',
      '\u034f',
      '\u115f',
      '\u3164',
      '\ud800',
    ])
      expect(validDisplayName(name)).toBe(false);
  });
  it('accepts only the minimal profile DTO and copies its values', () => {
    const source = { displayName: 'Synthetic name', shareDisplayName: false };
    const result = validateProfile(source);
    source.displayName = 'Changed';
    expect(result.displayName).toBe('Synthetic name');
    for (const value of [
      null,
      [],
      { displayName: 'Alex' },
      { ...source, shareDisplayName: 'true' },
      { ...source, privateKey: 'SYNTHETIC' },
      { ...source, displayName: 'Bad\u202ename' },
    ])
      expect(() => validateProfile(value)).toThrow(
        'The desktop returned an invalid profile.',
      );
  });
  it('rejects invalid inputs and unavailable native access without exposing raw errors', async () => {
    await expect(
      saveProfile({ displayName: 'Bad\nname', shareDisplayName: false }),
    ).rejects.toThrow('The desktop returned an invalid profile.');
    await expect(
      saveProfile({ displayName: 'Synthetic', shareDisplayName: false }),
    ).rejects.toThrow('Your profile could not be saved.');
    await expect(
      saveContactName('../other-contact', 'Synthetic'),
    ).rejects.toThrow('Enter a name within the supported limit.');
  });
});
