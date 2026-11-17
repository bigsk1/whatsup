import { afterEach, describe, expect, it, vi } from 'vitest';
import { Clipboard } from '@wailsio/runtime';
import { copyText } from './clipboard';

vi.mock('@wailsio/runtime', () => ({
  Clipboard: { SetText: vi.fn(async () => {}) },
}));

afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

describe('explicit text copying', () => {
  it('uses the native clipboard without the browser API on Linux and Windows', async () => {
    const text = 'https://example.invalid/a?b=1&c=2\nUnicode: café 🐈';
    for (const OS of ['linux', 'windows']) {
      vi.stubGlobal('window', { _wails: { environment: { OS } } });
      vi.stubGlobal('navigator', { clipboard: undefined });
      await copyText(text);
    }
    expect(Clipboard.SetText).toHaveBeenCalledTimes(2);
    expect(Clipboard.SetText).toHaveBeenNthCalledWith(1, text);
    expect(Clipboard.SetText).toHaveBeenNthCalledWith(2, text);
  });

  it('uses the browser clipboard for a browser preview', async () => {
    vi.stubGlobal('window', {});
    const writeText = vi.fn(async () => {});
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    await copyText('Synthetic browser text');
    expect(writeText).toHaveBeenCalledWith('Synthetic browser text');
    expect(Clipboard.SetText).not.toHaveBeenCalled();
  });

  it('reports missing browser clipboard access instead of claiming success', async () => {
    vi.stubGlobal('window', {});
    vi.stubGlobal('navigator', { clipboard: undefined });
    await expect(copyText('Synthetic text')).rejects.toThrow(
      'Clipboard access is unavailable.',
    );
    expect(Clipboard.SetText).not.toHaveBeenCalled();
  });

  it('propagates a native failure without falling back to the browser clipboard', async () => {
    vi.stubGlobal('window', { _wails: { environment: { OS: 'linux' } } });
    const writeText = vi.fn(async () => {});
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    vi.mocked(Clipboard.SetText).mockRejectedValueOnce(
      new Error('Synthetic failure'),
    );
    await expect(copyText('Synthetic text')).rejects.toThrow(
      'Synthetic failure',
    );
    expect(writeText).not.toHaveBeenCalled();
  });
});
