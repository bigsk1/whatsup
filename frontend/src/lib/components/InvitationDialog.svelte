<script lang="ts">
  import { onDestroy } from 'svelte';
  import { Copy, Link, Shield } from '@lucide/svelte';
  import {
    validConnectionService,
    validInvitationLink,
    type InvitationShare,
    type PairingStatus,
  } from '../pairing';

  let {
    enabled,
    pairing,
    oncreate,
    onjoin,
    oncancel,
    suspended = false,
  }: {
    enabled: boolean;
    pairing: PairingStatus;
    oncreate: (url: string, consent: boolean) => Promise<InvitationShare>;
    onjoin: (link: string, url: string, consent: boolean) => Promise<void>;
    oncancel: () => Promise<void>;
    suspended?: boolean;
  } = $props();
  let dialog = $state<HTMLDialogElement>();
  let mode = $state<'create' | 'join'>('create');
  let stage = $state<
    'form' | 'starting' | 'share' | 'joining' | 'failed' | 'canceling'
  >('form');
  let relayURL = $state('');
  let consent = $state(false);
  let pastedLink = $state('');
  let privateLink = $state('');
  let expiresAt = $state('');
  let error = $state('');
  let copyNotice = $state('');
  let relayCopyNotice = $state('');
  const publicRelayMap = 'https://tailcat.dev/derpmap.json';
  let operation = 0;
  let expiryTimer: number | undefined;
  let watchTerminal = $state(false);
  let terminalBaseline: PairingStatus | undefined;
  let resumeAfterSettings = false;

  function forget() {
    resumeAfterSettings = false;
    operation += 1;
    if (expiryTimer !== undefined) window.clearTimeout(expiryTimer);
    expiryTimer = undefined;
    relayURL = '';
    consent = false;
    pastedLink = '';
    privateLink = '';
    expiresAt = '';
    error = '';
    copyNotice = '';
    relayCopyNotice = '';
    stage = 'form';
    watchTerminal = false;
    terminalBaseline = undefined;
  }

  export function show() {
    if (!enabled || !dialog) return;
    forget();
    mode = 'create';
    dialog.showModal();
  }

  $effect(() => {
    if (!dialog) return;
    if (!enabled || pairing.state === 'awaiting-approval') {
      // Reservation hands the request to PairingDialog. Do not cancel its TLS
      // channel merely because the explicit-share surface is being hidden.
      forget();
      if (dialog.open) dialog.close();
    } else if (
      watchTerminal &&
      pairing !== terminalBaseline &&
      ['failed', 'expired', 'canceled', 'denied'].includes(pairing.state)
    ) {
      const ended = pairing.state;
      forget();
      stage = 'failed';
      error =
        ended === 'expired'
          ? 'This invitation expired. Cancel this attempt, then use a new private invitation.'
          : ended === 'failed'
            ? 'The invitation connection failed. Cancel this attempt, then check the link and connection service before trying again.'
            : 'This invitation was canceled or denied. Cancel this attempt before starting another invitation.';
    }
  });

  onDestroy(forget);

  $effect(() => {
    if (!dialog) return;
    if (suspended) {
      if (dialog.open) {
        resumeAfterSettings = true;
        dialog.close();
      }
    } else if (resumeAfterSettings) {
      resumeAfterSettings = false;
      if (enabled && pairing.state !== 'awaiting-approval') dialog.showModal();
    }
  });

  async function start() {
    if (!enabled || stage !== 'form') return;
    const selectedURL = relayURL.trim();
    const selectedLink = pastedLink.trim();
    if (
      !consent ||
      !validConnectionService(selectedURL) ||
      (mode === 'join' && !validInvitationLink(selectedLink))
    ) {
      error =
        'Check the connection service, invitation and consent before continuing.';
      return;
    }
    const selectedMode = mode;
    const current = ++operation;
    relayURL = '';
    pastedLink = '';
    consent = false;
    error = '';
    stage = selectedMode === 'create' ? 'starting' : 'joining';
    try {
      if (selectedMode === 'join') {
        await onjoin(selectedLink, selectedURL, true);
        if (current !== operation || !enabled) return;
        terminalBaseline = pairing;
        watchTerminal = true;
        return;
      }
      const share = await oncreate(selectedURL, true);
      if (current !== operation || !enabled) return;
      privateLink = share.link;
      expiresAt = share.expiresAt;
      stage = 'share';
      terminalBaseline = pairing;
      watchTerminal = true;
      expiryTimer = window.setTimeout(
        () => void cancel(),
        Math.min(
          600_000,
          Math.max(0, Date.parse(share.expiresAt) - Date.now()),
        ),
      );
    } catch {
      if (current !== operation || !enabled) return;
      privateLink = '';
      pastedLink = '';
      stage = 'failed';
      error =
        selectedMode === 'create'
          ? 'The invitation could not be created. Cancel this attempt, then check the connection service before trying again.'
          : 'The invitation could not be opened. Cancel this attempt, then check the link and connection service before trying again.';
    }
  }

  async function cancel() {
    if (stage === 'canceling') return;
    forget();
    const current = operation;
    stage = 'canceling';
    try {
      await oncancel();
      if (current !== operation) return;
      stage = 'form';
      if (dialog?.open) dialog.close();
    } catch {
      if (current !== operation || !enabled) return;
      stage = 'failed';
      error =
        'Cancellation could not be confirmed. Try Cancel again or close WhatsUp.';
    }
  }

  async function copyLink() {
    if (stage !== 'share' || privateLink === '') return;
    const current = operation;
    try {
      // Clipboard writes happen only in response to this explicit button.
      await navigator.clipboard.writeText(privateLink);
      if (current === operation) copyNotice = 'Invitation copied.';
    } catch {
      if (current === operation)
        copyNotice =
          'Copy is unavailable. Select the private link and copy it.';
    }
  }

  async function copyRelayURL() {
    if (!enabled || stage !== 'form') return;
    const current = operation;
    try {
      await navigator.clipboard.writeText(publicRelayMap);
      if (current === operation && stage === 'form')
        relayCopyNotice =
          'Public relay URL copied. Paste it into the field if you want to use it.';
    } catch {
      if (current === operation && stage === 'form')
        relayCopyNotice =
          'Copy is unavailable. Select the public relay URL and copy it.';
    }
  }

  const ready = $derived(
    enabled &&
      stage === 'form' &&
      consent &&
      validConnectionService(relayURL.trim()) &&
      (mode === 'create' || validInvitationLink(pastedLink.trim())),
  );
