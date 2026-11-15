<script lang="ts">
  import { onMount, tick } from 'svelte';
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
  import HistoryDialog from './lib/components/HistoryDialog.svelte';
  import ImageViewer from './lib/components/ImageViewer.svelte';
  import { createNotificationActivation } from './lib/state/notification-activation';
  import { createHistoryActions } from './lib/state/history';
  import { createAppPreferences } from './lib/state/preferences';
  import { createSession } from './lib/state/session';
  import ContactList from './lib/components/ContactList.svelte';
  import AttachmentPanel from './lib/components/AttachmentPanel.svelte';
  import PassphraseDialog from './lib/components/PassphraseDialog.svelte';
  import RecoveryDialog from './lib/components/RecoveryDialog.svelte';
  import { createSecurity, type SecurityOperation } from './lib/state/security';
  import { createAttachments } from './lib/state/attachments';
  import { createRelayChange } from './lib/state/relay';
  import RelayApprovalDialog from './lib/components/RelayApprovalDialog.svelte';
  import {
    createContacts,
    selectedConversation,
    contactLabel,
    connectionLabel as describeConnection,
  } from './lib/state/contacts';
  import { type Status, type StatusMethod } from './lib/backend';
  import { hasPairingIdentity } from './lib/pairing';
  import { createPairing } from './lib/state/pairing';
  import { validMessageText, type ConversationSnapshot } from './lib/messaging';
  import {
    applyPreferences,
    readPreferences,
    savePreferences,
    type Preferences,
  } from './lib/themes/preferences';
  import { createProfile } from './lib/state/profile';
  import type { LocalProfile } from './lib/profile';

  const appPreferences = createAppPreferences();
  const appPreferencesState = appPreferences.state;
  const session = createSession();
  const sessionState = session.state;
  const security = createSecurity({
    changed: applySecurityStatus,
    failed: () => void tick().then(() => refresh()),
    beforeImport: () => {
      clearPairing();
      clearConversation();
      clearProfile();
    },
  });
  const securityState = security.state;
  const status = $derived($sessionState.status);
  let preferences = $state(readPreferences());
  const native = $derived($sessionState.native);
  const busy = $derived(
    $sessionState.busy || $securityState.busy || $securityState.summary.busy,
  );
  let notice = $state('');
  let search = $state('');
  let settings: SettingsDialog;
  let settingsOpen = $state(false);
  const profiles = createProfile({
    enabled: conversationEnabled,
    selectedContact: () => conversation.contactID,
    apply: (value) => contacts.apply(value),
  });
  const profileState = profiles.state;
  const profile = $derived($profileState.profile);
  const profileLoading = $derived($profileState.loading);
  const profileSaving = $derived($profileState.saving);
  const profileNotice = $derived($profileState.notice);
  const contactSaving = $derived($profileState.contactSaving);
  const contactNotice = $derived($profileState.contactNotice);
  let aboutDialog: HTMLDialogElement;
  let invitationDialog: InvitationDialog;
  let forgetDialog: ForgetContactDialog;
  let historyDialog: HistoryDialog;
  const historyActions = createHistoryActions();
  const historyState = historyActions.state;
  let mounted = false;
  const pairings = createPairing({
    enabled: conversationEnabled,
    available: () => status.pairingAvailable,
    contactCount: () => $contactsState.contacts.length,
    changed: () => void contacts.reload(),
  });
  const pairingState = pairings.state;
  const pairing = $derived($pairingState.pairing);
  const pairingNotice = $derived($pairingState.pairingNotice);
  const actionRequest = $derived($pairingState.actionRequest);
  const actionKind = $derived($pairingState.actionKind);
  const invitationBusy = $derived($pairingState.invitationBusy);
  const contacts = createContacts();
  const attachments = createAttachments();
  const attachmentsState = attachments.state;
  const relayChange = createRelayChange();
  const relayState = relayChange.state;
  const relayActions = {
    draft: (value: string) => relayChange.setDraft(value),
    consent: (value: boolean) => relayChange.setConsent(value),
    preset: () => relayChange.preset(),
    propose: () => void relayChange.propose(),
    cancel: (id: string) => void relayChange.cancel(id),
    reload: () => void relayChange.reload(),
    manual: (value: boolean) => relayChange.setManual(value),
    export: () => void relayChange.exportCarry(),
    copy: () => void relayChange.copyCarry(),
    importText: (value: string) => relayChange.setImportText(value),
    inspect: () => void relayChange.inspectCarry(),
  };
  const contactsState = contacts.state;
  const conversation = $derived(selectedConversation($contactsState));
  const attachmentsSelected = $derived(
    $attachmentsState.contactID === conversation.contactID,
  );
  const activation = createNotificationActivation((id) => {
    settings.close();
    search = '';
    selectConversation(id);
  });
  const activationState = activation.state;
  $effect(() => {
    $activationState;
    activation.resolve(
      conversationEnabled(),
      $contactsState.loaded,
      $contactsState.contacts.map((contact) => contact.contactID),
    );
  });
  const conversationNotice = $derived(
    $contactsState.notices[conversation.contactID] || $contactsState.notice,
  );
  const draft = $derived($contactsState.drafts[conversation.contactID] ?? '');
  const sending = $derived(
    Boolean($contactsState.sending[conversation.contactID]),
  );
  const connecting = $derived(
    Boolean($contactsState.connecting[conversation.contactID]),
  );
  const forgetting = $derived(
    Object.values($contactsState.forgetting).some(Boolean),
  );
  let conversationEpoch = 0;
  const attachmentWireAllowed = $derived(
    conversation.state !== 'peer-removed' &&
      !(conversation.state === 'connected' && conversation.peerProtocol === 3),
  );
  const attachmentStageAllowed = $derived(
    conversation.state !== 'peer-removed',
  );
  $effect(() => {
    attachments.setConnection(conversation.state, conversation.peerProtocol);
    if (
      native &&
      status.storageState === 'unlocked' &&
      conversation.contactID &&
      conversation.state !== 'peer-removed'
    ) {
      relayChange.start();
      relayChange.select(conversation.contactID, {
        peerProtocol: conversation.peerProtocol,
        attachmentsSupported: conversation.attachmentsSupported,
        contactControlsSupported: conversation.contactControlsSupported,
      });
    } else relayChange.stop();
  });

  $effect(() => {
    if (
      native &&
      status.storageState === 'unlocked' &&
      status.attachmentsAvailable
    ) {
      attachments.start();
      void attachments.select(conversation.contactID);
    } else {
      attachments.stop();
    }
  });

  function clearConversation() {
    relayChange.stop();
    attachments.stop();
    historyActions.reset();
    historyDialog?.close();
    conversationEpoch += 1;
    contacts.stop();
    search = '';
    profiles.resetContact();
  }

  const clearProfile = () => profiles.stop();
  const refreshLocalProfile = () => profiles.refresh();
  const updateProfile = (value: LocalProfile) => profiles.save(value);
  const updateContactName = (id: string, name: string) =>
    profiles.saveName(id, name);

  function settingsChanged(open: boolean) {
    settingsOpen = open;
    contacts.setVisible(
      $sessionState.windowVisible && !document.hidden && !open,
    );
    if (open) {
      void refreshLocalProfile();
      if (native) void appPreferences.load();
      if (native) void security.load();
      if (native && conversation.contactID) void relayChange.reload();
    }
  }

  function openSettings() {
    attachments.closeImage();
    if (forgetDialog?.isOpen() || historyDialog?.isOpen()) return;
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
          value.contactID !== selectedContact))
    )
      return false;
    return contacts.apply(value);
  }

  function startConversationEvents() {
    if (conversationEnabled()) void contacts.start();
  }

  async function connectConversation() {
    if (conversationEnabled()) await contacts.connect();
  }

  async function queueMessage() {
    if (conversationEnabled()) await contacts.send();
  }

  function selectConversation(contactID: string) {
    if (!conversationEnabled() || contactID === conversation.contactID) return;
    historyActions.reset();
    historyDialog?.close();
    forgetDialog?.close();
    conversationEpoch += 1;
    profiles.resetContact();
    contacts.select(contactID);
  }

  function showForgetContact() {
    if (!conversationEnabled() || conversation.contactID === '') return;
    settings.close();
    forgetDialog.show();
  }

  async function changeLocalHistory(
    selectedContact: string,
    messageID?: string,
  ) {
    const epoch = conversationEpoch;
    const result =
      messageID === undefined
        ? await historyActions.clear(selectedContact)
        : await historyActions.remove(selectedContact, messageID);
    if (result && acceptConversation(result, epoch, selectedContact)) {
      if (messageID === undefined) attachments.invalidate();
      historyDialog.close();
    }
  }

  async function forgetConversation(selectedContact: string) {
    if (
      !conversationEnabled() ||
      forgetting ||
      selectedContact === '' ||
      selectedContact !== conversation.contactID
    )
      return;
    historyActions.reset();
    historyDialog?.close();
    conversationEpoch += 1;
    const epoch = conversationEpoch;
    notice = '';
    const removed = await contacts.forget(selectedContact);
    if (epoch !== conversationEpoch || !mounted) return;
    forgetDialog.close();
    profiles.resetContact();
    if (!removed) {
      await refresh();
      notice =
        'Contact removal could not be confirmed. Check the saved contact before trying again. No new connection was started.';
    }
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
      $contactsState.contacts.length < 32 &&
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
  const matchingContacts = $derived(
    $contactsState.contacts.filter(
      (contact) =>
        search === '' ||
        contactLabel(contact).toLowerCase().includes(search.toLowerCase()) ||
        contact.peerFingerprint.includes(search.toLowerCase()),
    ),
  );
  const connectionLabel = $derived(
    connecting ? 'Connecting…' : describeConnection(conversation.state),
  );

  const beginInvitation = (url: string, consent: boolean) =>
    pairings.create(url, consent);
  const openInvitation = (link: string, url: string, consent: boolean) =>
    pairings.join(link, url, consent);
  const stopInvitation = () => pairings.cancel();
  const startPairingEvents = () => pairings.start();
  const clearPairing = () => pairings.stop();
  const decidePairing = (
    requestID: string,
    approve: boolean,
    fingerprint = '',
  ) => pairings.decide(requestID, approve, fingerprint);

  async function refresh(method: StatusMethod = 'GetStatus') {
    if (busy) return;
    if (method === 'LockStorage' || method === 'UnlockStorage') {
      if (method === 'LockStorage') activation.clear();
      security.reset();
      clearPairing();
      clearConversation();
      clearProfile();
    }
    notice = '';
    const result = await session.refresh(method);
    if (!mounted || result === null) return;
    security.startup(result);
    void security.load();
    void appPreferences.load();
    if (result.storageState !== 'unlocked') {
      clearPairing();
      clearConversation();
      clearProfile();
      return;
    }
    void refreshLocalProfile();
    startPairingEvents();
    startConversationEvents();
  }

  function storageAction() {
    const unlock = status.storageState !== 'unlocked';
    if (unlock && status.requiresPassphrase) {
      security.begin('unlock');
      return;
    }
    void refresh(unlock ? 'UnlockStorage' : 'LockStorage');
  }

  function applySecurityStatus(result: Status) {
    if (!mounted) return;
    session.adopt(result);
    security.startup(result);
    clearPairing();
    clearConversation();
    clearProfile();
    if (result.storageState === 'unlocked') {
      void refreshLocalProfile();
      startPairingEvents();
      startConversationEvents();
    }
    void appPreferences.load();
  }
  function securityAction(operation: SecurityOperation) {
    if (!native || busy) return;
    if (operation === 'import') activation.clear();
    security.begin(operation);
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
    contacts.setVisible(!document.hidden);
    const stopActivation = activation.mount();
    const stopSession = session.mount(
      () => {
        void activation.ready();
        void refresh();
      },
      {
        storage: () => {
          security.reset();
          clearPairing();
          clearConversation();
          clearProfile();
        },
        visibility: (visible) => {
          contacts.setVisible(visible && !document.hidden && !settingsOpen);
          if (visible && conversationEnabled()) void contacts.reload();
          if (visible && conversationEnabled()) void attachments.reload();
          if (visible && conversationEnabled()) void relayChange.reload();
        },
      },
    );
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
    const recoverContacts = () => {
      if (conversationEnabled()) {
        contacts.setVisible(
          $sessionState.windowVisible && !document.hidden && !settingsOpen,
        );
        void contacts.reload();
        void attachments.reload();
        void relayChange.reload();
      }
    };
    window.addEventListener('focus', recoverContacts);
    window.addEventListener('online', recoverContacts);
    document.addEventListener('visibilitychange', recoverContacts);
    return () => {
      mounted = false;
      clearPairing();
      clearConversation();
      clearProfile();
      stopSession();
      stopActivation();
      appPreferences.stop();
      security.reset();
      media.removeEventListener('change', updateSystemTheme);
      window.removeEventListener('keydown', keyboard);
      window.removeEventListener('focus', recoverContacts);
      window.removeEventListener('online', recoverContacts);
      document.removeEventListener('visibilitychange', recoverContacts);
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
<ImageViewer
  image={attachmentsSelected ? $attachmentsState.image : null}
  onclose={() => attachments.closeImage()}
/>

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
      <span>CONVERSATIONS</span><span class="count"
        >{$contactsState.contacts.length}</span
      >
    </div>
    {#if matchingContacts.length > 0}
      <ContactList
        contacts={matchingContacts}
        selectedID={conversation.contactID}
        onselect={selectConversation}
      />
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
      <p>Every conversation.<br /><strong>Just the two of you.</strong></p>
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
            {#if hasContact}<span aria-label="Conversation connection path">
                · {conversation.path}</span
              >{/if}
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
            disabled={busy || $historyState.busy}
            onclick={() => historyDialog.show()}>Clear history…</button
          >
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
            ? 'Go offline'
            : 'Go online'}</button
        >
      </div>
      <button
        class="icon-button"
        aria-label="About this build"
        onclick={() => aboutDialog.showModal()}
        ><Info size={20} strokeWidth={1.7} /></button
      >
    </header>

    {#if notice || $sessionState.notice}
      <div class="notice" role="alert">
        <Info size={18} />
        <p>{notice || $sessionState.notice}</p>
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
        <button class="text-button" onclick={() => void contacts.reloadLatest()}
          >Try again</button
        >
        <button
          class="icon-button"
          aria-label="Dismiss conversation notice"
          onclick={() => contacts.dismissNotice()}><X size={16} /></button
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
        {#if conversation.state === 'peer-removed'}
          <p class="connection-notice" role="status">
            This contact removed you. Messaging and automatic reconnect have
            stopped. Your saved history and files stay on this device.
          </p>
        {:else if conversation.state === 'identity-changed'}
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
        {:else if conversation.state === 'route-unavailable'}
          <p class="connection-notice" role="alert">
            The approved connection route could not be reached. Check the
            connection service and choose Connect to retry. Your queued messages
            and saved contact are retained.
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
          aria-busy={Boolean(
            $contactsState.loadingPages[conversation.contactID],
          )}
          aria-live="polite"
          aria-relevant="additions text"
        >
          {#if conversation.messages.length > 100}
            <button
              type="button"
              class="text-button"
              disabled={$contactsState.loadingPages[conversation.contactID]}
              onclick={() => void contacts.reloadLatest()}
              >Reload latest messages</button
            >
          {/if}
          {#if $contactsState.pages[conversation.contactID]?.hasMore}
            <button
              type="button"
              class="text-button"
              disabled={$contactsState.loadingPages[conversation.contactID]}
              onclick={() => void contacts.loadOlder()}
            >
              {$contactsState.loadingPages[conversation.contactID]
                ? 'Loading history…'
                : 'Load older messages'}
            </button>
          {/if}
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
                <button
                  class="text-button"
                  aria-label="Delete local message"
                  disabled={busy || $historyState.busy}
                  onclick={() => historyDialog.show(message.id)}
                  >Delete locally</button
                >
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
        {#if status.attachmentsAvailable}
          {#key conversation.contactID}
            <AttachmentPanel
              items={attachmentsSelected ? $attachmentsState.items : []}
              wireAllowed={attachmentWireAllowed}
              stageAllowed={attachmentStageAllowed}
              wireNotice={conversation.state === 'connected' &&
              conversation.peerProtocol === 3
                ? 'Text messaging works with this device. It needs an update for attachments; local drafts and queued files are kept.'
                : ''}
              drafts={attachmentsSelected ? $attachmentsState.drafts : []}
              busy={$attachmentsState.busy}
              notice={attachmentsSelected ? $attachmentsState.notice : ''}
              videos={attachmentsSelected ? $attachmentsState.videos : {}}
              saved={attachmentsSelected ? $attachmentsState.saved : {}}
              autoAcceptImages={attachmentsSelected &&
                $attachmentsState.autoAcceptImages}
              hasMore={attachmentsSelected && $attachmentsState.hasMore}
              onolder={() => void attachments.older()}
              onpick={(original) => void attachments.pick(original)}
              onpaste={() => void attachments.paste()}
              onsend={(draft, allow) => void attachments.send(draft, allow)}
              ondiscard={(draft) => void attachments.discard(draft)}
              ondecision={(item, action) =>
                void attachments.decision(item, action)}
              onautoaccept={(enabled) =>
                void attachments.setAutoAccept(enabled)}
              onpreview={(item) => void attachments.preview(item)}
              onsave={(item) => void attachments.save(item)}
              onreveal={(item) => void attachments.reveal(item)}
            />
          {/key}
        {/if}
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
              : 'Choose Go online to reconnect or start a private invitation in the desktop app.'}
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
          aria-label={status.attachmentsAvailable && hasContact
            ? 'Choose attachment'
            : 'Attach file unavailable'}
          disabled={!status.attachmentsAvailable ||
            !attachmentStageAllowed ||
            !hasContact ||
            busy ||
            $attachmentsState.busy}
          onclick={() => void attachments.pick(false)}
          ><Paperclip size={21} strokeWidth={1.6} /></button
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
          value={draft}
          oninput={(event) =>
            contacts.setDraft(
              conversation.contactID,
              event.currentTarget.value,
            )}
          maxlength={16 * 1024}
          onpaste={(event) => {
            if (
              status.attachmentsAvailable &&
              attachmentStageAllowed &&
              event.clipboardData?.types.includes('Files')
            ) {
              event.preventDefault();
              void attachments.paste();
            }
          }}
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
  appPreferences={$appPreferencesState}
  onapppreferences={(value) => void appPreferences.save(value)}
  onreloadapppreferences={() => void appPreferences.load()}
  security={$securityState}
  onsecurity={securityAction}
  onreloadsecurity={() => void security.load()}
  relay={$relayState}
  onrelay={relayActions}
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

<RelayApprovalDialog
  snapshot={$relayState.snapshot}
  supported={$relayState.capabilities.contactControlsSupported ||
    $relayState.manual}
  loaded={$relayState.loaded}
  busy={$relayState.busy || busy}
  consent={$relayState.approvalConsent}
  notice={$relayState.notice}
  onconsent={relayChange.setApprovalConsent}
  onapprove={(id) => void relayChange.approve(id)}
  ondecline={(id) => void relayChange.decline(id)}
  onreload={() => void relayChange.reload()}
/>

<PassphraseDialog
  operation={$securityState.operation}
  busy={$securityState.busy}
  notice={$securityState.notice}
  requiresPassphrase={$securityState.operation === 'repair'
    ? $securityState.summary.requiresPassphrase
    : status.requiresPassphrase}
  backupAttention={$securityState.summary.backupAttention}
  backupRepairAvailable={$securityState.summary.backupRepairAvailable}
  onsubmit={(current, secret) => void security.submit(current, secret)}
  oncancel={security.cancel}
  onrepair={() => securityAction('repair')}
/>
<RecoveryDialog
  preview={$securityState.operation === 'import-confirm'
    ? $securityState.preview
    : null}
  busy={$securityState.busy}
  notice={$securityState.notice}
  onconfirm={(secret, required, replace) =>
    void security.commit(secret, required, replace)}
  oncancel={security.cancel}
/>

<ForgetContactDialog
  bind:this={forgetDialog}
  contactID={conversation.contactID}
  enabled={native && !busy && status.storageState === 'unlocked'}
  busy={forgetting}
  onconfirm={(contactID) => void forgetConversation(contactID)}
/>

<HistoryDialog
  bind:this={historyDialog}
  contactID={conversation.contactID}
  enabled={native && !busy && status.storageState === 'unlocked'}
  busy={$historyState.busy}
  notice={$historyState.notice}
  onconfirm={(contactID, messageID) =>
    void changeLocalHistory(contactID, messageID)}
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
        service and explicit consent. Messages and attachments use approved
        contact credentials. Files require receiver approval and are never
        opened automatically. Strict direct-only delivery is unavailable.
      </p>
    </div>
    <p class="muted small">
      Both devices need to be running to exchange messages. Your selected
      connection service learns your IP address and availability and may relay
      encrypted traffic. In OS-only mode, launch opens local storage and
      reconnects saved contacts automatically. Go online reconnects after you
      have gone offline; Connect retries a stopped contact connection. Go
      offline stops connections and locks local data while keeping saved
      contacts and history.
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
