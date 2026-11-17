<script lang="ts">
  import { onDestroy } from 'svelte';
  import Icon from '../icons/Icon.svelte';
  import {
    validConnectionService,
    validInvitationLink,
    maximumInvitationBytes,
    maximumServiceURLBytes,
    type InvitationShare,
    type PairingStatus,
  } from '../pairing';
  import { createInvitationForm, publicRelayMap } from '../state/invitation';
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
  let resumeAfterSettings = false;
  const flow = createInvitationForm({
    enabled: () => enabled,
    pairing: () => pairing,
    create: (url, consent) => oncreate(url, consent),
    join: (link, url, consent) => onjoin(link, url, consent),
    cancel: () => oncancel(),
    close: () => dialog?.close(),
  });
  const formState = flow.state;
  const {
    mode,
    stage,
    relayURL,
    consent,
    pastedLink,
    privateLink,
    expiresAt,
    error,
    copyNotice,
    relayCopyNotice,
    inspection,
    inspecting,
  } = $derived($formState);
  let now = $state(Date.now());
  const expiryTime = $derived(Date.parse(expiresAt));
  const secondsRemaining = $derived(
    Number.isFinite(expiryTime)
      ? Math.max(0, Math.ceil((expiryTime - now) / 1000))
      : 0,
  );
  $effect(() => {
    if (stage !== 'share' || !expiresAt) return;
    now = Date.now();
    const timer = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(timer);
  });
  export function show() {
    if (!enabled || !dialog) return;
    flow.reset();
    resumeAfterSettings = false;
    dialog.showModal();
  }
  const start = () => flow.start();
  const cancel = () => flow.cancel();
  const copyLink = () => flow.copyLink();
  const copyRelayURL = () => flow.copyRelayURL();
  $effect(() => {
    if (!dialog) return;
    if (!enabled || pairing.state === 'awaiting-approval') {
      flow.reset();
      resumeAfterSettings = false;
      if (dialog.open) dialog.close();
    } else flow.observe(pairing);
  });
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
  onDestroy(flow.reset);
  const ready = $derived(
    enabled &&
      stage === 'form' &&
      consent &&
      validConnectionService(relayURL.trim()) &&
      (mode === 'create' ||
        (inspection !== null && validInvitationLink(pastedLink.trim()))),
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
      <div class="small-icon"><Icon name="link" /></div>
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
          ><input
            type="radio"
            checked={mode === 'create'}
            onchange={() => flow.setMode('create')}
            value="create"
          /> Create invitation</label
        >
        <label
          ><input
            type="radio"
            checked={mode === 'join'}
            onchange={() => flow.setMode('join')}
            value="join"
          /> Paste invitation</label
        >
      </fieldset>

      {#if mode === 'create'}
        <p class="service-help">
          Invitations last 10 minutes and can be used once. Have the other
          person ready before you create one.
        </p>
      {/if}

      {#if mode === 'join'}
        <label class="field-label" for="pasted-invitation"
          >Private invitation</label
        >
        <textarea
          id="pasted-invitation"
          class="invitation-input"
          value={pastedLink}
          oninput={(event) => flow.setLink(event.currentTarget.value)}
          maxlength={maximumInvitationBytes}
          rows="3"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
          placeholder="whatsup://pair/v2?…"></textarea>
        <button
          type="button"
          class="secondary-button compact"
          disabled={inspecting || !validInvitationLink(pastedLink.trim())}
          onclick={() => void flow.inspect()}
          >{inspecting ? 'Reviewing…' : 'Review invitation'}</button
        >
        {#if inspection}<p class="service-help" role="status">
            This invitation uses <strong>{inspection.host}</strong>. Allow?
          </p>
          <output aria-label="Invitation connection service"
            >{inspection.serviceURL}</output
          >{/if}
      {/if}

      {#if mode === 'create'}
        <label class="field-label" for="connection-service"
          >Connection service HTTPS URL</label
        >
        <input
          id="connection-service"
          class="invitation-input"
          type="url"
          value={relayURL}
          oninput={(event) => flow.setURL(event.currentTarget.value)}
          maxlength={maximumServiceURLBytes}
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
          placeholder="https://relay.example.invalid/map.json"
        />
        <p class="muted small service-help">
          Enter the relay map URL supplied by the operator of a service you
          trust. Opening this dialog does not contact a service.
        </p>
        <button
          type="button"
          class="secondary-button compact"
          disabled={relayURL.trim() !== ''}
          onclick={() => flow.usePublicRelay()}>Use Tailcat public relay</button
        >
        <div class="public-relay-reference">
          <p class="muted small">Tailcat public relay map · best effort</p>
          <div class="public-relay-copy">
            <code>{publicRelayMap}</code>
            <button
              class="secondary-button compact"
              onclick={() => void copyRelayURL()}
              ><Icon name="copy" size="1.1em" /> Copy relay URL</button
            >
          </div>
          <p class="muted small">
            This public service is rate limited. Its operator sees your IP
            address and availability when you connect. Copying this URL makes no
            connection.
          </p>
          {#if relayCopyNotice}
            <p class="muted small" role="status">{relayCopyNotice}</p>
          {/if}
        </div>
      {/if}
      <label class="relay-consent">
        <input
          type="checkbox"
          checked={consent}
          disabled={mode === 'join' && inspection === null}
          onchange={(event) => flow.setConsent(event.currentTarget.checked)}
        />
        <span>
          I allow this connection service to learn my IP address and
          availability and relay encrypted connections when needed.
        </span>
      </label>
      <p class="muted small">
        This choice applies to pairing and your ongoing encrypted connection.
      </p>
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
        <Icon name="copy" size="1.1em" /> Copy invitation
      </button>
      {#if copyNotice}<p class="muted small" role="status">{copyNotice}</p>{/if}
      <p
        class="service-help"
        role="timer"
        aria-label="Invitation time remaining"
      >
        {Math.floor(secondsRemaining / 60)}:{String(
          secondsRemaining % 60,
        ).padStart(2, '0')} remaining
      </p>
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
      <Icon name="shield" />
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