</script>

<dialog
  bind:this={dialog}
  class="settings-dialog invitation-dialog"
  aria-labelledby="invitation-title"
  oncancel={(event) => {
    event.preventDefault();
    void cancel();
  }}
>
  <div class="dialog-heading">
    <div class="dialog-title">
      <div class="small-icon"><Link size={20} /></div>
      <div>
        <p class="eyebrow">A PRIVATE INVITATION</p>
        <h2 id="invitation-title">
          {stage === 'share' ? 'Share your invitation' : 'Start a conversation'}
        </h2>
      </div>
    </div>
  </div>

  <div class="dialog-body">
    {#if stage === 'form'}
      <fieldset class="invitation-modes">
        <legend class="sr-only">Invitation action</legend>
        <label
          ><input type="radio" bind:group={mode} value="create" /> Create invitation</label
        >
        <label
          ><input type="radio" bind:group={mode} value="join" /> Paste invitation</label
        >
      </fieldset>

      {#if mode === 'join'}
        <label class="field-label" for="pasted-invitation"
          >Private invitation</label
        >
        <textarea
          id="pasted-invitation"
          class="invitation-input"
          bind:value={pastedLink}
          maxlength="2048"
          rows="3"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
          placeholder="whatsup://pair/v1?…"></textarea>
      {/if}

      <label class="field-label" for="connection-service"
        >Connection service HTTPS URL</label
      >
      <input
        id="connection-service"
        class="invitation-input"
        type="url"
        bind:value={relayURL}
        oninput={() => (consent = false)}
        maxlength="2048"
        autocomplete="off"
        autocapitalize="off"
        spellcheck="false"
        placeholder="https://relay.example.invalid/map.json"
      />
      <p class="muted small service-help">
        Enter the relay map URL supplied by the operator of a service you trust.
        Opening this dialog does not contact a service.
      </p>
      <div class="public-relay-reference">
        <p class="muted small">Tailcat public relay map · best effort</p>
        <div class="public-relay-copy">
          <code>{publicRelayMap}</code>
          <button
            class="secondary-button compact"
            onclick={() => void copyRelayURL()}
            ><Copy size={14} /> Copy relay URL</button
          >
        </div>
        <p class="muted small">
          This public service is rate limited. Its operator sees your IP address
          and availability when you connect. Copying this URL makes no
          connection.
        </p>
        {#if relayCopyNotice}
          <p class="muted small" role="status">{relayCopyNotice}</p>
        {/if}
      </div>
      <label class="relay-consent">
        <input type="checkbox" bind:checked={consent} />
        <span>
          I allow this connection service to learn my IP address and
          availability and relay encrypted pairing setup when needed.
        </span>
      </label>
      <p class="muted small">
        Strict direct-only pairing is unavailable. Nothing connects until you
        select a service, give consent and continue.
      </p>
    {:else if stage === 'share'}
      <p>
        Send this one-use invitation privately to the person you want to pair
        with. Both of you will still need to approve the identity fingerprints.
      </p>
      <label class="field-label" for="private-invitation"
        >Private invitation link</label
      >
      <textarea
        id="private-invitation"
        class="invitation-input private-link"
        value={privateLink}
        readonly
        rows="4"
        autocomplete="off"
        autocapitalize="off"
        spellcheck="false"></textarea>
      <button
        class="secondary-button compact copy-button"
        onclick={() => void copyLink()}
      >
        <Copy size={15} /> Copy invitation
      </button>
      {#if copyNotice}<p class="muted small" role="status">{copyNotice}</p>{/if}
      <p class="muted small service-help">
        Expires at {new Date(expiresAt).toLocaleTimeString([], {
          hour: '2-digit',
          minute: '2-digit',
        })}. A copied invitation stays in your clipboard until you replace it.
      </p>
    {:else if stage === 'starting'}
      <p role="status">
        Preparing your private invitation with the selected service…
      </p>
    {:else if stage === 'joining'}
      <p role="status">
        Opening the private invitation. Waiting for the owner to reserve this
        request…
      </p>
    {:else if stage === 'canceling'}
      <p role="status">Canceling invitation setup…</p>
    {/if}
    {#if error}<p role="alert" class="invitation-error">{error}</p>{/if}
    <div class="privacy-note">
      <Shield size={16} />
      <p>
        Invitations stay in this session. WhatsUp has no analytics or automatic
        diagnostic uploads. Messaging starts after both people approve and
        contact setup completes. Files are not available yet.
      </p>
    </div>
  </div>

  <div class="dialog-footer">
    <button
      class="secondary-button compact"
      disabled={stage === 'canceling'}
      onclick={() => void cancel()}
    >
      {stage === 'canceling' ? 'Canceling…' : 'Cancel'}
    </button>
    {#if stage === 'form'}
      <button
        class="primary-button compact"
        disabled={!ready}
        onclick={() => void start()}
      >
        {mode === 'create'
          ? 'Create private invitation'
          : 'Open private invitation'}
      </button>
    {/if}
  </div>
</dialog>

<style>
  .invitation-dialog .dialog-body > p {
    font-size: 12px;
    line-height: 1.8;
  }
  .invitation-modes {
    display: flex;
    gap: 18px;
    margin: 0 0 22px;
    padding: 0;
    border: 0;
  }
  .invitation-modes label {
    display: flex;
    gap: 7px;
    align-items: center;
    font-size: 12px;
  }
  input[type='radio'],
  input[type='checkbox'] {
    accent-color: var(--accent);
  }
  .field-label {
    display: block;
    margin: 17px 0 8px;
    font-size: 12px;
  }
  .invitation-input {
    width: 100%;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
    color: var(--text);
    padding: 11px;
    font: inherit;
    font-size: 12px;
  }
  textarea.invitation-input {
    resize: vertical;
    min-height: 85px;
  }
  .private-link {
    user-select: text;
    font-family: ui-monospace, monospace;
    overflow-wrap: anywhere;
  }
  .service-help {
    margin: 9px 0 16px;
  }
  .public-relay-reference {
    padding: 12px;
    border: 1px solid var(--border);
    border-radius: 8px;
  }
  .public-relay-copy {
    display: flex;
    flex-wrap: wrap;
    gap: 9px;
    align-items: center;
    margin: 7px 0;
  }
  .public-relay-copy code {
    user-select: text;
    overflow-wrap: anywhere;
    font-size: 11px;
    min-width: 0;
  }
  .relay-consent {
    display: flex;
    gap: 9px;
    align-items: flex-start;
    font-size: 12px;
    line-height: 1.8;
    margin: 18px 0 9px;
  }
  .relay-consent input {
    margin-top: 5px;
    flex-shrink: 0;
  }
  .copy-button {
    display: inline-flex;
    gap: 7px;
    align-items: center;
    margin: 12px 0;
  }
  .invitation-error {
    padding: 11px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
  }
</style>
