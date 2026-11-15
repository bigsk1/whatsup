import { writable } from 'svelte/store';
import { hasDesktopBridge, validateStatus, type Status } from '../backend';

export type SecurityOperation =
  | ''
  | 'unlock'
  | 'enable'
  | 'disable'
  | 'export'
  | 'import'
  | 'import-confirm'
  | 'repair';
export interface StorageSecurity {
  requiresPassphrase: boolean;
  recoveryAvailable: boolean;
  busy: boolean;
  backupAttention: string;
}
export interface RecoveryPreview {
  handle: string;
  contacts: number;
  messages: number;
  attachments: number;
  sourceRequiresPassphrase: boolean;
  replaceRequired: boolean;
  credentialResetRequired: boolean;
}
export interface SecurityBridge {
  get(): Promise<unknown>;
  unlock(secret: string): Promise<unknown>;
  setAside(secret: string): Promise<unknown>;
  configure(
    current: string,
    secret: string,
    enabled: boolean,
  ): Promise<unknown>;
  export(secret: string): Promise<unknown>;
  stage(secret: string): Promise<unknown>;
  commit(
    handle: string,
    secret: string,
    required: boolean,
    replace: boolean,
  ): Promise<unknown>;
  cancel(handle: string): Promise<void>;
}

// Reject unpaired UTF-16 rather than letting JSON/TextEncoder replace it. Never
// trim or normalize: spaces and every valid Unicode code point are significant.
export function validSecret(value: string): boolean {
  if (value.length > 1024) return false;
  let bytes = 0;
  for (let i = 0; i < value.length; i++) {
    const code = value.charCodeAt(i);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(++i);
      if (!(next >= 0xdc00 && next <= 0xdfff)) return false;
      bytes += 4;
    } else if (code >= 0xdc00 && code <= 0xdfff) return false;
    else bytes += code < 0x80 ? 1 : code < 0x800 ? 2 : 3;
    if (bytes > 1024) return false;
  }
  return bytes >= 12 && bytes <= 1024;
}
function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value))
    throw new Error('Invalid security response.');
  return value as Record<string, unknown>;
}
export function validateStorageSecurity(input: unknown): StorageSecurity {
  const value = object(input);
  const keys = Object.keys(value);
  const backupAttention =
    'backupAttention' in value ? value.backupAttention : '';
  if (
    !(
      (keys.length === 3 && !('backupAttention' in value)) ||
      (keys.length === 4 && 'backupAttention' in value)
    ) ||
    ['requiresPassphrase', 'recoveryAvailable', 'busy'].some(
      (key) => typeof value[key] !== 'boolean',
    ) ||
    typeof backupAttention !== 'string' ||
    backupAttention.length > 160 ||
    (backupAttention !== '' &&
      !/^(?:(?:\.recovery-(?:stage|rollback)-[A-Za-z0-9_-]{22})[\\/])?(?:pre-migration-[0-9a-f]{32}\.vault|\.(?:state|protection)-[0-9]+\.tmp|state\.vault)$/.test(
        backupAttention,
      ))
  )
    throw new Error('Invalid security response.');
  return {
    requiresPassphrase: value.requiresPassphrase as boolean,
    recoveryAvailable: value.recoveryAvailable as boolean,
    busy: value.busy as boolean,
    backupAttention,
  };
}
export function validateRecoveryPreview(input: unknown): RecoveryPreview {
  const value = object(input);
  if (
    Object.keys(value).length !== 7 ||
    typeof value.handle !== 'string' ||
    !/^[A-Za-z0-9_-]{32}$/.test(value.handle) ||
    ['contacts', 'messages', 'attachments'].some(
      (key) => !Number.isSafeInteger(value[key]) || (value[key] as number) < 0,
    ) ||
    (value.contacts as number) > 32 ||
    [
      'sourceRequiresPassphrase',
      'replaceRequired',
      'credentialResetRequired',
    ].some((key) => typeof value[key] !== 'boolean')
  )
    throw new Error('Invalid recovery response.');
  return {
    handle: value.handle,
    contacts: value.contacts as number,
    messages: value.messages as number,
    attachments: value.attachments as number,
    sourceRequiresPassphrase: value.sourceRequiresPassphrase as boolean,
    replaceRequired: value.replaceRequired as boolean,
    credentialResetRequired: value.credentialResetRequired as boolean,
  };
}
async function service() {
  if (!hasDesktopBridge())
    throw new Error('Open the desktop app to manage local security.');
  return import('../../../bindings/whatsup/internal/desktop/service');
}
const bridge: SecurityBridge = {
  get: async () => (await service()).GetStorageSecurity(),
  unlock: async (secret) => (await service()).UnlockWithPassphrase(secret),
  setAside: async (secret) => (await service()).SetAsideBackup(secret),
  configure: async (current, secret, enabled) =>
    (await service()).ConfigurePassphrase(current, secret, enabled),
  export: async (secret) => (await service()).ExportRecovery(secret),
  stage: async (secret) => (await service()).StageRecoveryImport(secret),
  commit: async (handle, secret, required, replace) =>
    (await service()).CommitRecoveryImport(handle, secret, required, replace),
  cancel: async (handle) => {
    await (await service()).CancelRecoveryImport(handle);
  },
};
const initial = () => ({
  operation: '' as SecurityOperation,
  loaded: false,
  busy: false,
  notice: '',
  summary: {
    requiresPassphrase: false,
    recoveryAvailable: false,
    busy: false,
    backupAttention: '',
  } as StorageSecurity,
  preview: null as RecoveryPreview | null,
});
export function createSecurity(
  callbacks: {
    changed: (status: Status) => void;
    beforeImport: () => void;
    failed?: () => void;
  },
  api: SecurityBridge = bridge,
) {
  let current = initial(),
    generation = 0,
    loadRequest = 0,
    startupChecked = false;
  const state = writable(current);
  const publish = (update: Partial<typeof current>) => {
    current = { ...current, ...update };
    state.set(current);
  };
  const cancelHandle = (handle: string) => {
    void api.cancel(handle).catch(() => {});
  };
  function cancel() {
    generation++;
    if (current.preview) cancelHandle(current.preview.handle);
    publish({ operation: '', preview: null, busy: false, notice: '' });
  }
  function reset() {
    cancel();
    current = initial();
    state.set(current);
  }
  async function load() {
    if (current.busy) return;
    const epoch = generation,
      request = ++loadRequest;
    try {
      const summary = validateStorageSecurity(await api.get());
      if (epoch === generation && request === loadRequest)
        publish({ summary, loaded: true });
    } catch {
      if (epoch === generation && request === loadRequest)
        publish({
          loaded: false,
          notice: 'Local security settings could not be loaded. Try again.',
        });
    }
  }
  function begin(operation: SecurityOperation) {
    if (
      current.busy ||
      operation === 'import-confirm' ||
      operation === '' ||
      (operation === 'repair' &&
        (!current.loaded || current.summary.backupAttention === ''))
    )
      return;
    cancel();
    publish({ operation });
    void load();
  }
  function startup(status: Status) {
    if (startupChecked) return;
    if (status.storageState === 'unavailable') return;
    startupChecked = true;
    if (status.storageState === 'locked' && status.requiresPassphrase)
      begin('unlock');
  }
  async function submit(currentSecret: string, secret: string) {
    const operation = current.operation;
    if (current.busy || operation === '' || operation === 'import-confirm')
      return;
    const needsCurrent =
      operation === 'disable' ||
      ((operation === 'enable' || operation === 'repair') &&
        current.summary.requiresPassphrase);
    const needsNew = operation !== 'disable' && operation !== 'repair';
    if (
      (needsCurrent && !validSecret(currentSecret)) ||
      (needsNew && !validSecret(secret)) ||
      (operation === 'repair' &&
        (!current.loaded || current.summary.backupAttention === ''))
    ) {
      publish({
        notice:
          'Use a passphrase containing 12–1024 UTF-8 bytes. Spaces count exactly as entered.',
      });
      return;
    }
    const epoch = ++generation;
    publish({ busy: true, notice: '' });
    let refreshAfterFailure = false;
    let failed = false;
    try {
      if (operation === 'import') {
        const input = await api.stage(secret);
        // A cancelled native picker returns an empty handle without a preview.
        const raw = object(input);
        if (raw.handle === '') {
          if (
            Object.keys(raw).length !== 7 ||
            raw.contacts !== 0 ||
            raw.messages !== 0 ||
            raw.attachments !== 0 ||
            raw.sourceRequiresPassphrase !== false ||
            raw.replaceRequired !== false ||
            raw.credentialResetRequired !== false
          )
            throw new Error();
          if (epoch === generation)
            publish({ operation: '', notice: 'Recovery import cancelled.' });
          return;
        }
        const preview = validateRecoveryPreview(input);
        if (epoch !== generation) {
          cancelHandle(preview.handle);
          return;
        }
        publish({ operation: 'import-confirm', preview });
      } else if (operation === 'export') {
        const result = object(await api.export(secret));
        if (
          Object.keys(result).length !== 1 ||
          typeof result.saved !== 'boolean'
        )
          throw new Error();
        if (epoch === generation)
          publish({
            operation: '',
            notice: result.saved
              ? 'Encrypted recovery copy saved.'
              : 'Recovery export cancelled.',
          });
      } else {
        const result = validateStatus(
          await (operation === 'unlock'
            ? api.unlock(secret)
            : operation === 'repair'
              ? api.setAside(needsCurrent ? currentSecret : '')
              : api.configure(
                  needsCurrent ? currentSecret : '',
                  operation === 'enable' ? secret : '',
                  operation === 'enable',
                )),
        );
        if (epoch !== generation) return;
        publish({
          operation: '',
          loaded: true,
          summary: {
            requiresPassphrase: result.requiresPassphrase,
            recoveryAvailable: result.storageState === 'unlocked',
            busy: false,
            backupAttention: '',
          },
          notice:
            operation === 'unlock'
              ? ''
              : operation === 'repair'
                ? 'The local backup was preserved encrypted. Local storage was retried.'
                : operation === 'enable'
                  ? 'App passphrase enabled.'
                  : 'App passphrase disabled.',
        });
        callbacks.changed(result);
      }
    } catch {
      if (epoch === generation) {
        failed = true;
        refreshAfterFailure =
          operation === 'enable' ||
          operation === 'disable' ||
          operation === 'export';
        publish({
          ...(refreshAfterFailure
            ? { operation: '' as SecurityOperation }
            : {}),
          notice:
            operation === 'unlock'
              ? 'The passphrase was not accepted or local storage is unavailable. Try again.'
              : operation === 'repair'
                ? 'The backup could not be set aside. Check the current app passphrase and retry. A changed or unavailable file must be reviewed before retrying.'
                : operation === 'import'
                  ? 'The recovery copy could not be authenticated. Check its passphrase and try again.'
                  : 'The security action could not be completed. Check the passphrase and local storage, then try again.',
        });
      }
    } finally {
      if (epoch === generation) publish({ busy: false });
    }
    if (failed && epoch === generation) await load();
    if (refreshAfterFailure && epoch === generation) callbacks.failed?.();
  }
  async function commit(secret: string, required: boolean, replace: boolean) {
    const preview = current.preview;
    if (current.busy || current.operation !== 'import-confirm' || !preview)
      return;
    if (
      (required && !validSecret(secret)) ||
      ((preview.replaceRequired || preview.credentialResetRequired) && !replace)
    ) {
      publish({
        notice:
          'Confirm replacement and enter a valid app passphrase if one is required.',
      });
      return;
    }
    const epoch = ++generation;
    publish({ busy: true, notice: '' });
    let failed = false;
    try {
      callbacks.beforeImport();
      const result = validateStatus(
        await api.commit(
          preview.handle,
          required ? secret : '',
          required,
          replace,
        ),
      );
      if (epoch !== generation) return;
      publish({
        operation: '',
        preview: null,
        loaded: true,
        summary: {
          requiresPassphrase: result.requiresPassphrase,
          recoveryAvailable: result.storageState === 'unlocked',
          busy: false,
          backupAttention: '',
        },
        notice: 'Recovery copy restored.',
      });
      callbacks.changed(result);
    } catch {
      if (epoch === generation) {
        failed = true;
        cancelHandle(preview.handle);
        publish({
          operation: '',
          preview: null,
          notice:
            'The recovery copy could not be installed. Your previous local state has not been confirmed replaced. Go offline and review local storage before trying again.',
        });
        callbacks.failed?.();
      }
    } finally {
      if (epoch === generation) publish({ busy: false });
    }
    if (failed && epoch === generation) await load();
  }
  return { state, load, begin, startup, submit, commit, cancel, reset };
}
