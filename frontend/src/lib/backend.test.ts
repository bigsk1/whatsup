import { describe, expect, it } from 'vitest';
import {
  hasDesktopBridge,
  requestStatus,
  unavailableStatus,
  validateStatus,
} from './backend';

describe('desktop status boundary', () => {
  it('keeps browser previews disconnected without importing the native bridge', () => {
    expect(hasDesktopBridge()).toBe(false);
    expect(unavailableStatus).toMatchObject({
      networkState: 'unavailable',
      directOnlyAvailable: false,
      pairingAvailable: false,
      messagingAvailable: false,
      attachmentsAvailable: false,
    });
  });

  it('rejects malformed status rather than interpreting strings as capabilities', () => {
    for (const value of [
      null,
      {},
      [],
      { ...unavailableStatus, messagingAvailable: 'true' },
      { ...unavailableStatus, requiresPassphrase: 'false' },
      { ...unavailableStatus, identityFingerprint: 'x'.repeat(257) },
    ]) {
      expect(() => validateStatus(value)).toThrow('invalid status');
    }
  });

  it('selects public fields and drops unexpected backend data', () => {
    const status = validateStatus({
      ...unavailableStatus,
      privateKey: 'synthetic-not-a-real-key',
      endpoint: 'synthetic-only',
    });
    expect(status).toEqual(unavailableStatus);
    expect(status).not.toHaveProperty('privateKey');
    expect(status).not.toHaveProperty('endpoint');
  });

  it('requests the exact typed operation and validates its response', async () => {
    const calls: string[] = [];
    const status = await requestStatus('UnlockStorage', async (method) => {
      calls.push(method);
      return {
        ...unavailableStatus,
        storageState: 'unlocked',
        identityFingerprint: 'synthetic-fingerprint',
      };
    });
    expect(calls).toEqual(['UnlockStorage']);
    expect(status.storageState).toBe('unlocked');
    await expect(
      requestStatus('GetStatus', async () => ({ messagingAvailable: true })),
    ).rejects.toThrow('invalid status');
  });
});
