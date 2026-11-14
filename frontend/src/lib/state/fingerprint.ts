export function groupedFingerprint(value: string): string {
  return /^[0-9a-f]{64}$/.test(value) ? value.match(/.{4}/g)!.join(' ') : '';
}
