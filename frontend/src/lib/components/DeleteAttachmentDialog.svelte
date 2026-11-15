<script lang="ts">
  import { tick } from 'svelte';
  let {
    target,
    busy,
    notice,
    onconfirm,
    oncancel,
  }: {
    target: { id: string; name: string } | null;
    busy: boolean;
    notice: string;
    onconfirm: () => void;
    oncancel: () => void;
  } = $props();
  let dialog: HTMLDialogElement;
  let cancel: HTMLButtonElement;
  $effect(() => {
    if (!dialog) return;
    if (target && !dialog.open) {
      dialog.showModal();
      void tick().then(() => cancel?.focus());
    } else if (!target && dialog.open) dialog.close();
  });
</script>

<dialog
  bind:this={dialog}
  class="settings-dialog"
  aria-labelledby="delete-attachment-title"
  oncancel={(event) => {
    event.preventDefault();
    if (!busy) oncancel();
  }}
>
  <div class="dialog-heading">
    <h2 id="delete-attachment-title">Delete this attachment from WhatsUp?</h2>
  </div>
  <p><strong>{target?.name ?? ''}</strong></p>
  <p>
    This removes the attachment stored in WhatsUp on this device. Copies you
    saved elsewhere and your contact's copy stay. An unfinished transfer is
    cancelled.
  </p>
  {#if notice}<p role="alert">{notice}</p>{/if}
  <div class="dialog-footer">
    <button
      bind:this={cancel}
      class="secondary-button"
      disabled={busy}
      onclick={oncancel}>Cancel</button
    >
    <button
      class="primary-button"
      disabled={busy || !target}
      onclick={onconfirm}>{busy ? 'Deleting…' : 'Delete from WhatsUp'}</button
    >
  </div>
</dialog>
