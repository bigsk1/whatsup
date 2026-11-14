<script lang="ts">
  import { tick, untrack } from 'svelte';
  import type { RelayChangeSnapshot } from '../state/relay';
  let {
    snapshot,
    supported,
    loaded,
    busy,
    consent,
    notice,
    onconsent,
    onapprove,
    ondecline,
    onreload,
  }: {
    snapshot: RelayChangeSnapshot | null;
    supported: boolean;
    loaded: boolean;
    busy: boolean;
    consent: boolean;
    notice: string;
    onconsent: (value: boolean) => void;
    onapprove: (requestID: string) => void;
    ondecline: (requestID: string) => void;
    onreload: () => void;
  } = $props();
  let dialog: HTMLDialogElement;
  let declineButton: HTMLButtonElement;
  const pending = $derived(Boolean(snapshot?.canApprove && supported));
  const enabled = $derived(pending && loaded);
  $effect(() => {
    const open = pending,
      request = snapshot?.requestID;
    untrack(() => {
      if (!dialog) return;
      if (open && !dialog.open) {
        dialog.showModal();
        void tick().then(() => declineButton?.focus());
      } else if (!open && dialog.open) dialog.close();
    });
    void request;
  });
</script>

<dialog
  bind:this={dialog}
  class="settings-dialog"
  aria-labelledby="relay-approval-title"
  oncancel={(event) => {
    event.preventDefault();
    if (!busy && enabled && snapshot) ondecline(snapshot.requestID);
  }}
>
  <div class="dialog-heading">
    <h2 id="relay-approval-title">Allow a connection service change?</h2>
  </div>
  <div class="dialog-body">
    {#if snapshot}<p>
        This proposal uses <strong dir="auto">{snapshot.proposedHost}</strong>.
      </p>
      <p class="service-url" dir="auto">{snapshot.proposedServiceURL}</p>
      <p>
        The service can learn your IP address and availability and may relay
        encrypted traffic. Your verified identity pins and conversation stay
        saved.
      </p>
      <label
        ><input
          type="checkbox"
          checked={consent}
          disabled={busy || !enabled}
          onchange={(event) => onconsent(event.currentTarget.checked)}
        /> Allow this service for encrypted connections</label
      >{/if}
    {#if notice}<p role="alert">{notice}</p>{/if}
    {#if !loaded}<button
        type="button"
        class="text-button"
        disabled={busy}
        onclick={onreload}>Reload service status</button
      >{/if}
  </div>
  <div class="dialog-footer">
    <button
      bind:this={declineButton}
      type="button"
      class="secondary-button"
      disabled={busy || !enabled}
      onclick={() => snapshot && ondecline(snapshot.requestID)}>Decline</button
    ><button
      type="button"
      class="primary-button"
      disabled={busy || !enabled || !consent}
      onclick={() => snapshot && onapprove(snapshot.requestID)}
      >{busy ? 'Saving…' : 'Allow service change'}</button
    >
  </div>
</dialog>

<style>
  dialog {
    max-width: 35rem;
  }
  p {
    margin: 0 0 1rem;
  }
  .service-url {
    overflow-wrap: anywhere;
    unicode-bidi: isolate;
  }
  label {
    display: block;
    line-height: 1.5;
  }
  input {
    margin-right: 0.4rem;
  }
  .dialog-footer {
    gap: 0.75rem;
  }
</style>
