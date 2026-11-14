<script lang="ts">
  let {
    contactID,
    enabled,
    busy,
    notice,
    onconfirm,
  }: {
    contactID: string;
    enabled: boolean;
    busy: boolean;
    notice: string;
    onconfirm: (contactID: string, messageID?: string) => void;
  } = $props();
  let dialog: HTMLDialogElement;
  let cancelButton: HTMLButtonElement;
  let selectedContact = $state('');
  let selectedMessage = $state<string | undefined>();
  export function show(messageID?: string) {
    if (!enabled || busy || !contactID) return;
    selectedContact = contactID;
    selectedMessage = messageID;
    dialog.showModal();
    cancelButton.focus();
  }
  export function close() {
    dialog?.close();
    selectedContact = '';
    selectedMessage = undefined;
  }
  export function isOpen() {
    return dialog?.open === true;
  }
  $effect(() => {
    if (
      dialog &&
      !busy &&
      selectedContact &&
      (!enabled || selectedContact !== contactID)
    )
      close();
  });
</script>

<dialog
  bind:this={dialog}
  class="settings-dialog"
  aria-labelledby="history-action-title"
  oncancel={(event) => {
    event.preventDefault();
    if (!busy) close();
  }}
>
  <div class="dialog-heading">
    <h2 id="history-action-title">
      {selectedMessage ? 'Delete this local message?' : 'Clear local history?'}
    </h2>
  </div>
  <p>
    {#if selectedMessage}
      This deletes the local message. Your contact stays saved.
    {:else}
      This clears local history and keeps the contact. Unfinished attachments
      are cancelled.
    {/if}
    Messages still queued for delivery continue sending until acknowledged.
  </p>
  {#if notice}<p role="alert">{notice}</p>{/if}
  <div class="dialog-footer">
    <button
      class="secondary-button"
      bind:this={cancelButton}
      disabled={busy}
      onclick={close}>Cancel</button
    >
    <button
      class="primary-button"
      disabled={busy || !enabled || contactID !== selectedContact}
      onclick={() => onconfirm(selectedContact, selectedMessage)}
      >{busy
        ? 'Saving…'
        : selectedMessage
          ? 'Delete local message'
          : 'Clear history'}</button
    >
  </div>
</dialog>

<style>
  dialog {
    max-width: 34rem;
  }
  p {
    margin: 1rem 0;
  }
  .dialog-footer {
    gap: 0.75rem;
  }
</style>
