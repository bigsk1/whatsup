<script lang="ts">
  import { UserRoundMinus } from '@lucide/svelte';

  let {
    contactID,
    enabled,
    busy,
    onconfirm,
  }: {
    contactID: string;
    enabled: boolean;
    busy: boolean;
    onconfirm: (contactID: string) => void;
  } = $props();
  let dialog: HTMLDialogElement;
  let cancelButton: HTMLButtonElement;
  let selectedContact = $state('');

  export function show() {
    if (!enabled || contactID === '' || busy) return;
    selectedContact = contactID;
    dialog.showModal();
    cancelButton.focus();
  }

  export function close() {
    dialog.close();
    selectedContact = '';
  }

  export function isOpen() {
    return dialog?.open === true;
  }

  $effect(() => {
    if (
      dialog &&
      !busy &&
      selectedContact !== '' &&
      (!enabled || contactID !== selectedContact)
    )
      close();
  });
</script>

<dialog
  bind:this={dialog}
  class="settings-dialog forget-dialog"
  aria-labelledby="forget-contact-title"
  aria-describedby="forget-contact-description"
  oncancel={(event) => {
    event.preventDefault();
    if (!busy) close();
  }}
>
  <div class="dialog-heading">
    <div class="dialog-title">
      <div class="small-icon"><UserRoundMinus size={20} /></div>
      <h2 id="forget-contact-title">Forget contact and delete conversation</h2>
    </div>
  </div>
  <div class="dialog-body">
    <p id="forget-contact-description">
      This deletes this contact, your saved messages, queued messages and
      pairing keys from this device. Your device identity stays the same.
    </p>
    <p>
      The other person's history is untouched. To chat again, exchange a new
      invitation and approve each other again. The new pairing uses fresh keys.
    </p>
    <p class="muted small">This cannot be undone on this device.</p>
  </div>
  <div class="dialog-footer">
    <button
      bind:this={cancelButton}
      class="secondary-button compact"
      disabled={busy}
      onclick={close}>Cancel</button
    >
    <button
      class="primary-button compact forget-confirm"
      disabled={busy || !enabled || contactID !== selectedContact}
      onclick={() => onconfirm(selectedContact)}
      >{busy ? 'Forgetting…' : 'Forget contact and delete conversation'}</button
    >
  </div>
</dialog>

<style>
  .forget-dialog {
    max-width: 570px;
  }
  .dialog-title h2 {
    line-height: 1.45;
  }
  .dialog-body p + p {
    margin-top: 14px;
  }
  .dialog-footer {
    gap: 12px;
    flex-wrap: wrap;
  }
  .forget-confirm {
    white-space: normal;
  }
</style>
