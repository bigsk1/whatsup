import { Clipboard } from '@wailsio/runtime';
import { hasDesktopBridge } from './backend';

export async function copyText(text: string): Promise<void> {
  if (hasDesktopBridge()) {
    await Clipboard.SetText(text);
    return;
  }
  if (typeof navigator === 'undefined' || !navigator.clipboard?.writeText)
    throw new Error('Clipboard access is unavailable.');
  await navigator.clipboard.writeText(text);
}
