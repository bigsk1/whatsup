import { describe, expect, it } from 'vitest';
import { defaults, parsePreferences } from './preferences';

describe('appearance preferences', () => {
  it('handles corrupt, missing, oversized, and unsupported preferences', () => {
    for (const raw of [
      null,
      '',
      '{',
      'null',
      '"dark"',
      '0',
      'x'.repeat(300),
      '{"theme":"<script>","accent":"url(evil)"}',
    ]) {
      expect(parsePreferences(raw)).toEqual(defaults);
    }
  });

  it('accepts only supported theme and accent values', () => {
    expect(parsePreferences('{"theme":"system","accent":"rose"}')).toEqual({
      theme: 'system',
      accent: 'rose',
    });
    expect(parsePreferences('{"theme":"light","accent":"unknown"}')).toEqual({
      theme: 'light',
      accent: 'teal',
    });
  });

  it('ignores unrelated stored data and returns a fresh preference object', () => {
    expect(
      parsePreferences('{"theme":"dark","accent":"blue","secret":"synthetic"}'),
    ).toEqual({ theme: 'dark', accent: 'blue' });
    const parsed = parsePreferences(null);
    parsed.theme = 'light';
    expect(parsePreferences(null)).toEqual({ theme: 'dark', accent: 'teal' });
  });
});
