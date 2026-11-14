<script lang="ts">
  import { ShieldCheck } from '@lucide/svelte';
  import { hasPairingIdentity, type PairingStatus } from '../pairing';

  let {
    status,
    busy,
    denying,
    error,
    onapprove,
    ondeny,
    suspended = false,
  }: {
    status: PairingStatus;
    busy: boolean;
    denying: boolean;
    error: string;
    onapprove: (requestID: string, fingerprint: string) => void;
    ondeny: (requestID: string) => void;
    suspended?: boolean;
  } = $props();
  let dialog = $state<HTMLDialogElement>();
  let denyButton = $state<HTMLButtonElement>();
  let presentedRequest = '';

  $effect(() => {
    if (!dialog) return;
    if (suspended) {
      if (dialog.open) dialog.close();
      return;
    }
    const identityAvailable = hasPairingIdentity(status);
    if (status.state === 'awaiting-approval') {
      const firstPresentation = presentedRequest !== status.requestID;
      presentedRequest = status.requestID;
      if (!dialog.open) dialog.showModal();
      if (firstPresentation) denyButton?.focus();
    } else if (!identityAvailable || presentedRequest !== status.requestID) {
      presentedRequest = '';
      if (dialog.open) dialog.close();
    }
  });

  const title = $derived(
    status.state === 'awaiting-approval'
      ? 'Pairing request'
      : status.state === 'waiting-for-peer'
        ? 'Waiting for the other person'
        : status.state === 'saving'
          ? 'Saving contact setup'
          : 'Contact setup incomplete',
  );
</script>

<dialog
  bind:this={dialog}
  aria-labelledby="pairing-title"
  aria-describedby="pairing-description"
  class="settings-dialog pairing-dialog"
  oncancel={(event) => {
    event.preventDefault();
    ondeny(status.requestID);
  }}
>
  <div class="dialog-heading">
    <div class="dialog-title">
      <div class="small-icon"><ShieldCheck size={20} /></div>
      <div>
        <p class="eyebrow">JUST THE TWO OF YOU</p>
        <h2 id="pairing-title">{title}</h2>
      </div>
    </div>
  </div>

  <div class="dialog-body">
    {#if status.state === 'awaiting-approval'}
      <p id="pairing-description">
        Compare their full fingerprint with the other person through a channel
        you trust. Approve only if it matches.
      </p>
    {:else if status.state === 'waiting-for-peer'}
      <p id="pairing-description" role="status">
        You approved this request. The other person still needs to approve.
      </p>
    {:else if status.state === 'saving'}
      <p id="pairing-description" role="status">
        Both approvals are received. Contact setup is being saved privately on
        this device.
      </p>
    {:else if status.state === 'reconnect-required'}
      <p id="pairing-description" role="status">
        This contact was approved, but its saved setup cannot reconnect yet. You
        can discard this setup to use a new invitation.
      </p>
    {:else}
      <p id="pairing-description" role="status">
        Setup is incomplete. This contact is not active yet. You can cancel and
        try a new invitation.
      </p>
    {/if}

    {#if hasPairingIdentity(status)}
      <div class="fingerprint-group">
        <span class="fingerprint-label" id="peer-fingerprint-label"
          >Their fingerprint</span
        >
        <code class="fingerprint" aria-labelledby="peer-fingerprint-label"
          >{status.peerFingerprint}</code
        >
        <span class="fingerprint-label" id="local-fingerprint-label"
          >Your fingerprint</span
        >
        <code class="fingerprint" aria-labelledby="local-fingerprint-label"
          >{status.localFingerprint}</code
        >
      </div>
    {/if}
    {#if hasPairingIdentity(status) && status.expiresAt !== ''}
      <p class="muted small expiry">
        Request expires at {new Date(status.expiresAt).toLocaleTimeString([], {
          hour: '2-digit',
          minute: '2-digit',
        })}. Pairing does not open chat until setup is complete.
      </p>
    {/if}
    {#if error}<p class="pairing-error" role="alert">{error}</p>{/if}
  </div>

  <div class="dialog-footer">
    <button
      bind:this={denyButton}
      class="secondary-button compact"
      disabled={denying}
      onclick={() => ondeny(status.requestID)}
      >{denying
        ? 'Canceling…'
        : status.state === 'awaiting-approval'
          ? 'Deny'
          : 'Cancel pairing'}</button
    >
    {#if status.state === 'awaiting-approval'}
      <button
        class="primary-button compact"
        disabled={busy}
        onclick={() => onapprove(status.requestID, status.peerFingerprint)}
        >{busy ? 'Approving…' : 'Approve'}</button
      >
    {/if}
  </div>
</dialog>

<style>
  .pairing-dialog .dialog-body > p {
    font-size: 12px;
    line-height: 1.8;
  }

  .fingerprint-group {
    margin-top: 18px;
  }

  .pairing-dialog .fingerprint {
    font-size: 12px;
    user-select: text;
  }

  .expiry {
    margin-top: 18px;
  }

  .pairing-error {
    color: var(--text);
    border: 1px solid var(--border);
    background: var(--surface);
    border-radius: 8px;
    margin-top: 16px;
    padding: 10px;
  }
</style>
