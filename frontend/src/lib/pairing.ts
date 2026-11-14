import { hasDesktopBridge } from './backend';

export const pairingStates = [
  'unavailable',
  'idle',
  'connecting',
  'awaiting-approval',
  'waiting-for-peer',
  'saving',
  'pending',
  'completed',
  'reconnect-required',
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

// The bearer link exists only for the explicit invitation share action. Keep it
// out of browser storage, navigation, logging and generic status DTOs.
export interface InvitationShare {
  link: string;
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
    'reconnect-required',
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
          !(
            ['pending', 'reconnect-required'].includes(status.state) &&
            status.expiresAt === ''
          )))) ||
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

export function validConnectionService(value: string): boolean {
  if (
    value.length === 0 ||
    value.length > 2048 ||
    /[\s\u0000-\u001f\u007f]/.test(value)
  )
    return false;
  try {
    const url = new URL(value);
    return (
      url.protocol === 'https:' &&
      url.hostname !== '' &&
      url.username === '' &&
      url.password === '' &&
      url.hash === ''
    );
  } catch {
    return false;
  }
}

export function validInvitationLink(value: string): boolean {
  if (
    value.length === 0 ||
    value.length > 2048 ||
    /[\s\u0000-\u001f\u007f]/.test(value)
  )
    return false;
  try {
    const url = new URL(value);
    return (
      url.protocol === 'whatsup:' &&
      url.hostname === 'pair' &&
      url.pathname === '/v1' &&
      url.username === '' &&
      url.password === '' &&
      url.hash === '' &&
      url.search !== ''
    );
  } catch {
    return false;
  }
}

export function validateInvitationShare(value: unknown): InvitationShare {
  if (!value || typeof value !== 'object' || Array.isArray(value))
    throw new Error('The desktop returned an invalid invitation.');
  const record = value as Record<string, unknown>;
  if (
    Object.keys(record).length !== 2 ||
    typeof record.link !== 'string' ||
    !validInvitationLink(record.link) ||
    typeof record.expiresAt !== 'string' ||
    record.expiresAt.length > 64 ||
    !timestamp.test(record.expiresAt) ||
    !Number.isFinite(Date.parse(record.expiresAt))
  )
    throw new Error('The desktop returned an invalid invitation.');
  return { link: record.link, expiresAt: record.expiresAt };
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

export async function createInvitation(
  relayMapURL: string,
  allowRelayFallback: boolean,
): Promise<InvitationShare> {
  if (!validConnectionService(relayMapURL) || allowRelayFallback !== true)
    throw new Error(
      'Choose an HTTPS connection service and allow encrypted pairing relay.',
    );
  try {
    return validateInvitationShare(
      await (await service()).CreateInvitation(relayMapURL, allowRelayFallback),
    );
  } catch {
    throw new Error(
      'An invitation could not be created. Check the connection service and try again.',
    );
  }
}

export async function joinInvitation(
  link: string,
  relayMapURL: string,
  allowRelayFallback: boolean,
): Promise<PairingStatus> {
  if (
    !validInvitationLink(link) ||
    !validConnectionService(relayMapURL) ||
    allowRelayFallback !== true
  )
    throw new Error(
      'Check the private invitation and allow the selected connection service.',
    );
  try {
    return validatePairingStatus(
      await (
        await service()
      ).JoinInvitation(link, relayMapURL, allowRelayFallback),
    );
  } catch {
    throw new Error(
      'The invitation could not be opened. Check it and the connection service, then try again.',
    );
  }
}

export async function cancelInvitation(): Promise<PairingStatus> {
  try {
    return validatePairingStatus(await (await service()).CancelInvitation());
  } catch {
    throw new Error('Invitation cancellation could not be confirmed.');
  }
}
