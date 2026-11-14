const encoder = new TextEncoder();

export function validDisplayName(value: string): boolean {
  if (
    value.trim() !== value ||
    [...value].length > 64 ||
    encoder.encode(value).length > 256
  )
    return false;
  for (const character of value) {
    const point = character.codePointAt(0)!;
    if (
      (point >= 0xd800 && point <= 0xdfff) ||
      /[\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/u.test(character) ||
      [
        0x034f, 0x115f, 0x1160, 0x17b4, 0x17b5, 0x2065, 0x2800, 0x3164, 0xffa0,
      ].includes(point) ||
      (point >= 0xfff0 && point <= 0xfff8) ||
      (point >= 0xe0000 && point <= 0xe00ff) ||
      (point >= 0xe01f0 && point <= 0xe0fff)
    )
      return false;
  }
  return true;
}
