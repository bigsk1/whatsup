<script lang="ts">
  import { onMount } from 'svelte';
  import {
    ArrowUpRight,
    Check,
    ChevronDown,
    CircleHelp,
    CirclePlus,
    Info,
    LockKeyhole,
    MessageCircle,
    MoreHorizontal,
    Paperclip,
    Search,
    Send,
    Settings,
    Shield,
    Smile,
    Users,
    WifiOff,
    X,
  } from '@lucide/svelte';
  import SettingsDialog from './lib/components/SettingsDialog.svelte';
  import PairingDialog from './lib/components/PairingDialog.svelte';
  import InvitationDialog from './lib/components/InvitationDialog.svelte';
  import {
    hasDesktopBridge,
    requestStatus,
    unavailableStatus,
    type Status,
    type StatusMethod,
  } from './lib/backend';
  import {
    approvePairing,
    denyPairing,
    createInvitation,
    joinInvitation,
    cancelInvitation,
    hasPairingIdentity,
    requestPairingStatus,
    unavailablePairing,
    type PairingStatus,
  } from './lib/pairing';
  import {
    applyPreferences,
    readPreferences,
    savePreferences,
    type Preferences,
  } from './lib/themes/preferences';

  let status: Status = $state({ ...unavailableStatus });
  let preferences = $state(readPreferences());
  let native = $state(false);
  let busy = $state(false);
  let notice = $state('');
  let search = $state('');
  let settings: SettingsDialog;
  let aboutDialog: HTMLDialogElement;
  let invitationDialog: InvitationDialog;
  let pairing: PairingStatus = $state({ ...unavailablePairing });
  let pairingNotice = $state('');
  let actionRequest = $state('');
  let actionKind = $state<'approve' | 'deny' | ''>('');
  let mounted = false;
  let pairingEpoch = 0;
  let requestSequence = 0;
  let acceptedSequence = 0;
  let activePoll: number | undefined;
  let activeAction = 0;
  let pairingTimer: number | undefined;
  let invitationBusy = $state(false);
  let invitationSequence = 0;

  function pairingEnabled() {
    return mounted && native && !busy && status.storageState === 'unlocked';
  }

  function clearPairing() {
    if (pairingTimer !== undefined) window.clearInterval(pairingTimer);
    pairingTimer = undefined;
    pairingEpoch += 1;
    activePoll = undefined;
    pairing = { ...unavailablePairing };
    pairingNotice = '';
    actionRequest = '';
    actionKind = '';
    invitationSequence += 1;
    invitationBusy = false;
  }

  const invitationEnabled = $derived(
    native &&
      !busy &&
      status.storageState === 'unlocked' &&
      status.pairingAvailable,
  );
  const canStartInvitation = $derived(
    invitationEnabled &&
      !invitationBusy &&
      !hasPairingIdentity(pairing) &&
      pairing.state !== 'connecting',
  );

  async function beginInvitation(url: string, consent: boolean) {
    if (!mounted || !canStartInvitation)
      throw new Error('Invitation setup is unavailable.');
    const epoch = pairingEpoch;
    const operation = ++invitationSequence;
    invitationBusy = true;
    try {
      const share = await createInvitation(url, consent);
      if (
        epoch !== pairingEpoch ||
        operation !== invitationSequence ||
        !pairingEnabled()
      )
        throw new Error('Invitation setup is no longer available.');
      return share;
    } finally {
      if (operation === invitationSequence) invitationBusy = false;
    }
  }

  async function openInvitation(link: string, url: string, consent: boolean) {
    if (!mounted || !canStartInvitation)
      throw new Error('Invitation setup is unavailable.');
    const epoch = pairingEpoch;
    const operation = ++invitationSequence;
    const sequence = ++requestSequence;
    invitationBusy = true;
    try {
      const value = await joinInvitation(link, url, consent);
      if (
        epoch !== pairingEpoch ||
        operation !== invitationSequence ||
        !pairingEnabled()
      )
        throw new Error('Invitation setup is no longer available.');
      if (
        sequence >= acceptedSequence &&
        (!hasPairingIdentity(pairing) || pairing.requestID === value.requestID)
      ) {
        acceptedSequence = sequence;
        pairing = value;
      }
    } finally {
      if (operation === invitationSequence) invitationBusy = false;
    }
  }

  async function stopInvitation() {
    if (!mounted || !native || status.storageState !== 'unlocked')
      throw new Error('Invitation cancellation is unavailable.');
    clearPairing();
    const epoch = pairingEpoch;
    const operation = ++invitationSequence;
    invitationBusy = true;
    try {
      const value = await cancelInvitation();
      if (
        epoch === pairingEpoch &&
        operation === invitationSequence &&
        pairingEnabled()
      ) {
        acceptedSequence = ++requestSequence;
        pairing = value;
      }
    } finally {
      if (operation === invitationSequence) {
        invitationBusy = false;
        startPairingPolling();
      }
    }
  }

  function startPairingPolling() {
    if (!pairingEnabled()) return;
    void refreshPairing();
    if (pairingTimer === undefined)
      pairingTimer = window.setInterval(() => void refreshPairing(), 1000);
  }

  async function refreshPairing() {
    if (!pairingEnabled() || activePoll !== undefined) return;
    const epoch = pairingEpoch;
    const sequence = ++requestSequence;
    activePoll = sequence;
    try {
      const value = await requestPairingStatus();
      if (
        epoch === pairingEpoch &&
        pairingEnabled() &&
        sequence >= acceptedSequence
      ) {
        acceptedSequence = sequence;
        if (
          value.requestID !== pairing.requestID ||
          value.state !== pairing.state
        )
          pairingNotice = '';
        pairing = value;
      }
    } catch {
      if (
        epoch === pairingEpoch &&
        pairingEnabled() &&
        sequence >= acceptedSequence
      ) {
        acceptedSequence = sequence;
        pairing = { ...unavailablePairing };
        pairingNotice = '';
      }
    } finally {
      if (activePoll === sequence) activePoll = undefined;
    }
  }

  async function decidePairing(
    selectedRequest: string,
    approve: boolean,
    fingerprint = '',
  ) {
    if (
      !pairingEnabled() ||
      selectedRequest !== pairing.requestID ||
      selectedRequest === '' ||
      (approve && pairing.state !== 'awaiting-approval') ||
      (actionRequest === selectedRequest && (approve || actionKind === 'deny'))
    )
      return;
    const epoch = pairingEpoch;
    const sequence = ++requestSequence;
    activeAction = sequence;
    actionRequest = selectedRequest;
    actionKind = approve ? 'approve' : 'deny';
    pairingNotice = '';
    try {
      const value = approve
        ? await approvePairing(selectedRequest, fingerprint)
        : await denyPairing(selectedRequest);
      if (
        epoch === pairingEpoch &&
        pairingEnabled() &&
        pairing.requestID === selectedRequest &&
        sequence >= acceptedSequence
      ) {
        acceptedSequence = sequence;
        pairing = value;
      }
    } catch {
      if (
        epoch === pairingEpoch &&
        pairingEnabled() &&
        pairing.requestID === selectedRequest &&
        sequence >= acceptedSequence
      ) {
        acceptedSequence = sequence;
        pairingNotice = approve
          ? 'Pairing approval could not be confirmed. Check the request before trying again.'
          : 'Pairing cancellation could not be confirmed. Try again or lock storage to cancel.';
      }
    } finally {
      if (activeAction === sequence) {
        actionRequest = '';
        actionKind = '';
      }
    }
  }

  async function refresh(method: StatusMethod = 'GetStatus') {
    if (busy) return;
    if (method === 'LockStorage') clearPairing();
    busy = true;
    notice = '';
    try {
      status = await requestStatus(method);
      if (status.storageState !== 'unlocked') clearPairing();
    } catch {
      status = { ...unavailableStatus };
      clearPairing();
      notice =
        method === 'UnlockStorage'
          ? 'Local storage could not be unlocked. Check that your OS credential store is available, then try again.'
          : 'The desktop could not report its status. Local actions are unavailable. Try again.';
    } finally {
      busy = false;
      if (status.storageState === 'unlocked') startPairingPolling();
    }
  }

  function updatePreferences(value: Preferences) {
    preferences = value;
    applyPreferences(value);
    if (!savePreferences(value))
      notice =
        'Your appearance changed for this session. This device could not save the preference.';
  }

  onMount(() => {
    mounted = true;
    const activateDesktop = () => {
      if (native || !hasDesktopBridge()) return;
      native = true;
      void refresh();
    };
    // Wails injects configuration after navigation/load completion. Subscribe
    // before checking so both early and late injection reach the same path.
    window.addEventListener('wails:runtime-config-ready', activateDesktop);
    activateDesktop();
    const media = window.matchMedia('(prefers-color-scheme: dark)');
    const updateSystemTheme = () => applyPreferences(preferences);
    media.addEventListener('change', updateSystemTheme);
    const keyboard = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key === ',') {
        event.preventDefault();
        settings.show();
      }
    };
    window.addEventListener('keydown', keyboard);
    return () => {
      mounted = false;
      clearPairing();
      window.removeEventListener('wails:runtime-config-ready', activateDesktop);
      media.removeEventListener('change', updateSystemTheme);
      window.removeEventListener('keydown', keyboard);
    };
  });

  const storageLabel = $derived(
    status.storageState === 'unlocked'
      ? 'Local storage unlocked'
      : status.storageState === 'locked'
        ? 'Local storage locked'
        : 'Local storage unavailable',
  );
