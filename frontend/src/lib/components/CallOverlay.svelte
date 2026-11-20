<script lang="ts">
  import Icon from '../icons/Icon.svelte';
  import type { AudioStats, CallView } from '../state/calls';

  let {
    call,
    name,
    path,
    notice,
    busy,
    onanswer,
    onend,
    onmute,
    ondismiss,
    readStats,
  }: {
    /** The one call in progress on this device, or null. */
    call: CallView | null;
    /** Display name of the person on that call. */
    name: string;
    /** Direct, Relayed or Unknown for that contact's connection. */
    path: string;
    notice: string;
    busy: boolean;
    onanswer: (call: CallView) => void;
    onend: (call: CallView) => void;
    onmute: (call: CallView) => void;
    ondismiss: () => void;
    /** Audio counters for the answered call; numbers only. */
    readStats?: () => AudioStats | null;
  } = $props();

  let dialog: HTMLDialogElement;
  let declineButton: HTMLButtonElement;
  let now = $state(Date.now());
  let showDetails = $state(false);
  let stats = $state<AudioStats | null>(null);

  const incoming = $derived(call?.state === 'incoming');
  const answered = $derived(
    call?.state === 'connecting' || call?.state === 'active',
  );

  $effect(() => {
    if (!dialog) return;
    if (incoming && !dialog.open) {
      dialog.showModal();
      // Decline has the focus, so Enter or Space cannot answer by accident.
      declineButton.focus();
    } else if (!incoming && dialog.open) dialog.close();
  });

  $effect(() => {
    if (call?.state !== 'active') return;
    now = Date.now();
    const timer = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(timer);
  });

  $effect(() => {
    // Read the counters once a second, only while the readout is open.
    if (!showDetails || !answered || !readStats) {
      stats = null;
      return;
    }
    stats = readStats();
    const timer = setInterval(() => (stats = readStats()), 1000);
    return () => clearInterval(timer);
  });

  $effect(() => {
    if (!call) showDetails = false;
  });

  function elapsed(since: number): string {
    const seconds = Math.max(0, Math.floor((now - since) / 1000));
    const pad = (value: number) => String(value).padStart(2, '0');
    const hours = Math.floor(seconds / 3600);
    return (
      (hours ? hours + ':' : '') +
      pad(Math.floor((seconds % 3600) / 60)) +
      ':' +
      pad(seconds % 60)
    );
  }

  const status = $derived(
    !call
      ? ''
      : call.state === 'outgoing'
        ? call.peerRinging
          ? 'Ringing…'
          : 'Calling…'
        : call.state === 'connecting'
          ? 'Connecting…'
          : call.state === 'active'
            ? elapsed(call.activeSince)
            : '',
  );
</script>

<dialog
  bind:this={dialog}
  class="settings-dialog call-dialog"
  aria-labelledby="incoming-call-title"
  aria-describedby="incoming-call-description"
  oncancel={(event) => {
    // Escape neither answers nor declines; the call keeps ringing.
    event.preventDefault();
  }}
>
  <div class="dialog-heading">
    <div class="dialog-title">
      <div class="small-icon"><Icon name="phone" /></div>
      <h2 id="incoming-call-title">Incoming voice call</h2>
    </div>
  </div>
  <div class="dialog-body">
    <p id="incoming-call-description" class="caller" dir="auto">{name}</p>
    <p class="muted small">
      Your microphone turns on only if you accept. Headphones are recommended.
    </p>
  </div>
  <div class="dialog-footer">
    <button
      bind:this={declineButton}
      class="secondary-button compact"
      disabled={busy || !call}
      onclick={() => call && onend(call)}
      ><Icon name="phoneEnd" size="1.1em" /><span>Decline</span></button
    >
    <button
      class="primary-button compact"
      disabled={busy || !call}
      onclick={() => call && onanswer(call)}
      ><Icon name="phone" size="1.1em" /><span>Accept</span></button
    >
  </div>
</dialog>

