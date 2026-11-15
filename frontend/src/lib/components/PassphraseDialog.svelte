<script lang="ts">
  import { onDestroy, tick, untrack } from 'svelte';
  import { validSecret, type SecurityOperation } from '../state/security';
  let {
    operation,
    busy,
    notice,
    requiresPassphrase,
    backupAttention,
    backupRepairAvailable,
    onsubmit,
    oncancel,
    onrepair,
  }: {
    operation: SecurityOperation;
    busy: boolean;
    notice: string;
    requiresPassphrase: boolean;
    backupAttention: string;
    backupRepairAvailable: boolean;
    onsubmit: (current: string, secret: string) => void;
    oncancel: () => void;
    onrepair: () => void;
  } = $props();
  let dialog: HTMLDialogElement;
  let current = $state(''),
    secret = $state(''),
    confirm = $state('');
  let shownOperation: SecurityOperation | undefined;
  const visible = $derived(operation !== '' && operation !== 'import-confirm');
  const newSecret = $derived(operation === 'enable' || operation === 'export');
  const needsCurrent = $derived(
    operation === 'disable' ||
      ((operation === 'enable' || operation === 'repair') &&
        requiresPassphrase),
  );
  const needsSecret = $derived(
    operation !== 'disable' && operation !== 'repair',
  );
  const title = $derived(
    operation === 'unlock'
      ? 'Go online'
      : operation === 'repair'
        ? 'Set aside a local backup'
        : operation === 'enable'
          ? 'Require an app passphrase'
          : operation === 'disable'
            ? 'Disable the app passphrase'
            : operation === 'export'
              ? 'Export an encrypted recovery copy'
              : 'Import an encrypted recovery copy',
  );
  function clear() {
    current = '';
    secret = '';
    confirm = '';
  }
  function cancel() {
    if (busy) return;
    clear();
    oncancel();
  }
  function submit(event: SubmitEvent) {
    event.preventDefault();
    if (busy) return;
    const old = current,
      value = secret;
    clear();
    onsubmit(old, value);
  }
  $effect(() => {
    const active = visible,
      mode = operation;
    untrack(() => {
      if (!dialog || shownOperation === mode) return;
      shownOperation = mode;
      clear();
      if (active && !dialog.open) {
        dialog.showModal();
        void tick().then(() =>
          dialog
            .querySelector<HTMLInputElement>('input[type="password"]')
            ?.focus(),
        );
      } else if (!active && dialog.open) dialog.close();
    });
    void mode;
  });
  onDestroy(clear);
</script>

<dialog
  bind:this={dialog}
  class="settings-dialog"
  aria-labelledby="passphrase-title"
  oncancel={(event) => {
    event.preventDefault();
    cancel();
  }}
>
  <form onsubmit={submit} autocomplete="off">
    <div class="dialog-heading"><h2 id="passphrase-title">{title}</h2></div>
    <div class="dialog-body">
      <p>
        {operation === 'unlock'
          ? 'Enter your app passphrase to unlock local data and reconnect saved contacts.'
          : operation === 'repair'
            ? 'WhatsUp will preserve this backup encrypted in the app data folder, then retry opening your local data. Its contents will be kept for recovery.'
            : operation === 'enable'
              ? 'Your OS credential and this passphrase will both be required to unlock local data.'
              : operation === 'disable'
                ? 'Enter your current app passphrase. Your OS credential will continue protecting local data.'
                : operation === 'export'
                  ? 'Choose a recovery passphrase for this encrypted copy. Keep it separately; it is required to restore the copy on another device.'
                  : 'Enter the recovery copy’s passphrase. A native file dialog will let you choose the copy; its contents are authenticated before you confirm replacement.'}
      </p>
      {#if backupAttention}
        <div role="status">
          <p>
            <strong>A local backup needs attention:</strong>
            <code>{backupAttention}</code>
          </p>
          {#if operation !== 'repair' && backupRepairAvailable}
            <p>
              WhatsUp could not authenticate or update this backup. You can
              preserve it encrypted and retry opening local storage.
            </p>
            <button
              type="button"
              class="secondary-button"
              disabled={busy}
              onclick={onrepair}>Set aside and retry…</button
            >
          {:else if !backupRepairAvailable}
            <p>
              This file or folder cannot be set aside safely. Keep its contents
              and check its ownership, private permissions and availability,
              then retry going online.
            </p>
          {/if}
        </div>
      {/if}
      {#if needsCurrent}<label
          >Current app passphrase<input
            type="password"
            bind:value={current}
            autocomplete="off"
            spellcheck="false"
            autocapitalize="off"
            maxlength="1024"
            disabled={busy}
          /></label
        >{/if}
      {#if needsSecret}<label
          >{operation === 'unlock'
            ? 'App passphrase'
            : operation === 'import'
              ? 'Recovery passphrase'
              : operation === 'export'
                ? 'New recovery passphrase'
                : 'New app passphrase'}<input
            type="password"
            bind:value={secret}
            autocomplete="off"
            spellcheck="false"
            autocapitalize="off"
            maxlength="1024"
            disabled={busy}
          /></label
        >{/if}
      {#if newSecret}<label
          >Confirm {operation === 'export' ? 'recovery' : 'app'} passphrase<input
            type="password"
            bind:value={confirm}
            autocomplete="off"
            spellcheck="false"
            autocapitalize="off"
            maxlength="1024"
            disabled={busy}
          /></label
        >{#if confirm !== '' && secret !== confirm}<p role="status">
            The passphrases do not match.
          </p>{/if}{/if}
      {#if needsCurrent || needsSecret}<p class="muted small">
          12–1024 UTF-8 bytes. Spaces and Unicode are kept exactly as entered.
        </p>{/if}
      {#if notice}<p role="alert">{notice}</p>{/if}
    </div>
    <div class="dialog-footer">
      <button
        type="button"
        class="secondary-button"
        disabled={busy}
        onclick={cancel}>Cancel</button
      ><button
        type="submit"
        class="primary-button"
        disabled={busy ||
          (needsCurrent && !validSecret(current)) ||
          (needsSecret && !validSecret(secret)) ||
          (operation === 'repair' && !backupRepairAvailable) ||
          (newSecret && secret !== confirm)}
        >{busy
          ? 'Please wait…'
          : operation === 'unlock'
            ? 'Unlock and go online'
            : operation === 'repair'
              ? 'Set aside and retry'
              : operation === 'enable'
                ? 'Enable passphrase'
                : operation === 'disable'
                  ? 'Disable passphrase'
                  : operation === 'export'
                    ? 'Choose export location…'
                    : 'Choose recovery copy…'}</button
      >
    </div>
  </form>
</dialog>

<style>
  dialog {
    max-width: 35rem;
  }
  p {
    margin: 0 0 1rem;
  }
  label {
    display: block;
    margin: 1rem 0;
  }
  input {
    display: block;
    width: 100%;
    margin-top: 0.4rem;
    padding: 0.65rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    color: var(--text);
  }
  .dialog-footer {
    gap: 0.75rem;
  }
</style>
