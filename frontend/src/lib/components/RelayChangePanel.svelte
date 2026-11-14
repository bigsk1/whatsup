<script lang="ts">
  import {
    validRelayURL,
    validRelayCarry,
    maxRelayCarryBytes,
    type RelayCapabilities,
    type RelayChangeSnapshot,
  } from '../state/relay';
  let {
    snapshot,
    capabilities,
    draftURL,
    consent,
    loaded,
    loading,
    busy,
    notice,
    manual,
    exportText,
    importText,
    ondraft,
    onconsent,
    onpreset,
    onpropose,
    oncancel,
    onreload,
    onmanual,
    onexport,
    oncopy,
    onimporttext,
    oninspect,
  }: {
    snapshot: RelayChangeSnapshot | null;
    capabilities: RelayCapabilities;
    draftURL: string;
    consent: boolean;
    loaded: boolean;
    loading: boolean;
    busy: boolean;
    notice: string;
    manual: boolean;
    exportText: string;
    importText: string;
    ondraft: (value: string) => void;
    onconsent: (value: boolean) => void;
    onpreset: () => void;
    onpropose: () => void;
    oncancel: (requestID: string) => void;
    onreload: () => void;
    onmanual: (manual: boolean) => void;
    onexport: () => void;
    oncopy: () => void;
    onimporttext: (text: string) => void;
    oninspect: () => void;
  } = $props();
  const supported = $derived(capabilities.contactControlsSupported);
  const allowed = $derived(supported || manual);
  const status = $derived(
    snapshot?.state === 'complete'
      ? 'Connection service changed.'
      : snapshot?.state === 'declined'
        ? 'The service change was declined.'
        : snapshot?.state === 'preparing'
          ? 'Both devices are preparing the approved service.'
          : snapshot?.state === 'switching'
            ? 'Switching to the approved service…'
            : snapshot?.state === 'recovering'
              ? 'Finishing an interrupted service change…'
              : snapshot?.state === 'proposed'
                ? 'Waiting for the other person to approve this service.'
                : snapshot?.state === 'awaiting-approval'
                  ? 'A connection service change is waiting for your approval.'
                  : '',
  );
</script>

<section aria-labelledby="relay-service-heading" class="relay-panel">
  <h2 id="relay-service-heading">Connection service</h2>
  {#if snapshot}<p>
      Current service: <span dir="auto">{snapshot.currentServiceURL}</span>
    </p>{/if}
  <p class="muted small">
    Both people must approve a service change. Your verified identities and
    conversation stay the same.
  </p>
  {#if !supported}<p role="status">
      {capabilities.peerProtocol === 0
        ? 'Connect both devices for automatic exchange, or use signed text below.'
        : 'The other device needs an update for automatic service changes.'}
    </p>{/if}
  {#if status}<p role="status">{status}</p>{/if}
  {#if snapshot?.proposedServiceURL}<p>
      Proposed service: <span dir="auto">{snapshot.proposedServiceURL}</span>
    </p>{/if}
  <form
    onsubmit={(event) => {
      event.preventDefault();
      onpropose();
    }}
  >
    <label for="relay-change-url">New HTTPS relay-map URL</label>
    <input
      id="relay-change-url"
      type="url"
      maxlength="2048"
      autocomplete="off"
      spellcheck="false"
      value={draftURL}
      disabled={!allowed || !loaded || !snapshot?.canPropose || busy}
      oninput={(event) => ondraft(event.currentTarget.value)}
    />
    <button
      type="button"
      class="text-button"
      disabled={!allowed ||
        !loaded ||
        !snapshot?.canPropose ||
        busy ||
        draftURL !== ''}
      onclick={onpreset}>Use Tailcat public relay</button
    >
    <label class="relay-consent"
      ><input
        type="checkbox"
        checked={consent}
        disabled={!allowed ||
          !loaded ||
          !snapshot?.canPropose ||
          busy ||
          !validRelayURL(draftURL)}
        onchange={(event) => onconsent(event.currentTarget.checked)}
      /> Allow this service to establish and, when necessary, relay encrypted connections</label
    >
    <button
      type="submit"
      class="secondary-button compact"
      disabled={!allowed ||
        !loaded ||
        !snapshot?.canPropose ||
        busy ||
        !consent ||
        !validRelayURL(draftURL)}
      >{manual ? 'Create signed proposal' : 'Propose service change'}</button
    >
  </form>
  {#if snapshot?.localApproved && ['proposed', 'awaiting-approval', 'preparing'].includes(snapshot.state)}<button
      type="button"
      class="text-button"
      disabled={!allowed || !loaded || busy}
      onclick={() => oncancel(snapshot!.requestID)}
      >Cancel service change</button
    >{/if}
  <details open={manual}>
    <summary
      onclick={(event) => {
        event.preventDefault();
        onmanual(!manual);
      }}>Advanced: exchange signed text</summary
    >
    <p>
      Use this when the current service is unavailable. Both devices must
      support signed service changes. Create or approve the proposal above, then
      share your signed text with this contact and inspect their signed reply
      here.
    </p>
    <button
      type="button"
      class="secondary-button compact"
      disabled={busy ||
        !loaded ||
        !snapshot ||
        (!snapshot.requestID &&
          !['declined', 'complete'].includes(snapshot.state)) ||
        (!snapshot.localApproved &&
          !['declined', 'complete'].includes(snapshot.state))}
      onclick={onexport}>Prepare signed text</button
    >
    {#if exportText}<label for="relay-export-text"
        >Signed service-change text</label
      ><textarea
        id="relay-export-text"
        readonly
        value={exportText}
        spellcheck="false"
        autocomplete="off"
        aria-label="Signed service-change text"></textarea><button
        type="button"
        class="text-button"
        disabled={busy}
        onclick={oncopy}>Copy signed text</button
      >{/if}
    <label for="relay-import-text">Signed text from this contact</label
    ><textarea
      id="relay-import-text"
      value={importText}
      maxlength={maxRelayCarryBytes}
      disabled={busy || !loaded}
      spellcheck="false"
      autocomplete="off"
      oninput={(event) => onimporttext(event.currentTarget.value)}></textarea>
    <button
      type="button"
      class="secondary-button compact"
      disabled={busy || !loaded || !validRelayCarry(importText)}
      onclick={oninspect}>Inspect signed text</button
    >
    <p class="muted small">
      Inspection verifies the saved identity and proposal locally. Approving a
      new service uses the separate unchecked consent control.
    </p>
  </details>
  {#if notice}<p role="alert">{notice}</p>{/if}
  {#if !loaded || notice}<button
      type="button"
      class="text-button"
      disabled={busy || loading}
      onclick={onreload}
      >{loading ? 'Checking…' : 'Reload service status'}</button
    >{/if}
</section>

<style>
  .relay-panel p {
    margin: 0.6rem 0;
    overflow-wrap: anywhere;
    unicode-bidi: isolate;
  }
  label {
    display: block;
    margin: 0.7rem 0;
  }
  input[type='url'],
  textarea {
    display: block;
    width: 100%;
    padding: 0.65rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    color: var(--text);
  }
  textarea {
    min-height: 5rem;
    resize: vertical;
    overflow-wrap: anywhere;
  }
  details {
    margin-top: 1rem;
  }
  summary {
    cursor: pointer;
  }
  input[type='checkbox'] {
    margin-right: 0.4rem;
  }
  .relay-consent {
    line-height: 1.5;
  }
</style>
