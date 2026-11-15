export interface Status {
  version: string;
  identityFingerprint: string;
  storageState: string;
  networkState: string;
  directOnlyAvailable: boolean;
  pairingAvailable: boolean;
  messagingAvailable: boolean;
  attachmentsAvailable: boolean;
  requiresPassphrase: boolean;
}

export const unavailableStatus: Status = Object.freeze({
  version: '0.0.17',
  identityFingerprint: '',
  storageState: 'unavailable',
  networkState: 'unavailable',
  directOnlyAvailable: false,
  pairingAvailable: false,
  messagingAvailable: false,
  attachmentsAvailable: false,
  requiresPassphrase: false,
});

export type StatusMethod = 'GetStatus' | 'UnlockStorage' | 'LockStorage';
type Invoke = (method: StatusMethod) => Promise<unknown>;

export function validateStatus(value: unknown): Status {
  if (!value || typeof value !== 'object') {
    throw new Error('The desktop returned an invalid status.');
  }
  const record = value as Record<string, unknown>;
  const strings = [
    'version',
    'identityFingerprint',
    'storageState',
    'networkState',
  ] as const;
  const booleans = [
    'directOnlyAvailable',
    'pairingAvailable',
    'messagingAvailable',
    'attachmentsAvailable',
    'requiresPassphrase',
  ] as const;
  if (
    strings.some(
      (name) => typeof record[name] !== 'string' || record[name].length > 256,
    ) ||
    booleans.some((name) => typeof record[name] !== 'boolean')
  ) {
    throw new Error('The desktop returned an invalid status.');
  }
  // Select only the public DTO fields. Extra backend properties never enter UI state.
  return Object.fromEntries(
    [...strings, ...booleans].map((name) => [name, record[name]]),
  ) as unknown as Status;
}

export function hasDesktopBridge(): boolean {
  return Boolean(
    typeof window !== 'undefined' &&
    (window as Window & { _wails?: { environment?: unknown } })._wails
      ?.environment,
  );
}

async function invokeDesktop(method: StatusMethod): Promise<unknown> {
  if (!hasDesktopBridge())
    throw new Error('Open the desktop app to use local storage.');
  const service =
    await import('../../bindings/whatsup/internal/desktop/service');
  return service[method]();
}

export async function requestStatus(
  method: StatusMethod = 'GetStatus',
  invoke: Invoke = invokeDesktop,
): Promise<Status> {
  return validateStatus(await invoke(method));
}