</script>

<svelte:head><title>WhatsUp — Your conversations, closer</title></svelte:head>

<a href="#conversation" class="skip-link">Skip to conversation</a>
<div class="app-shell">
  <nav class="navigation-rail" aria-label="Main navigation">
    <a href="#conversation" class="brand-mark" aria-label="WhatsUp home"
      ><MessageCircle size={25} strokeWidth={2.2} /><span class="brand-spark"
      ></span></a
    >
    <div class="rail-navigation">
      <button
        class="rail-button active"
        aria-label="Conversations"
        aria-current="page"
        ><MessageCircle size={23} strokeWidth={1.7} /></button
      >
      <button
        class="rail-button"
        aria-label="About contacts"
        onclick={() => aboutDialog.showModal()}
        ><Users size={23} strokeWidth={1.7} /></button
      >
    </div>
    <div class="rail-bottom">
      <button
        class="rail-button"
        aria-label="About WhatsUp"
        onclick={() => aboutDialog.showModal()}
        ><CircleHelp size={22} strokeWidth={1.7} /></button
      >
      <button
        class="rail-button"
        aria-label="Open settings"
        title="Settings (Ctrl+,)"
        onclick={() => settings.show()}
        ><Settings size={22} strokeWidth={1.7} /></button
      >
      <div class="device-avatar" aria-label="This device"><span>Y</span></div>
    </div>
  </nav>

  <aside class="contacts-panel" aria-labelledby="contacts-heading">
    <header class="contacts-header">
      <div>
        <p class="eyebrow">A LITTLE CLOSER</p>
        <h1 id="contacts-heading">
          WhatsUp<span class="wordmark-dot">.</span>
        </h1>
      </div>
      <button
        class="icon-button"
        aria-label="New conversation"
        disabled={!canStartInvitation}
        onclick={() => invitationDialog.show()}
        title="Unlock storage to create or join a private invitation"
        ><CirclePlus size={22} strokeWidth={1.7} /></button
      >
    </header>
    <label class="search-box"
      ><Search size={17} /><span class="sr-only">Search contacts</span><input
        type="search"
        placeholder="Search conversations"
        bind:value={search}
        maxlength="100"
      />{#if search}<button
          aria-label="Clear search"
          class="clear-search"
          onclick={() => (search = '')}><X size={15} /></button
        >{/if}</label
    >
    <div class="contacts-list-label">
      <span>CONVERSATIONS</span><span class="count">0</span>
    </div>
    <div class="empty-contacts">
      <div class="contact-empty-icon">
        <Users size={26} strokeWidth={1.4} /><span></span>
      </div>
      <h2>{search ? 'No matching conversations' : 'Your people go here'}</h2>
      <p>
        {search
          ? `No conversations match “${search}”.`
          : 'A quiet space for the people you choose.'}
      </p>
      {#if search}<button class="text-button" onclick={() => (search = '')}
          >Clear search</button
        >{:else}<span class="subtle-pill"
          ><LockKeyhole size={12} /> Invite only</span
        >{/if}
    </div>
    <div class="sidebar-note">
      <Shield size={17} strokeWidth={1.6} />
      <p>One conversation.<br /><strong>Just the two of you.</strong></p>
    </div>
    <footer class="device-footer">
      <div class="mini-device"><span>Y</span></div>
      <div>
        <strong>Your device</strong><span
          >{native ? 'Desktop development build' : 'Browser preview'}</span
        >
      </div>
      <button
        class="icon-button"
        aria-label="Device settings"
        onclick={() => settings.show()}><MoreHorizontal size={19} /></button
      >
    </footer>
  </aside>

  <main id="conversation" class="conversation-panel" tabindex="-1">
    <header class="conversation-header">
      <div class="header-identity">
        <div class="header-icon">
          <MessageCircle size={20} strokeWidth={1.6} />
        </div>
        <div>
          <h2>Your conversations, closer</h2>
          <p>A private space. A familiar face.</p>
        </div>
      </div>
      <button
        class="icon-button"
        aria-label="About this build"
        onclick={() => aboutDialog.showModal()}
        ><Info size={20} strokeWidth={1.7} /></button
      >
    </header>

    {#if notice}
      <div class="notice" role="alert">
        <Info size={18} />
        <p>{notice}</p>
        {#if native}<button
            class="text-button"
            onclick={() => refresh()}
            disabled={busy}>Try again</button
          >{/if}<button
          class="icon-button"
          aria-label="Dismiss notice"
          onclick={() => (notice = '')}><X size={16} /></button
        >
      </div>
    {/if}

    {#if pairing.state === 'pending' || pairing.state === 'reconnect-required'}
      <div class="notice" role="status">
        <Shield size={18} />
        <p>
          {pairingNotice ||
            (pairing.state === 'reconnect-required'
              ? 'Contact approved. Reconnection and messaging are not ready in this build. Discard saved setup to use a new invitation.'
              : 'Contact setup is incomplete. No conversation is active. Discard this setup to use a new invitation.')}
        </p>
        <button
          class="text-button"
          disabled={actionRequest === pairing.requestID &&
            actionKind === 'deny'}
          onclick={() => void decidePairing(pairing.requestID, false)}
          >Discard setup</button
        >
      </div>
    {/if}
    {#if pairing.state === 'completed'}
      <div class="notice" role="status">
        <Shield size={18} />
        <p>Contact approved. Messaging is not ready yet.</p>
      </div>
    {/if}

    <section class="welcome-space" aria-labelledby="welcome-heading">
      <div class="welcome-art" aria-hidden="true">
        <div class="orbit orbit-one"></div>
        <div class="orbit orbit-two"></div>
        <div class="art-dot art-dot-one"></div>
        <div class="art-dot art-dot-two"></div>
        <div class="art-dash"></div>
        <div class="bubble bubble-back">
          <span></span><span></span><span></span>
        </div>
        <div class="bubble bubble-front">
          <MessageCircle size={51} strokeWidth={1.25} /><span
            class="bubble-check"><Check size={15} strokeWidth={2.4} /></span
          >
        </div>
      </div>
      <span class="welcome-kicker">GOOD CONVERSATIONS START HERE</span>
      <h2 id="welcome-heading">
        Less noise.<br />More <span>connection.</span>
      </h2>
      <p class="welcome-description">
        A space for real conversations with someone you trust.<br
          class="desktop-break"
        /> Just you, them, and what’s on your mind.
      </p>
      <div class="welcome-action">
        <button
          class="primary-button"
          disabled={!canStartInvitation}
          onclick={() => invitationDialog.show()}
          title="Unlock storage to create or join a private invitation"
          ><CirclePlus size={18} /> Start a conversation <ArrowUpRight
            size={16}
          /></button
        >
        <p>
          {native && status.storageState === 'unlocked'
            ? 'Create or join a private invitation. Messaging and files are not available yet.'
            : 'Unlock storage in the desktop app to pair. Messaging and files are not available yet.'}
        </p>
      </div>
      <div class="welcome-details">
        <span><Users size={15} /> Always one to one</span><span
          class="detail-divider"
        ></span><span><LockKeyhole size={14} /> You choose who joins</span>
      </div>
    </section>

    <div class="composer-area">
      <div class="composer" aria-label="Message composer unavailable">
        <button
          class="icon-button"
          aria-label="Attach file unavailable"
          disabled><Paperclip size={21} strokeWidth={1.6} /></button
        ><textarea
          aria-label="Message unavailable until messaging is implemented"
          placeholder="Your next conversation starts here…"
          disabled
          rows="1"></textarea><button
          class="icon-button emoji-button"
          aria-label="Emoji unavailable"
          disabled><Smile size={21} strokeWidth={1.6} /></button
        ><button
          class="send-button"
          aria-label="Send message unavailable"
          disabled><Send size={19} strokeWidth={1.7} /></button
        >
      </div>
      <p class="composer-caption">
        No contact selected. Nothing is sent or queued.
      </p>
    </div>
    <footer class="network-footer">
      <span
        ><WifiOff size={13} />
        {native ? 'No conversation session' : 'No network session'}</span
      ><span class="footer-divider"></span><span
        ><LockKeyhole size={12} /> {storageLabel}</span
      ><span class="build-label"
        >{native
          ? `Development · v${status.version}`
          : 'Browser preview · desktop actions unavailable'}</span
      >
    </footer>
  </main>
</div>

<SettingsDialog
  bind:this={settings}
  {status}
  {preferences}
  {native}
  {busy}
  onpreferences={updatePreferences}
  onstorage={() =>
    refresh(
      status.storageState === 'unlocked' ? 'LockStorage' : 'UnlockStorage',
    )}
/>

<PairingDialog
  status={pairing}
  busy={actionRequest === pairing.requestID && actionKind !== ''}
  denying={actionRequest === pairing.requestID && actionKind === 'deny'}
  error={pairingNotice}
  onapprove={(requestID, fingerprint) =>
    void decidePairing(requestID, true, fingerprint)}
  ondeny={(requestID) => void decidePairing(requestID, false)}
/>

<InvitationDialog
  bind:this={invitationDialog}
  enabled={invitationEnabled}
  {pairing}
  oncreate={beginInvitation}
  onjoin={openInvitation}
  oncancel={stopInvitation}
/>

<dialog
  bind:this={aboutDialog}
  aria-labelledby="about-title"
  class="settings-dialog about-dialog"
>
  <div class="dialog-heading">
    <div class="dialog-title">
      <div class="small-icon"><MessageCircle size={19} /></div>
      <div>
        <p class="eyebrow">SAY SOMETHING REAL</p>
        <h2 id="about-title">About WhatsUp</h2>
      </div>
    </div>
    <button
      class="icon-button"
      aria-label="Close about"
      onclick={() => aboutDialog.close()}><X size={19} /></button
    >
  </div>
  <div class="dialog-body">
    <p>
      WhatsUp is being built for private, one-to-one conversations on Windows
      and Linux.
    </p>
    <div class="build-notice">
      <Info size={18} />
      <p>
        This is a development build. Pairing requires your chosen connection
        service and explicit consent. Chat, attachments and strict direct-only
        delivery are not available yet.
      </p>
    </div>
    <p class="muted small">
      Both devices need to be running to pair. Your selected connection service
      learns your IP address and availability and may relay encrypted pairing
      setup. Nothing connects automatically. This build does not deliver
      messages.
    </p>
    <p class="muted small">
      WhatsUp keeps redacted troubleshooting logs locally and has no analytics
      or remote crash reporting. On Windows it uses the system WebView2 with its
      own cookies and storage, separate from Edge. Optional WebView2 diagnostics
      follow Windows privacy settings; required Microsoft diagnostics may still
      occur. Linux uses WebKitGTK, whose diagnostic behavior is different.
    </p>
    <button
      class="secondary-button"
      onclick={() => {
        aboutDialog.close();
        settings.show();
      }}>Personalize your space <ChevronDown size={15} /></button
    >
  </div>
  <div class="dialog-footer">
    <span class="muted small">Version {status.version}</span><button
      class="primary-button compact"
      onclick={() => aboutDialog.close()}>Got it</button
    >
  </div>
</dialog>
