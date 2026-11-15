import { describe, expect, it } from 'vitest';
import { paletteCatalog, paletteIDs, skinCatalog, skins } from './catalog';
import {
  activePalette,
  defaults,
  migrateLegacyPreferences,
  parsePreferences,
} from './preferences';

describe('appearance preferences', () => {
  it('handles corrupt, missing, oversized, and unsupported preferences', () => {
    for (const raw of [
      null,
      '',
      '{',
      'null',
      '[]',
      '"dark"',
      '0',
      'x'.repeat(600),
      '{"skin":"<script>","palette":"url(evil)","textSize":"9999px"}',
    ]) {
      expect(parsePreferences(raw)).toEqual(defaults);
    }
  });

  it('accepts only catalog values and falls back field by field', () => {
    expect(
      parsePreferences(
        '{"skin":"arcade","palette":"gameboy","lightPalette":"github-light","followSystem":true,"textSize":"large","effects":false}',
      ),
    ).toEqual({
      skin: 'arcade',
      palette: 'gameboy',
      lightPalette: 'github-light',
      followSystem: true,
      textSize: 'large',
      effects: false,
    });
    expect(
      parsePreferences('{"skin":"neon","palette":"unknown","effects":"yes"}'),
    ).toEqual({ ...defaults, skin: 'neon' });
  });

  it('ignores unrelated stored data and returns a fresh preference object', () => {
    expect(
      parsePreferences('{"skin":"terminal","secret":"synthetic"}'),
    ).toEqual({ ...defaults, skin: 'terminal' });
    const parsed = parsePreferences(null);
    parsed.skin = 'arcade';
    expect(parsePreferences(null)).toEqual(defaults);
  });

  it('migrates the previous light, dark and system appearance', () => {
    expect(
      migrateLegacyPreferences('{"theme":"light","accent":"rose"}'),
    ).toEqual({ ...defaults, palette: 'daybreak' });
    expect(migrateLegacyPreferences('{"theme":"system"}')).toEqual({
      ...defaults,
      followSystem: true,
    });
    expect(migrateLegacyPreferences('{"theme":"dark"}')).toEqual(defaults);
    expect(migrateLegacyPreferences('{bad')).toEqual(defaults);
  });

  it('follows the system only when requested', () => {
    const follow = { ...defaults, followSystem: true };
    expect(activePalette(follow, false)).toBe('daybreak');
    expect(activePalette(follow, true)).toBe('whatsup');
    expect(activePalette(defaults, false)).toBe('whatsup');
  });

  it('keeps the catalog complete, unique and safe for CSS', () => {
    expect(new Set(paletteCatalog.map((palette) => palette.id))).toEqual(
      new Set(paletteIDs),
    );
    expect(skinCatalog.map((skin) => skin.id)).toEqual([...skins]);
    for (const palette of paletteCatalog) {
      const { avatars, ...colors } = palette.colors;
      for (const value of [...Object.values(colors), ...avatars])
        expect(value).toMatch(/^#[0-9a-f]{6}$/);
    }
  });
});
