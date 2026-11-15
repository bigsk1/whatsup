<script lang="ts">
  import { groupedFingerprint } from '../state/fingerprint';
  import { tick } from 'svelte';
  import {
    ArrowLeft,
    ChevronRight,
    ContactRound,
    LockKeyhole,
    MessageCircle,
    Palette,
    ShieldCheck,
    UserRound,
    Bell,
  } from '@lucide/svelte';
  import type { Status } from '../backend';
  import type { ConversationSnapshot } from '../messaging';
  import { validDisplayName, type LocalProfile } from '../profile';
  import type { Accent, Preferences, Theme } from '../themes/preferences';
  import type { AppPreferences } from '../state/preferences';
  import type { StorageSecurity, SecurityOperation } from '../state/security';
  import RelayChangePanel from './RelayChangePanel.svelte';
  import type { RelayChangeState } from '../state/relay';

  type Page =
    'profile' | 'contact' | 'appearance' | 'privacy' | 'app' | 'security';
  let {
    status,
    preferences,
    native,
    busy,
    onpreferences,
    onstorage,
    canForget,
    onforget,
    onopenchange,
    profile,
    profileLoading,
    profileSaving,
    profileNotice,
    onsaveprofile,
    conversation,
    contactSaving,
    contactNotice,
    onsavecontact,
    appPreferences,
    onapppreferences,
    onreloadapppreferences,
    security,
    onsecurity,
    onreloadsecurity,
    relay,
    onrelay,
  }: {
    status: Status;
    preferences: Preferences;
    native: boolean;
    busy: boolean;
    onpreferences: (value: Preferences) => void;
    onstorage: () => void;
    canForget: boolean;
    onforget: () => void;
    onopenchange: (open: boolean) => void;
    profile: LocalProfile | null;
    profileLoading: boolean;
    profileSaving: boolean;
    profileNotice: string;
    onsaveprofile: (value: LocalProfile) => void;
    conversation: ConversationSnapshot;
    contactSaving: boolean;
    contactNotice: string;
    onsavecontact: (contactID: string, name: string) => void;
    appPreferences: {
      value: AppPreferences;
      loaded: boolean;
      busy: boolean;
      notice: string;
    };
    onapppreferences: (value: AppPreferences) => void;
    onreloadapppreferences: () => void;
    security: {
      summary: StorageSecurity;
      loaded: boolean;
      busy: boolean;
      notice: string;
    };
    onsecurity: (operation: SecurityOperation) => void;
    onreloadsecurity: () => void;
    relay: RelayChangeState;
    onrelay: {
      draft: (value: string) => void;
      consent: (value: boolean) => void;
      preset: () => void;
      propose: () => void;
      cancel: (id: string) => void;
      reload: () => void;
      manual: (value: boolean) => void;
      export: () => void;
      copy: () => void;
      importText: (value: string) => void;
      inspect: () => void;
    };
  } = $props();
  let open = $state(false);
  let selected = $state<Page>('profile');
  let heading = $state<HTMLHeadingElement>();
  let returnFocus: HTMLElement | null = null;
  let displayName = $state('');
  let shareDisplayName = $state(false);
  let localName = $state('');
  const unlocked = $derived(native && status.storageState === 'unlocked');
  const contactName = $derived(
    conversation.localName ||
      conversation.peerDisplayName ||
      'Approved contact',
  );
  const selectedContactID = $derived(conversation.contactID);
  const savedLocalName = $derived(conversation.localName);
  const pages = [
    { id: 'profile', title: 'Profile', icon: UserRound },
    { id: 'contact', title: 'Contact', icon: ContactRound },
    { id: 'appearance', title: 'Appearance', icon: Palette },
    { id: 'app', title: 'App behavior', icon: Bell },
    { id: 'security', title: 'Security & recovery', icon: LockKeyhole },
    { id: 'privacy', title: 'Privacy & diagnostics', icon: ShieldCheck },
  ] as const;
  const pageTitle = $derived(pages.find((page) => page.id === selected)!.title);

  export function show(page: Page = 'profile') {
    if (!open) returnFocus = document.activeElement as HTMLElement | null;
    selected = page;
    open = true;
    onopenchange(true);
    void tick().then(() => heading?.focus());
  }
  export function close() {
    if (!open) return;
    open = false;
    onopenchange(false);
    void tick().then(() => returnFocus?.focus());
  }
  $effect(() => {
    if (!open || !unlocked || profile === null) {
      displayName = '';
      shareDisplayName = false;
    } else {
      displayName = profile.displayName;
      shareDisplayName = profile.shareDisplayName;
    }
  });
  $effect(() => {
    localName =
      open && unlocked && selectedContactID !== '' ? savedLocalName : '';
  });
  function choose(page: Page) {
    selected = page;
    void tick().then(() => heading?.focus());
  }
