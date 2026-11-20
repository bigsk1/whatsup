<script lang="ts">
  import Icon from '../icons/Icon.svelte';

  let {
    contactID,
    name,
    voiceCalls,
    videoCalls = false,
    inCall,
    busy,
    onstart,
    onenable,
  }: {
    /** The local "Allow video calls" setting. */
    videoCalls?: boolean;
    /** The contact currently selected in the conversation pane. */
    contactID: string;
    name: string;
    /** The local "Allow voice calls" setting. */
    voiceCalls: boolean;
    /** A call with anyone is in progress on this device. */
    inCall: boolean;
    busy: boolean;
    onstart: (contactID: string, video: boolean) => void;
    /** Switch on what the call asked about needs: voice, or voice and video. */
    onenable: (video: boolean) => void;
  } = $props();

  let dialog: HTMLDialogElement;
  let cancelButton: HTMLButtonElement;
  let target = $state('');
  // Whether the open question is about a video call.
  let video = $state(false);
  const allowed = $derived(voiceCalls && (!video || videoCalls));

  /** Ask about a voice call, or a video call when asked for one. */
  export function show(forVideo = false) {
    if (busy || inCall || contactID === '') return;
    target = contactID;
    video = forVideo;
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
      <div class="small-icon"><Icon name={video ? 'video' : 'phone'} /></div>
      <h2 id="call-confirm-title" dir="auto">
        {allowed
          ? video
            ? `Video call ${name}?`
            : `Call ${name}?`
          : video
            ? 'Video calls are switched off'
            : 'Voice calls are switched off'}
      </h2>
    </div>
  </div>
  <div class="dialog-body">
    {#if allowed && video}
      <p id="call-confirm-description">
        This starts a video call. Your microphone and camera turn on only after
        they answer.
      </p>
      <p class="muted small">
        Headphones are recommended so the other person does not hear an echo.
      </p>
    {:else if allowed}
      <p id="call-confirm-description">
        This starts a voice call. Your microphone turns on only after they
        answer.
      </p>
      <p class="muted small">
        Headphones are recommended so the other person does not hear an echo.
      </p>
    {:else if video}
      <p id="call-confirm-description">
        Video calls use your microphone and camera during a call you place or
        answer. They are off until you turn them on, and you can turn them off
        again in Settings, App behavior.
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
    {#if allowed}
      <button
        class="primary-button compact"
        disabled={busy || contactID !== target}
        onclick={() => {
          const id = target;
          const withVideo = video;
          close();
          onstart(id, withVideo);
        }}
        ><Icon name={video ? 'video' : 'phone'} size="1.1em" /><span
          >{video ? 'Video call' : 'Call'}</span
        ></button
      >
    {:else}
      <button
        class="primary-button compact"
        disabled={busy}
        onclick={() => {
          const withVideo = video;
          close();
          onenable(withVideo);
        }}>{video ? 'Turn on video calls' : 'Turn on voice calls'}</button
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