{#if call && !incoming}
  <section class="call-bar" aria-label="Voice call" aria-live="polite">
    <span class="call-icon"><Icon name="phone" /></span>
    <span class="call-who">
      <strong dir="auto">{name}</strong>
      <span class="call-status">{status}{answered ? ` · ${path}` : ''}</span>
    </span>
    {#if answered}
      <span class="mic-state" class:muted={call.micMuted}>
        <Icon name={call.micMuted ? 'micOff' : 'mic'} size="1.1em" />
        {call.micMuted ? 'Muted' : 'Microphone on'}
      </span>
      {#if call.peerMicMuted}<span class="peer-muted">They are muted</span>{/if}
      <button
        type="button"
        class="icon-button"
        aria-pressed={call.micMuted}
        aria-label={call.micMuted ? 'Unmute microphone' : 'Mute microphone'}
        title={call.micMuted ? 'Unmute microphone' : 'Mute microphone'}
        onclick={() => call && onmute(call)}
        ><Icon name={call.micMuted ? 'micOff' : 'mic'} /></button
      >
      <button
        type="button"
        class="secondary-button compact"
        aria-expanded={showDetails}
        aria-controls="call-details"
        onclick={() => (showDetails = !showDetails)}>Details</button
      >
    {/if}
    <button
      type="button"
      class="danger-button compact end-call"
      aria-label={answered ? 'Hang up' : 'Cancel call'}
      onclick={() => call && onend(call)}
      ><Icon name="phoneEnd" size="1.1em" /><span
        >{answered ? 'Hang up' : 'Cancel'}</span
      ></button
    >
    {#if showDetails && answered}
      <dl id="call-details" class="call-details" aria-live="off">
        {#if stats}
          <div>
            <dt>Your audio</dt>
            <dd>
              microphone {stats.captured} · sent {stats.sent} · level {stats.micLevel}%
            </dd>
          </div>
          <div>
            <dt>Their audio</dt>
            <dd>
              received {stats.received} · decoded {stats.decoded} · played {stats.playedSeconds}
              s · level {stats.incomingLevel}%
            </dd>
          </div>
          <div>
            <dt>Sound output</dt>
            <dd>{stats.output}</dd>
          </div>
        {:else}
          <div>
            <dt>Audio</dt>
            <dd>starting…</dd>
          </div>
        {/if}
      </dl>
    {/if}
  </section>
{/if}

{#if notice}
  <div class="call-notice" role="status">
    <span>{notice}</span>
    <button
      type="button"
      class="icon-button"
      aria-label="Dismiss call notice"
      onclick={ondismiss}><Icon name="close" size="1em" /></button
    >
  </div>
{/if}

<style>
  .call-dialog {
    max-width: 440px;
  }
  .caller {
    font-size: 1.25rem;
    font-weight: var(--strong-weight, 700);
    overflow-wrap: anywhere;
  }
  .dialog-body p + p {
    margin-top: 12px;
  }
  .dialog-footer {
    gap: 12px;
    flex-wrap: wrap;
  }
  .call-bar,
  .call-notice {
    position: fixed;
    right: 20px;
    z-index: 30;
    display: flex;
    align-items: center;
    gap: 12px;
    max-width: min(560px, calc(100vw - 40px));
    padding: 10px 14px;
    border: 1px solid var(--accent-line, var(--border));
    border-radius: var(--radius, 14px);
    background: var(--raised, var(--panel));
    color: var(--text);
    box-shadow: var(--shadow-1, 0 8px 24px rgb(0 0 0 / 0.35));
  }
  .call-bar {
    top: 76px;
    flex-wrap: wrap;
  }
  .call-details {
    flex-basis: 100%;
    margin: 0;
    display: grid;
    gap: 4px;
    color: var(--muted);
    font-size: 0.85rem;
    font-variant-numeric: tabular-nums;
  }
  .call-details div {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
  }
  .call-details dt {
    color: var(--text);
    min-width: 6.5em;
  }
  .call-details dd {
    margin: 0;
  }
  .call-notice {
    bottom: 84px;
    border-color: var(--border);
  }
  .call-icon {
    color: var(--accent);
    display: inline-flex;
  }
  .call-who {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .call-who strong {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .call-status,
  .peer-muted {
    color: var(--muted);
    font-size: 0.85rem;
    font-variant-numeric: tabular-nums;
  }
  .mic-state {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    color: var(--ok, var(--accent));
    font-size: 0.85rem;
    white-space: nowrap;
  }
  .mic-state.muted {
    color: var(--warn, var(--muted));
  }
  .end-call {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    white-space: nowrap;
  }
</style>
