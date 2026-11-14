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
    Wifi,
    WifiOff,
    X,
  } from '@lucide/svelte';
  import SettingsDialog from './lib/components/SettingsDialog.svelte';
  import PairingDialog from './lib/components/PairingDialog.svelte';
  import InvitationDialog from './lib/components/InvitationDialog.svelte';
  import ForgetContactDialog from './lib/components/ForgetContactDialog.svelte';
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
    connectContact,
    forgetContact,
    requestConversation,
    sendMessage,
    unavailableConversation,
    validMessageText,
    type ConversationSnapshot,
  } from './lib/messaging';
  import {
    applyPreferences,
    readPreferences,
    savePreferences,
    type Preferences,
  } from './lib/themes/preferences';
  import {
    requestProfile,
    saveProfile,
    saveContactName,
    type LocalProfile,
  } from './lib/profile';

  let status: Status = $state({ ...unavailableStatus });
  let preferences = $state(readPreferences());
  let native = $state(false);
  let busy = $state(false);
  let notice = $state('');
  let search = $state('');
  let settings: SettingsDialog;
  let settingsOpen = $state(false);
  let profile: LocalProfile | null = $state(null);
  let profileLoading = $state(false);
  let profileSaving = $state(false);
  let profileNotice = $state('');
  let profileEpoch = 0;
  let contactSaving = $state(false);
  let contactNotice = $state('');
  let aboutDialog: HTMLDialogElement;
  let invitationDialog: InvitationDialog;
  let forgetDialog: ForgetContactDialog;
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
  let conversation: ConversationSnapshot = $state({
    ...unavailableConversation,
  });
  let conversationNotice = $state('');
  let draft = $state('');
  let sending = $state(false);
  let connecting = $state(false);
  let forgetting = $state(false);
  let conversationEpoch = 0;
  let conversationPoll: number | undefined;
  let conversationTimer: number | undefined;
  let conversationSequence = 0;

  function clearConversation() {
    if (conversationTimer !== undefined)
      window.clearInterval(conversationTimer);
    conversationTimer = undefined;
    conversationEpoch += 1;
    conversationPoll = undefined;
    conversation = { ...unavailableConversation, messages: [] };
    conversationNotice = '';
    draft = '';
    search = '';
    sending = false;
    connecting = false;
    contactSaving = false;
    contactNotice = '';
  }

  function clearProfile() {
    profileEpoch += 1;
    profile = null;
    profileLoading = false;
    profileSaving = false;
    profileNotice = '';
  }

  async function refreshLocalProfile() {
    if (!conversationEnabled() || profileLoading || profileSaving) return;
    const epoch = profileEpoch;
    profileLoading = true;
    try {
      const value = await requestProfile();
      if (epoch === profileEpoch && conversationEnabled()) profile = value;
    } catch {
      if (epoch === profileEpoch && conversationEnabled()) {
        profile = null;
        profileNotice =
          'Your profile could not be loaded. Close settings and try again.';
      }
    } finally {
      if (epoch === profileEpoch) profileLoading = false;
    }
  }

  async function updateProfile(value: LocalProfile) {
    if (
      !conversationEnabled() ||
      profileLoading ||
      profileSaving ||
      profile === null
    )
      return;
    const epoch = profileEpoch;
    profileSaving = true;
    profileNotice = '';
    try {
      const saved = await saveProfile(value);
      if (epoch === profileEpoch && conversationEnabled()) {
        profile = saved;
        profileNotice = 'Profile saved on this device.';
      }
    } catch {
      if (epoch === profileEpoch && conversationEnabled())
        profileNotice =
          'Your profile could not be saved. Unlock storage and try again.';
    } finally {
      if (epoch === profileEpoch) profileSaving = false;
    }
  }

  async function updateContactName(selectedContact: string, name: string) {
    if (
      !conversationEnabled() ||
      contactSaving ||
      selectedContact !== conversation.contactID ||
      selectedContact === ''
    )
      return;
    const epoch = conversationEpoch;
    contactSaving = true;
    contactNotice = '';
    try {
      const value = await saveContactName(selectedContact, name);
      if (acceptConversation(value, epoch, selectedContact))
        contactNotice = 'Contact name saved only on this device.';
    } catch {
      if (
        epoch === conversationEpoch &&
        conversationEnabled() &&
        selectedContact === conversation.contactID
      )
        contactNotice =
          'The contact name could not be saved. Check the current contact and try again.';
    } finally {
      if (
        epoch === conversationEpoch &&
        selectedContact === conversation.contactID
      )
        contactSaving = false;
    }
  }

  function settingsChanged(open: boolean) {
    settingsOpen = open;
    if (open) void refreshLocalProfile();
  }

  function openSettings() {
    if (forgetDialog?.isOpen()) return;
    if (aboutDialog?.open) aboutDialog.close();
    settings.show();
  }

  function conversationEnabled() {
    return mounted && native && !busy && status.storageState === 'unlocked';
  }

  function acceptConversation(
    value: ConversationSnapshot,
    epoch: number,
    selectedContact?: string,
  ): boolean {
    if (
      epoch !== conversationEpoch ||
      !conversationEnabled() ||
      (selectedContact !== undefined &&
        (selectedContact !== conversation.contactID ||
          value.contactID !== selectedContact)) ||
      value.revision < conversation.revision
    )
      return false;
    if (conversation.contactID !== value.contactID) {
      contactSaving = false;
      contactNotice = '';
    }
    conversation = value;
    return true;
  }

  async function refreshConversation() {
    if (!conversationEnabled() || conversationPoll !== undefined) return;
    const epoch = conversationEpoch;
    const sequence = ++conversationSequence;
    conversationPoll = sequence;
    try {
      acceptConversation(await requestConversation(), epoch);
    } catch {
      if (epoch === conversationEpoch && conversationEnabled()) {
        conversation = { ...unavailableConversation, messages: [] };
        conversationNotice =
          'Conversation status could not be verified. No message will be sent. Try again or disconnect and lock.';
      }
    } finally {
      if (conversationPoll === sequence) conversationPoll = undefined;
    }
  }

  function startConversationPolling() {
    if (!conversationEnabled()) return;
    void refreshConversation();
    if (conversationTimer === undefined)
      conversationTimer = window.setInterval(
        () => void refreshConversation(),
        1000,
      );
  }

  async function connectConversation() {
    if (!conversationEnabled() || !conversation.canConnect || connecting)
      return;
    const epoch = conversationEpoch;
    const selectedContact = conversation.contactID;
    connecting = true;
    conversationNotice = '';
    try {
      acceptConversation(
        await connectContact(selectedContact),
        epoch,
        selectedContact,
      );
    } catch {
      if (
        epoch === conversationEpoch &&
        conversationEnabled() &&
        conversation.contactID === selectedContact
      )
        conversationNotice =
          'The connection could not be started. Your saved messages remain on this device. Try Connect again.';
    } finally {
      if (epoch === conversationEpoch) connecting = false;
    }
  }

  async function queueMessage() {
    if (
      !conversationEnabled() ||
      !conversation.canSend ||
      sending ||
      !validMessageText(draft)
    )
      return;
    const epoch = conversationEpoch;
    const selectedContact = conversation.contactID;
    const text = draft;
    sending = true;
    conversationNotice = '';
    try {
      const value = await sendMessage(selectedContact, text);
      if (
        epoch === conversationEpoch &&
        conversationEnabled() &&
        conversation.contactID === selectedContact
      ) {
        acceptConversation(value, epoch, selectedContact);
        if (draft === text) draft = '';
      }
    } catch {
      if (
        epoch === conversationEpoch &&
        conversationEnabled() &&
        conversation.contactID === selectedContact
      )
        conversationNotice =
          'The message could not be saved to the local queue. Your draft is still here; try again.';
    } finally {
      if (epoch === conversationEpoch) sending = false;
    }
  }

  function showForgetContact() {
    if (!conversationEnabled() || conversation.contactID === '') return;
    settings.close();
    forgetDialog.show();
  }

  async function forgetConversation(selectedContact: string) {
    if (
      !conversationEnabled() ||
      forgetting ||
      selectedContact === '' ||
      selectedContact !== conversation.contactID
    )
      return;
    // Retire all old polls and mutations before asking Go to remove the contact.
    // A response for the old conversation must never reopen its history or draft.
    clearPairing();
    clearConversation();
    clearProfile();
    const epoch = conversationEpoch;
    forgetting = true;
    busy = true;
    notice = '';
    let failed = false;
    try {
      const value = await forgetContact(selectedContact);
      if (epoch === conversationEpoch && mounted) {
        conversation = value;
        search = '';
      }
    } catch {
      failed = true;
    } finally {
      if (epoch === conversationEpoch && mounted) {
        forgetting = false;
        busy = false;
        forgetDialog.close();
        if (failed) {
          // The backend may lock after an uncertain storage write. Verify its
          // state afresh instead of restoring any snapshot from before removal.
          await refresh();
          notice =
            'Contact removal could not be confirmed. Check the saved contact before trying again. No new connection was started.';
        } else {
          void refreshLocalProfile();
          startPairingPolling();
          startConversationPolling();
        }
      }
    }
  }

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
      conversation.contactID === '' &&
      pairing.state !== 'connecting',
  );

  const hasContact = $derived(conversation.contactID !== '');
  const contactName = $derived(
    conversation.localName ||
      conversation.peerDisplayName ||
      'Approved contact',
  );
  const ownName = $derived.by(() => profile?.displayName || 'Your device');
  const savedPairingIsConversation = $derived(
    hasContact &&
      pairing.peerFingerprint === conversation.peerFingerprint &&
      pairing.state === 'reconnect-required',
  );
  const contactMatchesSearch = $derived(
    search === '' ||
      contactName.toLowerCase().includes(search.toLowerCase()) ||
      conversation.peerFingerprint.includes(search.toLowerCase()),
  );
  const connectionLabel = $derived(
    conversation.state === 'connected'
      ? 'Connected'
      : conversation.state === 'connecting' || connecting
        ? 'Connecting…'
        : conversation.state === 'identity-changed'
          ? 'Identity could not be verified'
          : conversation.state === 'update-required'
            ? 'Compatible app update needed'
            : conversation.state === 'authentication-failed'
              ? 'Authentication needs attention'
              : conversation.state === 'failed'
                ? 'Connection unavailable'
                : conversation.state === 'capacity-reached'
                  ? 'Local message storage full'
                  : conversation.state === 'peer-capacity-reached'
                    ? 'Contact message storage full'
                    : 'Offline',
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

  async function refresh(
    method: StatusMethod = 'GetStatus',
    connectAfterUnlock = false,
  ) {
    if (busy) return;
    if (method === 'LockStorage' || method === 'UnlockStorage') {
      clearPairing();
      clearConversation();
      clearProfile();
    }
    busy = true;
    notice = '';
    try {
      status = await requestStatus(method);
      if (status.storageState !== 'unlocked') {
        clearPairing();
        clearConversation();
        clearProfile();
      }
    } catch {
      status = { ...unavailableStatus };
      clearPairing();
      clearConversation();
      clearProfile();
      notice =
        method === 'UnlockStorage'
          ? 'Local storage could not be unlocked. Check that your OS credential store is available, then try again.'
          : 'The desktop could not report its status. Local actions are unavailable. Try again.';
    } finally {
      busy = false;
      if (status.storageState === 'unlocked') {
        void refreshLocalProfile();
        startPairingPolling();
        if (method === 'UnlockStorage' && connectAfterUnlock) {
          const epoch = conversationEpoch;
          await refreshConversation();
          if (epoch === conversationEpoch && conversationEnabled())
            await connectConversation();
        }
        startConversationPolling();
      }
    }
  }

  function storageAction() {
    const unlock = status.storageState !== 'unlocked';
    void refresh(unlock ? 'UnlockStorage' : 'LockStorage', unlock);
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
        openSettings();
      }
    };
    window.addEventListener('keydown', keyboard);
    return () => {
      mounted = false;
      clearPairing();
      clearConversation();
      clearProfile();
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

<a href="#conversation" class="skip-link" hidden={settingsOpen}
  >Skip to conversation</a
>
<div class="app-shell" hidden={settingsOpen}>
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
        onclick={openSettings}><Settings size={22} strokeWidth={1.7} /></button
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
      <span>CONVERSATIONS</span><span class="count">{hasContact ? 1 : 0}</span>
    </div>
    {#if hasContact && contactMatchesSearch}
      <div class="approved-contact" aria-label="Approved contact">
        <div class="contact-avatar"><Shield size={21} /></div>
        <div>
          <strong>{contactName}</strong>
          <span>{connectionLabel}</span>
          <code>{conversation.peerFingerprint.slice(0, 12)}…</code>
        </div>
      </div>
    {:else}
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
    {/if}
    <div class="sidebar-note">
      <Shield size={17} strokeWidth={1.6} />
      <p>One conversation.<br /><strong>Just the two of you.</strong></p>
    </div>
    <footer class="device-footer">
      <div class="mini-device"><span>Y</span></div>
      <div>
        <strong>{ownName}</strong><span
          >{native ? 'Desktop development build' : 'Browser preview'}</span
        >
      </div>
      <button
        class="icon-button"
        aria-label="Device settings"
        onclick={openSettings}><MoreHorizontal size={19} /></button
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
          <h2>
            {hasContact ? contactName : 'Your conversations, closer'}
          </h2>
          <p>
            {hasContact ? connectionLabel : 'A private space. A familiar face.'}
          </p>
        </div>
      </div>
      <div class="conversation-actions">
        {#if hasContact && conversation.canConnect}
          <button
            class="secondary-button compact"
            disabled={connecting || busy}
            onclick={() => void connectConversation()}
            >{connecting ? 'Connecting…' : 'Connect'}</button
          >
        {/if}
        {#if hasContact}
          <button
            class="secondary-button compact"
            disabled={busy}
            onclick={showForgetContact}>Forget contact…</button
          >
        {/if}
        <button
          class="secondary-button compact"
          disabled={!native || busy}
          onclick={storageAction}
          >{status.storageState === 'unlocked'
            ? 'Disconnect and lock'
            : 'Unlock and connect'}</button
        >
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

    {#if conversationNotice}
      <div class="notice" role="alert">
        <Info size={18} />
        <p>{conversationNotice}</p>
        <button
          class="icon-button"
          aria-label="Dismiss conversation notice"
          onclick={() => (conversationNotice = '')}><X size={16} /></button
        >
      </div>
    {/if}

    {#if pairing.state === 'pending' || (pairing.state === 'reconnect-required' && !savedPairingIsConversation)}
      <div class="notice" role="status">
        <Shield size={18} />
        <p>
          {pairingNotice ||
            (pairing.state === 'reconnect-required'
              ? 'This saved setup cannot reconnect yet. Discard it to use a new invitation.'
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
    {#if pairing.state === 'completed' && !hasContact}
      <div class="notice" role="status">
        <Shield size={18} />
        <p>Contact approved. Waiting for the saved conversation.</p>
      </div>
    {/if}

    {#if hasContact}
      <section class="message-space" aria-label="Conversation">
        {#if conversation.state === 'identity-changed'}
          <p class="connection-notice" role="alert">
            Your contact's identity could not be verified. The connection
            stopped; your saved contact was not changed.
          </p>
        {:else if conversation.state === 'update-required'}
          <p class="connection-notice" role="alert">
            Your saved contact is using an incompatible messaging protocol.
            Update both apps to compatible builds, then choose Connect. Your
            saved contact and queued messages are retained.
          </p>
        {:else if conversation.state === 'authentication-failed'}
          <p class="connection-notice" role="alert">
            The saved contact could not authenticate. Check that both apps use
            compatible builds and their device clocks are correct. WhatsUp will
            retry; your saved contact has not been replaced.
          </p>
        {:else if conversation.state === 'capacity-reached'}
          <p class="connection-notice" role="alert">
            This preview's local message storage is full. New messages cannot be
            saved. Existing history and queued messages are retained.
          </p>
        {:else if conversation.state === 'peer-capacity-reached'}
          <p class="connection-notice" role="alert">
            Your contact's message storage is full. Automatic retries stopped.
            Your undelivered messages remain queued on this device; they have
            not been marked delivered.
          </p>
        {:else if conversation.state === 'connecting'}
          <p class="connection-notice" role="status">
            Waiting for your contact. Both devices need to be unlocked and
            connected. WhatsUp will keep trying while this connection is
            enabled.
          </p>
        {:else if conversation.state !== 'connected'}
          <p class="connection-notice" role="status">
            {conversation.canSend
              ? 'You are offline. Messages stay encrypted on this device until your contact reconnects.'
              : 'Connect to authenticate your saved contact before opening this conversation.'}
          </p>
        {/if}
        <div
          class="message-history"
          role="log"
          aria-label="Messages"
          aria-live="polite"
          aria-relevant="additions text"
        >
          {#each conversation.messages as message (message.id)}
            <article
              class="message-bubble"
              class:outgoing={message.direction === 'outgoing'}
              aria-label={message.direction === 'outgoing'
                ? 'Your message'
                : 'Contact message'}
            >
              <p class="message-text" dir="auto">{message.text}</p>
              <footer>
                <time datetime={message.createdAt}
                  >{new Date(message.createdAt).toLocaleTimeString([], {
                    hour: '2-digit',
                    minute: '2-digit',
                  })}</time
                >
                {#if message.direction === 'outgoing'}
                  <span class="delivery-status"
                    >{message.delivered ? 'Delivered' : 'Queued'}</span
                  >
                {/if}
              </footer>
            </article>
          {:else}
            <p class="conversation-empty">
              {conversation.canSend
                ? 'Your conversation is ready. Write the first message.'
                : 'Your contact is saved. Messages become available after authentication.'}
            </p>
          {/each}
        </div>
      </section>
    {:else}
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
              ? 'Create or join a private invitation to approve your contact.'
              : 'Choose Unlock and connect to reconnect or start a private invitation in the desktop app.'}
          </p>
        </div>
        <div class="welcome-details">
          <span><Users size={15} /> Always one to one</span><span
            class="detail-divider"
          ></span><span><LockKeyhole size={14} /> You choose who joins</span>
        </div>
      </section>
    {/if}

    <div class="composer-area">
      <div
        class="composer"
        aria-label={hasContact
          ? 'Message composer'
          : 'Message composer unavailable'}
      >
        <button
          class="icon-button"
          aria-label="Attach file unavailable"
          disabled><Paperclip size={21} strokeWidth={1.6} /></button
        ><textarea
          aria-label={hasContact
            ? 'Message'
            : 'Message unavailable without an approved contact'}
          placeholder={conversation.canSend
            ? 'Write a message…'
            : hasContact
              ? 'Connect to open this conversation…'
              : 'Your next conversation starts here…'}
          disabled={!conversation.canSend || busy}
          bind:value={draft}
          maxlength={16 * 1024}
          onkeydown={(event) => {
            if (
              event.key === 'Enter' &&
              !event.shiftKey &&
              !event.isComposing
            ) {
              event.preventDefault();
              void queueMessage();
            }
          }}
          rows="1"></textarea><button
          class="icon-button emoji-button"
          aria-label="Emoji unavailable"
          disabled><Smile size={21} strokeWidth={1.6} /></button
        ><button
          class="send-button"
          aria-label={hasContact ? 'Send message' : 'Send message unavailable'}
          disabled={!conversation.canSend ||
            sending ||
            busy ||
            !validMessageText(draft)}
          onclick={() => void queueMessage()}
          ><Send size={19} strokeWidth={1.7} /></button
        >
      </div>
      <p class="composer-caption">
        {hasContact
          ? conversation.canSend
            ? 'Enter to send · Shift+Enter for a new line. Queued messages stay here until your contact receives them.'
            : 'Connect to authenticate your contact. Nothing is sent before verification.'
          : 'No contact selected. Nothing is sent or queued.'}
      </p>
    </div>
    <footer class="network-footer">
      <span
        >{#if conversation.state === 'connected'}<Wifi
            size={13}
          />{:else}<WifiOff size={13} />{/if}
        {hasContact
          ? connectionLabel
          : native
            ? 'No conversation session'
            : 'No network session'}</span
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
  onstorage={storageAction}
  canForget={hasContact && status.storageState === 'unlocked'}
  onforget={showForgetContact}
  onopenchange={settingsChanged}
  {profile}
  {profileLoading}
  {profileSaving}
  {profileNotice}
  onsaveprofile={(value) => void updateProfile(value)}
  {conversation}
  {contactSaving}
  {contactNotice}
  onsavecontact={(contactID, name) => void updateContactName(contactID, name)}
/>

<ForgetContactDialog
  bind:this={forgetDialog}
  contactID={conversation.contactID}
  enabled={native && !busy && status.storageState === 'unlocked'}
  busy={forgetting}
  onconfirm={(contactID) => void forgetConversation(contactID)}
/>

<PairingDialog
  suspended={settingsOpen}
  status={pairing}
  busy={actionRequest === pairing.requestID && actionKind !== ''}
  denying={actionRequest === pairing.requestID && actionKind === 'deny'}
  error={pairingNotice}
  onapprove={(requestID, fingerprint) =>
    void decidePairing(requestID, true, fingerprint)}
  ondeny={(requestID) => void decidePairing(requestID, false)}
/>

<InvitationDialog
  suspended={settingsOpen}
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
        service and explicit consent. Text messaging uses approved contact
        credentials. Attachments and strict direct-only delivery are not
        available yet.
      </p>
    </div>
    <p class="muted small">
      Both devices need to be running to exchange messages. Your selected
      connection service learns your IP address and availability and may relay
      encrypted traffic. Unlock and connect starts automatic retries to your
      saved contact; Connect can retry a stopped connection. Opening the app
      starts no network connection. Disconnect and lock stops messaging and
      locks local data while keeping your saved contact and conversation.
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
        openSettings();
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

<style>
  .approved-contact {
    display: flex;
    align-items: center;
    gap: 12px;
    margin: 22px 15px;
    padding: 15px 12px;
    border: 1px solid var(--border);
    border-radius: 11px;
    background: var(--surface);
  }
  .contact-avatar {
    display: grid;
    place-items: center;
    flex-shrink: 0;
    width: 39px;
    height: 39px;
    color: var(--accent);
    background: var(--accent-soft);
    border-radius: 12px;
  }
  .approved-contact strong,
  .approved-contact span,
  .approved-contact code {
    display: block;
  }
  .approved-contact strong {
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .approved-contact > div:last-child {
    min-width: 0;
  }
  .approved-contact span,
  .approved-contact code {
    margin-top: 5px;
    font-size: 10px;
    color: var(--muted);
  }
  .conversation-actions {
    display: flex;
    flex-wrap: wrap;
    justify-content: flex-end;
    gap: 8px;
    margin-left: auto;
    margin-right: 14px;
  }
  @media (max-width: 740px) {
    .conversation-actions {
      order: 3;
      width: 100%;
      justify-content: flex-start;
      margin: 0;
    }
  }
  .message-space {
    display: flex;
    flex: 1;
    min-height: 0;
    flex-direction: column;
    overflow: hidden;
  }
  .connection-notice {
    margin: 18px 30px 0;
    padding: 11px 14px;
    border: 1px solid var(--border-soft);
    border-radius: 9px;
    font-size: 11px;
    line-height: 1.7;
    color: var(--muted);
  }
  .message-history {
    display: flex;
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    flex-direction: column;
    gap: 13px;
    padding: 24px 30px;
  }
  .message-bubble {
    max-width: min(82%, 620px);
    align-self: flex-start;
    padding: 12px 15px 9px;
    border: 1px solid var(--border);
    border-radius: 12px;
    background: var(--surface);
  }
  .message-bubble.outgoing {
    align-self: flex-end;
    background: var(--accent-soft);
  }
  .message-text {
    color: var(--text);
    font-size: 13px;
    line-height: 1.7;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    unicode-bidi: isolate;
  }
  .message-bubble footer {
    display: flex;
    justify-content: flex-end;
    gap: 10px;
    margin-top: 7px;
    font-size: 9px;
    color: var(--muted);
  }
  .conversation-empty {
    margin: auto;
    max-width: 310px;
    text-align: center;
    font-size: 12px;
    line-height: 1.8;
    color: var(--muted);
  }
</style>
