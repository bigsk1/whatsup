<script lang="ts">
  import { onDestroy, tick, untrack } from 'svelte';
  import { validSecret, type RecoveryPreview } from '../state/security';
  let {
    preview,
    busy,
    notice,
    onconfirm,
    oncancel,
  }: {
    preview: RecoveryPreview | null;
    busy: boolean;
    notice: string;
    onconfirm: (secret: string, required: boolean, replace: boolean) => void;
    oncancel: () => void;
  } = $props();
  let dialog: HTMLDialogElement;
  let cancelButton: HTMLButtonElement;
  let shownHandle: string | undefined;
  let required = $state(false),
    replace = $state(false),
    secret = $state(''),
    confirm = $state('');
  function clear() {
    required = false;
    replace = false;
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
    const value = secret,
      requireApp = required,
      replaceData = replace;
    clear();
    onconfirm(value, requireApp, replaceData);
  }
  $effect(() => {
    const handle = preview?.handle;
    untrack(() => {
      if (!dialog || shownHandle === handle) return;
      shownHandle = handle;
      clear();
      if (handle && !dialog.open) {
        dialog.showModal();
        void tick().then(() => cancelButton?.focus());
      } else if (!handle && dialog.open) dialog.close();
    });
  });
  $effect(() => {
    if (!required) {
      secret = '';
      confirm = '';
    }
  });
  onDestroy(clear);
</script>

<dialog
  bind:this={dialog}
  class="settings-dialog"
  aria-labelledby="recovery-title"
  oncancel={(event) => {
    event.preventDefault();
    cancel();
  }}
>
  <form onsubmit={submit} autocomplete="off">
    <div class="dialog-heading">
      <h2 id="recovery-title">Restore this recovery copy?</h2>
    </div>
    <div class="dialog-body">
      {#if preview}<p>
          The encrypted copy has been authenticated. It contains {preview.contacts}
          saved contacts, {preview.messages} messages and {preview.attachments} attachments.
        </p>
        <p>
          Your identity and contact pins come from this copy. The app will use
          this device’s OS credential for local protection.
        </p>
        {#if preview.credentialResetRequired}<p role="status">
            The existing OS credential is missing or cannot unlock this device’s
            data. Restoring also replaces that credential.
          </p>{/if}
        {#if preview.replaceRequired || preview.credentialResetRequired}<label
            ><input type="checkbox" bind:checked={replace} disabled={busy} />
            Replace existing local data{preview.credentialResetRequired
              ? ' and its OS credential'
              : ''} with this authenticated copy</label
          >{/if}
        <label
          ><input type="checkbox" bind:checked={required} disabled={busy} /> Require
          an app passphrase on this device</label
        >
        {#if required}<label
            >New app passphrase<input
              type="password"
              bind:value={secret}
              autocomplete="off"
              spellcheck="false"
              autocapitalize="off"
              maxlength="1024"
              disabled={busy}
            /></label
          ><label
            >Confirm app passphrase<input
              type="password"
              bind:value={confirm}
              autocomplete="off"
              spellcheck="false"
              autocapitalize="off"
              maxlength="1024"
              disabled={busy}
            /></label
          >
          <p class="muted small">
            12–1024 UTF-8 bytes. Spaces and Unicode are kept exactly as entered.
          </p>
          {#if confirm !== '' && confirm !== secret}<p role="status">
              The passphrases do not match.
            </p>{/if}{/if}
      {/if}
      {#if notice}<p role="alert">{notice}</p>{/if}
    </div>
    <div class="dialog-footer">
      <button
        bind:this={cancelButton}
        type="button"
        class="secondary-button"
        disabled={busy}
        onclick={cancel}>Cancel</button
      ><button
        type="submit"
        class="primary-button"
        disabled={busy ||
          !preview ||
          ((preview.replaceRequired || preview.credentialResetRequired) &&
            !replace) ||
          (required && (!validSecret(secret) || secret !== confirm))}
        >{busy ? 'Restoring…' : 'Restore recovery copy'}</button
      >
    </div>
  </form>
</dialog>

<style>
  dialog {
    max-width: 36rem;
  }
  p {
    margin: 0 0 1rem;
  }
  label {
    display: block;
    margin: 1rem 0;
  }
  input[type='password'] {
    display: block;
    width: 100%;
    margin-top: 0.4rem;
    padding: 0.65rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    color: var(--text);
  }
  input[type='checkbox'] {
    margin-right: 0.5rem;
  }
  .dialog-footer {
    gap: 0.75rem;
  }
</style>
