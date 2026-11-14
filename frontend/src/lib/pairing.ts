import { hasDesktopBridge } from './backend';

export const pairingStates = [
  'unavailable',
  'idle',
  'connecting',
  'awaiting-approval',
  'waiting-for-peer',
  'saving',
  'pending',
  'denied',
  'expired',
  'failed',
  'canceled',
] as const;

export type PairingState = (typeof pairingStates)[number];

export interface PairingStatus {
  state: PairingState;
  requestID: string;
  localFingerprint: string;
  peerFingerprint: string;
  expiresAt: string;
}

export const unavailablePairing: PairingStatus = Object.freeze({
  state: 'unavailable',
  requestID: '',
  localFingerprint: '',
  peerFingerprint: '',
  expiresAt: '',
});

const fields = [
  'state',
  'requestID',
  'localFingerprint',
  'peerFingerprint',
  'expiresAt',
] as const;
const fingerprint = /^[0-9a-f]{64}$/;
// Sixteen random bytes use 22 raw base64url characters; the final four bits are 0.
const requestID = /^[A-Za-z0-9_-]{21}[AQgw]$/;
const timestamp =
  /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/;

export function hasPairingIdentity(status: PairingStatus): boolean {
  return [
    'awaiting-approval',
    'waiting-for-peer',
    'saving',
    'pending',
  ].includes(status.state);
}

export function validatePairingStatus(value: unknown): PairingStatus {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('The desktop returned an invalid pairing status.');
  }
  const record = value as Record<string, unknown>;
  if (
    Object.keys(record).length !== fields.length ||
    fields.some((field) => typeof record[field] !== 'string') ||
    !pairingStates.includes(record.state as PairingState)
  ) {
    throw new Error('The desktop returned an invalid pairing status.');
  }
  const status = Object.fromEntries(
    fields.map((field) => [field, record[field]]),
  ) as unknown as PairingStatus;
  const validExpiry =
    status.expiresAt.length <= 64 &&
    timestamp.test(status.expiresAt) &&
    Number.isFinite(Date.parse(status.expiresAt));
  if (
    (hasPairingIdentity(status) &&
      (!requestID.test(status.requestID) ||
        !fingerprint.test(status.localFingerprint) ||
        !fingerprint.test(status.peerFingerprint) ||
        status.localFingerprint === status.peerFingerprint ||
        (!validExpiry &&
          !(status.state === 'pending' && status.expiresAt === '')))) ||
    (!hasPairingIdentity(status) &&
      (status.requestID !== '' ||
        status.localFingerprint !== '' ||
        status.peerFingerprint !== '' ||
        (status.expiresAt !== '' && !validExpiry)))
  ) {
    throw new Error('The desktop returned an invalid pairing status.');
  }
  return status;
}

async function service() {
  if (!hasDesktopBridge())
    throw new Error('Open the desktop app to review pairing requests.');
  return import('../../bindings/whatsup/internal/desktop/service');
}

export async function requestPairingStatus(): Promise<PairingStatus> {
  try {
    return validatePairingStatus(await (await service()).GetPairingStatus());
  } catch {
    throw new Error('Pairing status could not be verified.');
  }
}

export async function approvePairing(
  selectedRequest: string,
  peerFingerprint: string,
): Promise<PairingStatus> {
  if (!requestID.test(selectedRequest) || !fingerprint.test(peerFingerprint))
    throw new Error('This pairing request is no longer available.');
  try {
    return validatePairingStatus(
      await (await service()).ApprovePairing(selectedRequest, peerFingerprint),
    );
  } catch {
    throw new Error('Pairing approval could not be confirmed.');
  }
}

export async function denyPairing(
  selectedRequest: string,
): Promise<PairingStatus> {
  if (!requestID.test(selectedRequest))
    throw new Error('This pairing request is no longer available.');
  try {
    return validatePairingStatus(
      await (await service()).DenyPairing(selectedRequest),
    );
  } catch {
    throw new Error('Pairing cancellation could not be confirmed.');
  }
}