</script>

<svelte:window
  onkeydown={(event) => {
    if (
      open &&
      event.key === 'Escape' &&
      !document.querySelector('dialog[open]')
    ) {
      event.preventDefault();
      close();
    }
  }}
/>

{#if open}
  <section class="settings-workspace" aria-label="Settings">
    <aside class="settings-sidebar">
      <a
        class="settings-brand"
        href="#conversation"
        onclick={(event) => {
          event.preventDefault();
          close();
        }}
        ><MessageCircle size={27} /><span
          >WhatsUp<span class="brand-period">.</span><small>YOUR SPACE</small
          ></span
        ></a
      >
      <p class="settings-nav-label">SETTINGS</p>
      <nav aria-label="Settings pages">
        {#each pages as page}<button
            class:selected={selected === page.id}
            aria-current={selected === page.id ? 'page' : undefined}
            onclick={() => choose(page.id)}
            ><page.icon size={18} strokeWidth={1.6} /><span>{page.title}</span
            ></button
          >{/each}
      </nav>
      <div class="settings-sidebar-bottom">
        <div class="settings-device">
          <LockKeyhole size={16} />
          <p>
            On this device<small
              >{status.storageState === 'unlocked'
                ? 'Storage unlocked'
                : status.storageState === 'locked'
                  ? 'Storage locked'
                  : 'Storage unavailable'}</small
            >
          </p>
        </div>
        <p class="settings-version">WhatsUp <span>{status.version}</span></p>
      </div>
    </aside>
    <div class="settings-main">
      <header class="settings-topbar">
        <p>Settings <ChevronRight size={13} /><strong>{pageTitle}</strong></p>
        <button class="secondary-button compact settings-back" onclick={close}
          ><ArrowLeft size={15} /> Back to chats</button
        >
      </header>
      <main class="settings-scroll" aria-labelledby="settings-page-title">
        <div class="settings-content">
          <div class="settings-page-heading">
            <p class="eyebrow">
              {selected === 'profile'
                ? 'A FAMILIAR NAME'
                : selected === 'contact'
                  ? 'THE PERSON YOU CHOOSE'
                  : selected === 'appearance'
                    ? 'MAKE IT YOURS'
                    : 'ON YOUR TERMS'}
            </p>
            <h1 id="settings-page-title" bind:this={heading} tabindex="-1">
              {pageTitle}
            </h1>
            <p class="muted">
              {selected === 'profile'
                ? 'Choose how you appear, and what you share.'
                : selected === 'contact'
                  ? 'A familiar name for a verified identity.'
                  : selected === 'appearance'
                    ? 'A space that feels like you.'
                    : 'Your connection, local data and troubleshooting details.'}
            </p>
          </div>

          {#if selected === 'profile'}
            <section class="settings-card" aria-labelledby="profile-heading">
              <div class="settings-card-heading">
                <div>
                  <h2 id="profile-heading">Your display name</h2>
                  <p class="muted small">Saved encrypted on this device.</p>
                </div>
                <UserRound size={19} />
              </div>
              <form
                onsubmit={(event) => {
                  event.preventDefault();
                  onsaveprofile({
                    displayName: displayName.trim(),
                    shareDisplayName,
                  });
                }}
              >
                <label class="settings-field" for="display-name"
                  >Display name</label
                ><input
                  id="display-name"
                  class="settings-input"
                  bind:value={displayName}
                  maxlength="256"
                  autocomplete="off"
                  placeholder="How would you like to be known?"
                  disabled={!unlocked ||
                    profile === null ||
                    profileLoading ||
                    profileSaving ||
                    busy}
                />
                <p class="muted small field-help">
                  Up to 64 characters. Your device fingerprint stays the same
                  when your name changes.
                </p>
                <label class="settings-toggle-row" for="share-name"
                  ><span
                    ><strong>Share my display name</strong><small
                      >Send your name to your approved contact over the
                      encrypted connection.</small
                    ></span
                  ><input
                    id="share-name"
                    type="checkbox"
                    bind:checked={shareDisplayName}
                    disabled={!unlocked ||
                      profile === null ||
                      profileLoading ||
                      profileSaving ||
                      busy}
                  /><span class="toggle-track" aria-hidden="true"></span></label
                >
                <div class="settings-save-row">
                  <button
                    class="primary-button compact"
                    disabled={!unlocked ||
                      profile === null ||
                      profileLoading ||
                      profileSaving ||
                      busy ||
                      !validDisplayName(displayName.trim())}
                    type="submit"
                    >{profileSaving ? 'Saving…' : 'Save profile'}</button
                  >{#if profileLoading}<span class="muted small" role="status"
                      >Loading profile…</span
                    >{/if}
                </div>
              </form>
              {#if profileNotice}<p
                  class="settings-inline-notice"
                  role="status"
                >
                  {profileNotice}
                </p>{/if}
              {#if !unlocked}<div class="settings-empty-note">
                  <LockKeyhole size={17} />
                  <p>
                    {native
                      ? 'Unlock storage to edit your encrypted profile.'
                      : 'Open the desktop app to edit your encrypted profile.'}
                  </p>
                  <button
                    class="secondary-button compact"
                    disabled={!native || busy}
                    onclick={onstorage}>Go online</button
                  >
                </div>{/if}
            </section>
            <section
              class="settings-card"
              aria-labelledby="your-identity-heading"
            >
              <div class="settings-card-heading">
                <div>
                  <h2 id="your-identity-heading">Your device identity</h2>
                  <p class="muted small">
                    This fingerprint identifies this device, independently of
                    its name.
                  </p>
                </div>
                <ShieldCheck size={19} />
              </div>
              {#if status.identityFingerprint}<output
                  id="identity-fingerprint"
                  class="fingerprint"
                  aria-label="Your identity fingerprint"
                  >{groupedFingerprint(status.identityFingerprint)}</output
                >{:else}<p class="muted small">
                  {native
                    ? 'Available after you unlock storage.'
                    : 'Available in the desktop app.'}
                </p>{/if}
            </section>
          {:else if selected === 'contact'}
            {#if unlocked && conversation.contactID !== ''}
              <section class="settings-card" aria-labelledby="contact-heading">
                <div class="settings-card-heading">
                  <div>
                    <h2 id="contact-heading">{contactName}</h2>
                    <p class="muted small">
                      Your saved contact on this device.
                    </p>
                  </div>
                  <ContactRound size={19} />
                </div>
                <form
                  onsubmit={(event) => {
                    event.preventDefault();
                    onsavecontact(conversation.contactID, localName.trim());
                  }}
                >
                  <label class="settings-field" for="local-contact-name"
                    >Name on this device</label
                  ><input
                    id="local-contact-name"
                    class="settings-input"
                    bind:value={localName}
                    maxlength="256"
                    autocomplete="off"
                    placeholder="Choose a private contact name"
                    disabled={busy || contactSaving}
                  />
                  <p class="muted small field-help">
                    Only you see this name. Leave it empty to use their shared
                    display name.
                  </p>
                  <div class="settings-save-row">
                    <button
                      class="primary-button compact"
                      disabled={busy ||
                        contactSaving ||
                        !validDisplayName(localName.trim())}
                      type="submit"
                      >{contactSaving ? 'Saving…' : 'Save contact name'}</button
                    >
                  </div>
                </form>
                {#if contactNotice}<p
                    class="settings-inline-notice"
                    role="status"
                  >
                    {contactNotice}
                  </p>{/if}
                <dl class="settings-details">
                  <div>
                    <dt>Shared display name</dt>
                    <dd>{conversation.peerDisplayName || 'Not shared'}</dd>
                  </div>
                  <div>
                    <dt>Verified fingerprint</dt>
                    <dd>
                      <output class="fingerprint"
                        >{groupedFingerprint(
                          conversation.peerFingerprint,
                        )}</output
                      >
                    </dd>
                  </div>
                </dl>
              </section>
              {#if conversation.state === 'peer-removed'}<section
                  class="settings-card"
                >
                  <h2>This contact removed you</h2>
                  <p>
                    Your history and saved files are kept. Messaging and
                    automatic reconnect have stopped.
                  </p>
                </section>
              {:else}<section class="settings-card">
                  <RelayChangePanel
                    snapshot={relay.snapshot}
                    capabilities={relay.capabilities}
                    draftURL={relay.draftURL}
                    consent={relay.consent}
                    loaded={relay.loaded}
                    loading={relay.loading}
                    busy={relay.busy || busy}
                    notice={relay.notice}
                    manual={relay.manual}
                    exportText={relay.exportText}
                    importText={relay.importText}
                    ondraft={onrelay.draft}
                    onconsent={onrelay.consent}
                    onpreset={onrelay.preset}
                    onpropose={onrelay.propose}
                    oncancel={onrelay.cancel}
                    onreload={onrelay.reload}
                    onmanual={onrelay.manual}
                    onexport={onrelay.export}
                    oncopy={onrelay.copy}
                    onimporttext={onrelay.importText}
                    oninspect={onrelay.inspect}
                  />
                </section>{/if}
              <section class="settings-card" aria-labelledby="forget-heading">
                <div class="settings-card-heading">
                  <div>
                    <h2 id="forget-heading">Forget this contact</h2>
                    <p class="muted small">
                      Remove this contact and this device’s saved conversation.
                      Your device identity is kept.
                    </p>
                  </div>
                </div>
                <button
                  class="secondary-button compact"
                  disabled={busy || !canForget}
                  onclick={onforget}>Forget contact…</button
                >
              </section>
            {:else}<section class="settings-card settings-page-empty">
                <ContactRound size={28} />
                <h2>
                  {unlocked
                    ? 'No saved contact yet'
                    : 'Your contact stays private'}
                </h2>
                <p class="muted">
                  {unlocked
                    ? 'Go back to chats to create or paste an invitation.'
                    : native
                      ? 'Unlock storage to view your saved contact.'
                      : 'Contact settings are available in the desktop app.'}
                </p>
                {#if native && !unlocked}<button
                    class="secondary-button compact"
                    disabled={busy}
                    onclick={onstorage}>Go online</button
                  >{:else}<button
                    class="secondary-button compact"
                    onclick={close}>Back to chats</button
                  >{/if}
              </section>{/if}
          {:else if selected === 'appearance'}
            <section
              class="settings-card settings-section"
              aria-labelledby="appearance-heading"
            >
              <div class="settings-card-heading">
                <div>
                  <h2 id="appearance-heading">Theme</h2>
                  <p class="muted small">
                    Appearance preferences stay on this device.
                  </p>
                </div>
                <Palette size={19} />
              </div>
              <fieldset class="theme-options">
                <legend class="sr-only">Theme</legend
                >{#each ['dark', 'light', 'system'] as theme}<label
                    class="choice"
                    class:chosen={preferences.theme === theme}
                    ><input
                      type="radio"
                      name="theme"
                      value={theme}
                      checked={preferences.theme === theme}
                      onchange={() =>
                        onpreferences({
                          ...preferences,
                          theme: theme as Theme,
                        })}
                    /><span
                      class="theme-preview"
                      class:light-preview={theme === 'light'}
                      class:system-preview={theme === 'system'}
                      ><span></span><span></span></span
                    ><span class="choice-label"
                      >{theme[0].toUpperCase() + theme.slice(1)}</span
                    ></label
                  >{/each}
              </fieldset>
            </section>
            <section
              class="settings-card settings-section"
              aria-labelledby="accent-heading"
            >
              <div class="settings-card-heading">
                <div>
                  <h2 id="accent-heading">Accent color</h2>
                  <p class="muted small">
                    A little color across your conversations.
                  </p>
                </div>
              </div>
              <fieldset class="accent-options">
                <legend class="sr-only">Accent color</legend
                >{#each ['teal', 'blue', 'rose'] as accent}<label
                    class="accent-choice"
                    class:chosen={preferences.accent === accent}
                    ><input
                      type="radio"
                      name="accent"
                      value={accent}
                      checked={preferences.accent === accent}
                      onchange={() =>
                        onpreferences({
                          ...preferences,
                          accent: accent as Accent,
                        })}
                    /><span class="accent-swatch" data-color={accent}
                    ></span><span
                      >{accent[0].toUpperCase() + accent.slice(1)}</span
                    ></label
                  >{/each}
              </fieldset>
            </section>
          {:else if selected === 'app'}
            <section
              class="settings-card"
              aria-labelledby="app-behavior-heading"
            >
              <h2 id="app-behavior-heading">App behavior</h2>
              <p class="muted small">
                These preferences stay on this device. In OS-only mode, launch
                opens local storage and reconnects saved contacts.
              </p>
              {#key appPreferences.loaded}
                {#each [{ key: 'closeToTray', label: 'Keep running when the window closes', help: 'Close to the tray. Choose Quit from the tray to exit.' }, { key: 'startAtLogin', label: 'Start WhatsUp when I sign in', help: 'Starts on this OS account only. This is off unless you enable it.' }, { key: 'notifications', label: 'Show new-message notifications', help: 'Notifications say only “New message” by default.' }, { key: 'notificationName', label: 'Include contact names in notifications', help: 'Allows names on the desktop and its notification history.' }, { key: 'notificationPreview', label: 'Include message previews in notifications', help: 'Allows message text on the desktop and its notification history.' }] as option}
                  <label class="settings-toggle-row app-preference-row">
                    <span class="app-preference-copy">
                      <strong id={`app-preference-${option.key}-label`}
                        >{option.label}</strong
                      >
                      <small id={`app-preference-${option.key}-help`}
                        >{option.help}</small
                      >
                    </span>
                    <input
                      aria-labelledby={`app-preference-${option.key}-label`}
                      aria-describedby={`app-preference-${option.key}-help`}
                      type="checkbox"
                      checked={appPreferences.value[
                        option.key as keyof AppPreferences
                      ]}
                      disabled={!native ||
                        !appPreferences.loaded ||
                        appPreferences.busy ||
                        ((option.key === 'notificationName' ||
                          option.key === 'notificationPreview') &&
                          !appPreferences.value.notifications)}
                      onchange={(event) =>
                        onapppreferences({
                          ...appPreferences.value,
                          [option.key]: event.currentTarget.checked,
                        })}
                    />
                    <span class="toggle-track" aria-hidden="true"></span>
                  </label>
                {/each}
              {/key}
              {#if appPreferences.notice}<p
                  role={appPreferences.loaded ? 'status' : 'alert'}
                >
                  {appPreferences.notice}
                </p>{/if}
              {#if native && !appPreferences.loaded}<button
                  class="secondary-button compact"
                  disabled={appPreferences.busy}
                  onclick={onreloadapppreferences}
                  >Reload app preferences</button
                >{/if}
            </section>
          {:else if selected === 'security'}
            {#if security.summary.backupAttention}
              <section
                class="settings-card"
                aria-labelledby="backup-attention-heading"
              >
                <h2 id="backup-attention-heading">
                  A local backup needs attention
                </h2>
                <p role="alert">
                  WhatsUp could not authenticate or update <code
                    >{security.summary.backupAttention}</code
                  >.
                </p>
                <p class="muted small">
                  Set this backup aside encrypted in the app data folder and
                  retry opening local storage. Its original contents stay
                  preserved for recovery.
                </p>
                <button
                  class="secondary-button compact"
                  disabled={!native ||
                    busy ||
                    security.busy ||
                    !security.loaded}
                  onclick={() => onsecurity('repair')}
                  >Set aside and retry…</button
                >
              </section>
            {/if}
            <section
              class="settings-card"
              aria-labelledby="app-passphrase-heading"
            >
              <h2 id="app-passphrase-heading">App passphrase</h2>
              <p class="muted small">
                Your OS credential protects local data. An optional app
                passphrase also requires a secret when WhatsUp starts or goes
                online.
              </p>
              <p>
                {status.requiresPassphrase
                  ? 'An app passphrase is required.'
                  : 'Local data uses OS protection without an app passphrase.'}
              </p>
              <button
                class="secondary-button compact"
                disabled={!native || !unlocked || busy || security.busy}
                onclick={() =>
                  onsecurity(status.requiresPassphrase ? 'disable' : 'enable')}
                >{status.requiresPassphrase
                  ? 'Disable app passphrase…'
                  : 'Require app passphrase…'}</button
              >
              {#if !unlocked}<p class="muted small">
                  Go online before changing your app passphrase.
                </p>{/if}
            </section>
            <section
              class="settings-card settings-section"
              aria-labelledby="recovery-heading"
            >
              <h2 id="recovery-heading">Encrypted recovery copies</h2>
              <p class="muted small">
                Export your identity, saved contacts and history with a separate
                recovery passphrase. Import authenticates the entire copy before
                you confirm replacement. Native file dialogs keep file paths out
                of the interface.
              </p>
              <div class="settings-storage-row">
                <button
                  class="secondary-button compact"
                  disabled={!native ||
                    !unlocked ||
                    busy ||
                    security.busy ||
                    !security.loaded ||
                    !security.summary.recoveryAvailable}
                  onclick={() => onsecurity('export')}
                  >Export recovery copy…</button
                ><button
                  class="secondary-button compact"
                  disabled={!native || busy || security.busy}
                  onclick={() => onsecurity('import')}
                  >Import recovery copy…</button
                >
              </div>
              {#if security.notice}<p role="status">{security.notice}</p>{/if}
              {#if native && !security.loaded}<button
                  class="text-button"
                  disabled={security.busy}
                  onclick={onreloadsecurity}>Reload security settings</button
                >{/if}
            </section>
          {:else}
            <section class="settings-card" aria-labelledby="storage-heading">
              <div class="settings-card-heading">
                <div>
                  <h2 id="storage-heading">Local storage</h2>
                  <p class="muted small">
                    {native
                      ? 'Your OS credential store protects the local encryption key.'
                      : 'Local storage is available in the desktop app.'}
                  </p>
                </div>
                <LockKeyhole size={19} />
              </div>
              <div class="settings-storage-row">
                <span
                  >{status.storageState === 'unlocked'
                    ? 'Unlocked on this device'
                    : status.storageState === 'locked'
                      ? 'Locked on this device'
                      : 'Unavailable'}</span
                ><button
                  class="secondary-button compact"
                  disabled={!native || busy}
                  onclick={onstorage}
                  >{busy
                    ? 'Please wait…'
                    : status.storageState === 'unlocked'
                      ? 'Go offline'
                      : 'Go online'}</button
                >
              </div>
              <p class="muted small field-help">
                {status.storageState === 'unlocked'
                  ? 'Disconnects from all contacts and locks local data. Saved contacts and history stay on this device.'
                  : 'Unlocks local data and reconnects to your saved contact. Without a contact, you can start a new invitation.'}
              </p>
              {#if status.identityFingerprint}<output
                  id="identity-fingerprint"
                  class="fingerprint"
                  aria-label="Your identity fingerprint"
                  >{groupedFingerprint(status.identityFingerprint)}</output
                >{/if}
            </section>
            <section
              class="settings-card"
              aria-labelledby="diagnostics-heading"
            >
              <div class="settings-card-heading">
                <div>
                  <h2 id="diagnostics-heading">Local diagnostics</h2>
                  <p class="muted small">
                    Troubleshooting without automatic uploads.
                  </p>
                </div>
                <ShieldCheck size={19} />
              </div>
              <p class="settings-body-copy">
                Redacted troubleshooting logs stay on this device. They exclude
                your names, messages, fingerprints and keys. WhatsUp has no
                analytics or remote crash reporting.
              </p>
              <p class="muted small field-help">
                The active and previous logs are limited to 256 KiB each.
                Appearance is the only preference saved in the WebView; names
                and conversations are encrypted in Go storage.
              </p>
            </section>
            <section class="settings-card" aria-labelledby="runtime-heading">
              <div class="settings-card-heading">
                <div>
                  <h2 id="runtime-heading">Desktop runtime</h2>
                  <p class="muted small">WhatsUp · {status.version}</p>
                </div>
              </div>
              <p class="settings-body-copy">
                Windows uses the system WebView2 with a private profile,
                separate from Edge. Optional diagnostic data follows Windows
                privacy settings; required Microsoft diagnostics may still
                occur.
              </p>
              <p class="muted small field-help">
                Linux uses WebKitGTK with private application data and cache
                directories. Its diagnostic behavior differs from WebView2.
              </p>
            </section>
          {/if}
        </div>
      </main>
    </div>
  </section>
{/if}

<style>
  .settings-workspace {
    display: grid;
    grid-template-columns: 224px minmax(0, 1fr);
    height: 100dvh;
    overflow: hidden;
    background: var(--bg);
  }
  .settings-sidebar {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
    background: var(--rail);
    border-right: 1px solid var(--border);
    padding: 30px 14px 16px;
  }
  .settings-brand {
    display: flex;
    gap: 12px;
    align-items: center;
    text-decoration: none;
    padding: 8px 13px;
    color: var(--accent);
  }
  .settings-brand > span {
    color: var(--text);
    font-size: 23px;
    font-weight: 650;
    letter-spacing: -1px;
  }
  .brand-period {
    color: var(--accent);
  }
  .settings-brand small {
    display: block;
    font-size: 8px;
    letter-spacing: 3px;
    color: var(--faint);
    margin-top: 5px;
  }
  .settings-nav-label {
    margin: 46px 15px 14px;
    font-size: 9px;
    letter-spacing: 2px;
    color: var(--faint);
    font-weight: 650;
  }
  nav {
    display: grid;
    gap: 7px;
  }
  nav button {
    display: flex;
    align-items: center;
    gap: 13px;
    width: 100%;
    text-align: left;
    padding: 14px 15px;
    border: 1px solid transparent;
    border-radius: 8px;
    color: var(--muted);
    font-size: 13px;
    font-weight: 550;
  }
  nav button:hover {
    color: var(--text);
    background: var(--surface);
  }
  nav button.selected {
    background: var(--accent-soft);
    border-color: var(--border);
    color: var(--accent);
  }
  .settings-sidebar-bottom {
    margin-top: auto;
    padding: 28px 10px 0;
  }
  .settings-device {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    color: var(--muted);
  }
  .settings-device p {
    color: var(--text);
    font-size: 11px;
    line-height: 1.6;
  }
  .settings-device small {
    display: block;
    color: var(--muted);
    font-size: 10px;
  }
  .settings-version {
    border-top: 1px solid var(--border);
    padding-top: 16px;
    margin-top: 24px;
    display: flex;
    justify-content: space-between;
    font-size: 9px;
    color: var(--faint);
    letter-spacing: 0.6px;
  }
  .settings-main {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
  }
  .settings-topbar {
    min-height: 80px;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 20px;
    border-bottom: 1px solid var(--border);
    padding: 16px 42px;
    background: var(--sidebar);
  }
  .settings-topbar p {
    display: flex;
    align-items: center;
    gap: 14px;
    font-size: 11px;
    color: var(--muted);
  }
  .settings-topbar strong {
    font-weight: 550;
    color: var(--text);
  }
  .settings-scroll {
    flex: 1;
    min-height: 0;
    overflow: auto;
    overscroll-behavior: contain;
  }
  .settings-content {
    width: min(100%, 1080px);
    padding: 42px 42px 56px;
    margin: 0 auto;
  }
  .settings-page-heading {
    margin-bottom: 30px;
  }
  .settings-page-heading .eyebrow {
    margin-bottom: 15px;
  }
  .settings-page-heading h1 {
    font-size: 30px;
    letter-spacing: -1px;
    font-weight: 600;
    margin-bottom: 10px;
    outline: 0;
  }
  .settings-page-heading > .muted {
    font-size: 13px;
    line-height: 1.6;
  }
  .settings-card {
    padding: 26px;
    border-radius: 11px;
    border: 1px solid var(--border);
    background: var(--surface-subtle);
  }
  .settings-card + .settings-card {
    margin-top: 20px;
  }
  .settings-card-heading {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 20px;
    margin-bottom: 25px;
  }
  .settings-card-heading > :global(svg) {
    color: var(--faint);
    flex-shrink: 0;
  }
  .settings-card h2 {
    font-size: 14px;
    font-weight: 600;
    line-height: 1.5;
    overflow-wrap: anywhere;
  }
  .settings-card-heading p {
    margin-top: 5px;
    line-height: 1.7;
  }
  .settings-field {
    display: block;
    font-size: 12px;
    font-weight: 550;
    margin-bottom: 10px;
  }
  .settings-input {
    width: 100%;
    max-width: 520px;
    padding: 12px 14px;
    background: var(--bg);
    color: var(--text);
    border: 1px solid var(--border);
    border-radius: 7px;
    font-size: 13px;
  }
  .settings-input:disabled {
    opacity: 0.5;
  }
  .settings-input::placeholder {
    color: var(--faint);
  }
  .field-help {
    margin-top: 10px;
    line-height: 1.8;
  }
  .settings-toggle-row {
    position: relative;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 20px;
    padding: 21px 0;
    margin-top: 20px;
    border-top: 1px solid var(--border);
    border-bottom: 1px solid var(--border);
    cursor: pointer;
  }
  .settings-toggle-row strong {
    display: block;
    font-size: 12px;
    font-weight: 550;
  }
  .settings-toggle-row small {
    display: block;
    color: var(--muted);
    font-size: 11px;
    line-height: 1.7;
    margin-top: 6px;
  }
  .settings-toggle-row input {
    position: absolute;
    right: 0;
    width: 36px;
    height: 23px;
    opacity: 0;
  }
  .toggle-track {
    display: block;
    width: 35px;
    height: 19px;
    flex-shrink: 0;
    border-radius: 20px;
    background: var(--faint);
    position: relative;
    pointer-events: none;
  }
  .toggle-track::after {
    content: '';
    position: absolute;
    top: 3px;
    left: 3px;
    width: 13px;
    height: 13px;
    border-radius: 50%;
    background: var(--bg);
    transition: transform 140ms;
  }
  .settings-toggle-row input:checked + .toggle-track {
    background: var(--accent);
  }
  .settings-toggle-row input:checked + .toggle-track::after {
    transform: translateX(16px);
  }
  .settings-toggle-row input:focus-visible + .toggle-track {
    outline: 2px solid var(--accent);
    outline-offset: 4px;
  }
  .settings-toggle-row input:disabled + .toggle-track {
    opacity: 0.4;
  }
  .app-preference-row {
    margin-top: 0;
    padding: 18px 0;
    border-bottom: 0;
  }
  .app-preference-row:first-of-type {
    margin-top: 12px;
    border-top: 0;
  }
  .app-preference-copy {
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .settings-save-row {
    margin-top: 23px;
    display: flex;
    align-items: center;
    gap: 16px;
  }
  .settings-inline-notice {
    margin-top: 16px;
    font-size: 12px;
    line-height: 1.7;
    color: var(--accent);
  }
  .settings-empty-note {
    margin-top: 22px;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 12px;
    color: var(--muted);
    font-size: 12px;
  }
  .settings-empty-note p {
    flex: 1;
    min-width: 180px;
    line-height: 1.7;
  }
  .settings-details {
    margin: 26px 0 0;
  }
  .settings-details > div {
    border-top: 1px solid var(--border);
    padding-top: 18px;
    margin-top: 18px;
  }
  dt {
    font-size: 11px;
    color: var(--muted);
    margin-bottom: 10px;
  }
  dd {
    font-size: 13px;
    margin: 0;
    overflow-wrap: anywhere;
  }
  .settings-page-empty {
    display: grid;
    gap: 16px;
    justify-items: start;
  }
  .settings-page-empty > :global(svg) {
    color: var(--accent);
    margin-bottom: 5px;
  }
  .settings-page-empty p {
    font-size: 13px;
    line-height: 1.8;
  }
  .settings-storage-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 16px;
    font-size: 12px;
  }
  .settings-body-copy {
    font-size: 13px;
    line-height: 1.9;
    color: var(--muted);
  }
  @media (max-width: 760px) {
    .settings-workspace {
      grid-template-columns: 178px minmax(0, 1fr);
    }
    .settings-sidebar {
      padding-inline: 9px;
    }
    .settings-brand {
      padding-inline: 9px;
      gap: 8px;
    }
    .settings-brand > span {
      font-size: 21px;
    }
    nav button {
      padding-inline: 10px;
      gap: 9px;
      font-size: 12px;
    }
    .settings-topbar {
      padding: 16px 24px;
    }
    .settings-content {
      padding: 30px 24px 40px;
    }
    .settings-card {
      padding: 22px;
    }
  }
  @media (max-width: 560px) {
    .settings-workspace {
      grid-template-columns: 1fr;
      grid-template-rows: auto minmax(0, 1fr);
    }
    .settings-sidebar {
      padding: 10px 14px;
      border-right: 0;
      border-bottom: 1px solid var(--border);
    }
    .settings-brand,
    .settings-nav-label,
    .settings-sidebar-bottom {
      display: none;
    }
    nav {
      display: flex;
      gap: 5px;
      overflow-x: auto;
    }
    nav button {
      width: auto;
      flex: 1;
      justify-content: center;
      padding: 10px;
      white-space: nowrap;
    }
    nav button :global(svg) {
      display: none;
    }
    .settings-topbar {
      min-height: 66px;
      padding: 13px 20px;
      gap: 8px;
    }
    .settings-topbar p {
      gap: 6px;
      font-size: 10px;
    }
    .settings-back {
      font-size: 11px;
      gap: 6px;
      padding-inline: 10px;
    }
    .settings-content {
      padding: 26px 20px 36px;
    }
    .settings-page-heading h1 {
      font-size: 27px;
    }
    .settings-page-heading {
      margin-bottom: 24px;
    }
    .settings-card {
      padding: 20px;
    }
  }
</style>
