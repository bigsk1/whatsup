<script lang="ts">
  import Icon from '../icons/Icon.svelte';

  let {
    contactID,
    name,
    voiceCalls,
    inCall,
    busy,
    onstart,
    onenable,
  }: {
    /** The contact currently selected in the conversation pane. */
    contactID: string;
    name: string;
    /** The local "Allow voice calls" setting. */
    voiceCalls: boolean;
    /** A call with anyone is in progress on this device. */
    inCall: boolean;
    busy: boolean;
    onstart: (contactID: string) => void;
    onenable: () => void;
  } = $props();

  let dialog: HTMLDialogElement;
  let cancelButton: HTMLButtonElement;
  let target = $state('');

  export function show() {
    if (busy || inCall || contactID === '') return;
    target = contactID;
    dialog.showModal();
    cancelButton.focus();
  }

  export function close() {
    if (dialog?.open) dialog.close();
    target = '';
  }

  $effect(() => {
    // The selected contact changed or a call began: a question about calling
    // someone else must not stay on screen.
    if (dialog?.open && (contactID !== target || inCall)) close();
  });
</script>

<dialog
  bind:this={dialog}
  class="settings-dialog call-dialog"
  aria-labelledby="call-confirm-title"
  aria-describedby="call-confirm-description"
  oncancel={(event) => {
    event.preventDefault();
    close();
  }}
>
  <div class="dialog-heading">
    <div class="dialog-title">
      <div class="small-icon"><Icon name="phone" /></div>
      <h2 id="call-confirm-title" dir="auto">
        {voiceCalls ? `Call ${name}?` : 'Voice calls are switched off'}
      </h2>
    </div>
  </div>
  <div class="dialog-body">
    {#if voiceCalls}
      <p id="call-confirm-description">
        This starts a voice call. Your microphone turns on only after they
        answer.
      </p>
      <p class="muted small">
        Headphones are recommended so the other person does not hear an echo.
      </p>
    {:else}
      <p id="call-confirm-description">
        Voice calls use your microphone during a call you place or answer. They
        are off until you turn them on, and you can turn them off again in
        Settings, App behavior.
      </p>
    {/if}
  </div>
  <div class="dialog-footer">
    <button
      bind:this={cancelButton}
      class="secondary-button compact"
      onclick={close}>Cancel</button
    >
    {#if voiceCalls}
      <button
        class="primary-button compact"
        disabled={busy || contactID !== target}
        onclick={() => {
          const id = target;
          close();
          onstart(id);
        }}><Icon name="phone" size="1.1em" /><span>Call</span></button
      >
    {:else}
      <button
        class="primary-button compact"
        disabled={busy}
        onclick={() => {
          close();
          onenable();
        }}>Turn on voice calls</button
      >
    {/if}
  </div>
</dialog>

<style>
  .call-dialog {
    max-width: 480px;
  }
  .dialog-title h2 {
    overflow-wrap: anywhere;
  }
  .dialog-body p + p {
    margin-top: 12px;
  }
  .dialog-footer {
    gap: 12px;
    flex-wrap: wrap;
  }
</style>
