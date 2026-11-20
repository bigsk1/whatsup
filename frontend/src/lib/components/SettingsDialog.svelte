<script lang="ts">
  import { groupedFingerprint } from '../state/fingerprint';
  import { tick } from 'svelte';
  import Icon from '../icons/Icon.svelte';
  import Logo from '../brand/Logo.svelte';
  import Avatar from './Avatar.svelte';
  import type { Status } from '../backend';
  import type { ConversationSnapshot } from '../messaging';
  import { validDisplayName, type LocalProfile } from '../profile';
  import type { Preferences } from '../themes/preferences';
  import {
    paletteCatalog,
    paletteGroups,
    paletteInfo,
    skinCatalog,
    skinInfo,
    textSizeLabels,
    textSizes,
  } from '../themes/catalog';
  import type { AppPreferences } from '../state/preferences';
  import type { StorageSecurity, SecurityOperation } from '../state/security';
  import RelayChangePanel from './RelayChangePanel.svelte';
  import type { RelayChangeState } from '../state/relay';
  import ContactStorage from './ContactStorage.svelte';
  import type { ContactStorageState } from '../state/storage';
  import type { AvatarState } from '../state/avatars';

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
    avatars,
    onchooseavatar,
    onremoveavatar,
    onavatarpreferences,
    onreloadavatars,
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
    contactStorage,
    oncontactstorage,
    onreloadstorage,
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
    avatars: AvatarState;
    onchooseavatar: () => void;
    onremoveavatar: () => void;
    onavatarpreferences: (share: boolean, show: boolean) => void;
    onreloadavatars: () => void;
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
    contactStorage: ContactStorageState;
    oncontactstorage: (id: string) => void;
    onreloadstorage: () => void;
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
  $effect(() => {
    oncontactstorage(
      open && unlocked && selected === 'contact' ? selectedContactID : '',
    );
  });
  const pages = [
    { id: 'profile', title: 'Profile', icon: 'user' },
    { id: 'contact', title: 'Contact', icon: 'contact' },
    { id: 'appearance', title: 'Appearance', icon: 'palette' },
    { id: 'app', title: 'App behavior', icon: 'bell' },
    { id: 'security', title: 'Security & recovery', icon: 'key' },
    { id: 'privacy', title: 'Privacy & diagnostics', icon: 'shield' },
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
  const paint = (color: string) => (node: HTMLElement) => {
    node.style.setProperty('background', color);
  };
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
        ><Logo size="2.4rem" /><span
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
            ><Icon name={page.icon} /><span>{page.title}</span></button
          >{/each}
      </nav>
      <div class="settings-sidebar-bottom">
        <div class="settings-device">
          <Icon name="lock" />
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
        <p>
          Settings <Icon name="chevronRight" size="0.9em" /><strong
            >{pageTitle}</strong
          >
        </p>
        <button class="secondary-button compact settings-back" onclick={close}
          ><Icon name="arrowLeft" size="1.1em" /> Back to chats</button
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
            <section
              class="settings-card"
              aria-labelledby="profile-picture-heading"
            >
              <div class="settings-card-heading">
                <div>
                  <h2 id="profile-picture-heading">Your profile picture</h2>
                  <p class="muted small">
                    Saved encrypted on this device. A picture is a label, not
                    identity verification.
                  </p>
                </div>
                <Avatar
                  name={profile?.displayName || 'Your device'}
                  seed={status.identityFingerprint}
                  photoURL={unlocked ? (avatars.snapshot?.ownURL ?? '') : ''}
                  size="large"
                />
              </div>
              <p class="muted small">
                Choose a still PNG, JPEG or WebP. WhatsUp crops it to a bounded
                square and removes metadata. Your picture always shows to you,
                even when sharing is off.
              </p>
              <div class="settings-save-row">
                <button
                  class="secondary-button compact"
                  disabled={!unlocked ||
                    busy ||
                    avatars.busy ||
                    avatars.loading ||
                    !avatars.snapshot}
                  onclick={onchooseavatar}
                  >{avatars.snapshot?.ownURL
                    ? 'Replace profile picture…'
                    : 'Choose profile picture…'}</button
                >
                <button
                  class="text-button"
                  disabled={!unlocked ||
                    busy ||
                    avatars.busy ||
                    avatars.loading ||
                    !avatars.snapshot?.ownURL}
                  onclick={onremoveavatar}>Remove profile picture</button
                >
                <button
                  class="text-button"
                  disabled={!unlocked ||
                    busy ||
                    avatars.busy ||
                    avatars.loading}
                  onclick={onreloadavatars}>Refresh pictures</button
                >
              </div>
              <label class="settings-toggle-row" for="share-avatar"
                ><span
                  ><strong>Share my profile picture</strong><small
                    >Off by default. Turning this off clears your shared picture
                    on each contact's device when it next connects with photo
                    support.</small
                  ></span
                ><input
                  id="share-avatar"
                  type="checkbox"
                  checked={avatars.snapshot?.shareAvatar ?? false}
                  disabled={!unlocked ||
                    busy ||
                    avatars.busy ||
                    avatars.loading ||
                    !avatars.snapshot}
                  onchange={(event) =>
                    onavatarpreferences(
                      event.currentTarget.checked,
                      avatars.snapshot?.showContactPhotos ?? true,
                    )}
                /><span class="toggle-track" aria-hidden="true"></span></label
              >
              <label class="settings-toggle-row" for="show-contact-photos"
                ><span
                  ><strong>Show contact photos</strong><small
                    >Use shared pictures for your contacts. When off, or no
                    picture was shared, their letter avatar stays.</small
                  ></span
                ><input
                  id="show-contact-photos"
                  type="checkbox"
                  checked={avatars.snapshot?.showContactPhotos ?? true}
                  disabled={!unlocked ||
                    busy ||
                    avatars.busy ||
                    avatars.loading ||
                    !avatars.snapshot}
                  onchange={(event) =>
                    onavatarpreferences(
                      avatars.snapshot?.shareAvatar ?? false,
                      event.currentTarget.checked,
                    )}
                /><span class="toggle-track" aria-hidden="true"></span></label
              >
              {#if avatars.notice}<p
                  class="settings-inline-notice"
                  role="status"
                >
                  {avatars.notice}
                </p>{/if}
              {#if avatars.loading}<p class="muted small" role="status">
                  Loading profile pictures…
                </p>{/if}
            </section>
            <section class="settings-card" aria-labelledby="profile-heading">
              <div class="settings-card-heading">
                <div>
                  <h2 id="profile-heading">Your display name</h2>
                  <p class="muted small">Saved encrypted on this device.</p>
                </div>
                <Icon name="user" />
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
                  <Icon name="lock" />
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
                <Icon name="shield" />
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
                  <Avatar
                    name={contactName}
                    seed={conversation.peerFingerprint}
                    photoURL={avatars.snapshot?.showContactPhotos
                      ? (avatars.snapshot.contacts.find(
                          (contact) => contact.contactID === selectedContactID,
                        )?.url ?? '')
                      : ''}
                  />
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
              <section class="settings-card" aria-label="Contact storage">
                <ContactStorage
                  value={contactStorage.contactID === selectedContactID
                    ? contactStorage
                    : {
                        contactID: selectedContactID,
                        usage: null,
                        loading: true,
                        notice: '',
                      }}
                  onreload={onreloadstorage}
                />
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
                <Icon name="contact" size="1.8em" />
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
              class="settings-card appearance-preview"
              aria-label="Appearance preview"
            >
              <div class="preview-window">
                <div class="preview-head">
                  <Logo size="2rem" />
                  <span class="preview-name">Sam</span>
                  <span class="status-pill tone-ok"
                    ><span class="status-dot"></span><span>Connected</span
                    ></span
                  >
                </div>
                <p class="preview-bubble">Hey! Are we still on for tonight?</p>
                <p class="preview-bubble outgoing">
                  Yes! I’ll bring the snacks 🍿
                </p>
              </div>
              <div class="preview-copy">
                <p class="eyebrow">Now showing</p>
                <h2>{skinInfo(preferences.skin).name}</h2>
                <p class="muted">
                  {paletteInfo(preferences.palette).name} colors ·
                  {textSizeLabels[preferences.textSize]} text
                </p>
                {#if preferences.palette !== skinInfo(preferences.skin).signature}
                  <button
                    class="secondary-button compact"
                    onclick={() =>
                      onpreferences({
                        ...preferences,
                        palette: skinInfo(preferences.skin).signature,
                      })}
                    ><Icon name="sparkle" size="1.1em" />Use {paletteInfo(
                      skinInfo(preferences.skin).signature,
                    ).name} colors</button
                  >
                {/if}
              </div>
            </section>
            <section class="settings-card" aria-labelledby="skin-heading">
              <div class="settings-card-heading">
                <div>
                  <h2 id="skin-heading">Look</h2>
                  <p class="muted small">
                    Fonts, shapes, icons and effects. Mix any look with any
                    colors below.
                  </p>
                </div>
                <Icon name="gamepad" />
              </div>
              <fieldset class="skin-options">
                <legend class="sr-only">Look</legend>
                {#each skinCatalog as skin (skin.id)}
                  <label
                    class="skin-choice skin-{skin.id}"
                    class:chosen={preferences.skin === skin.id}
                    ><input
                      type="radio"
                      name="skin"
                      value={skin.id}
                      aria-label={skin.name}
                      aria-describedby={`skin-${skin.id}-tagline`}
                      checked={preferences.skin === skin.id}
                      onchange={() =>
                        onpreferences({ ...preferences, skin: skin.id })}
                    /><span class="skin-art"
                      ><Logo size="3rem" skin={skin.id} /></span
                    ><span class="skin-name" aria-hidden="true"
                      >{skin.name}</span
                    ><span class="skin-tagline" id={`skin-${skin.id}-tagline`}
                      >{skin.tagline}</span
                    ></label
                  >
                {/each}
              </fieldset>
            </section>
            <section class="settings-card" aria-labelledby="palette-heading">
              <div class="settings-card-heading">
                <div>
                  <h2 id="palette-heading">Colors</h2>
                  <p class="muted small">
                    {preferences.followSystem
                      ? 'Used while your system is in dark mode.'
                      : 'Pick a color scheme. Appearance preferences stay on this device.'}
                  </p>
                </div>
                <Icon name="palette" />
              </div>
              {#each paletteGroups as group (group.id)}
                <fieldset class="palette-group">
                  <legend>{group.name}</legend>
                  <div class="palette-options">
                    {#each paletteCatalog.filter((palette) => palette.group === group.id) as palette (palette.id)}
                      <label
                        class="palette-choice"
                        class:chosen={preferences.palette === palette.id}
                        ><input
                          type="radio"
                          name="palette"
                          value={palette.id}
                          checked={preferences.palette === palette.id}
                          onchange={() =>
                            onpreferences({
                              ...preferences,
                              palette: palette.id,
                            })}
                        /><span
                          class="palette-art"
                          aria-hidden="true"
                          {@attach paint(palette.colors.bg)}
                          ><span
                            class="art-panel"
                            {@attach paint(palette.colors.panel)}
                          ></span><span
                            class="art-in"
                            {@attach paint(palette.colors.bubbleIn)}
                          ></span><span
                            class="art-out"
                            {@attach paint(palette.colors.bubbleOut)}
                          ></span><span
                            class="art-dot"
                            {@attach paint(palette.colors.accent2)}
                          ></span></span
                        ><span class="palette-name">{palette.name}</span></label
                      >
                    {/each}
                  </div>
                </fieldset>
              {/each}
              <label class="settings-toggle-row" for="follow-system"
                ><span
                  ><strong>Match system light and dark</strong><small
                    >Switch to a light color scheme when your system uses light
                    mode.</small
                  ></span
                ><input
                  id="follow-system"
                  type="checkbox"
                  checked={preferences.followSystem}
                  onchange={(event) =>
                    onpreferences({
                      ...preferences,
                      followSystem: event.currentTarget.checked,
                    })}
                /><span class="toggle-track" aria-hidden="true"></span></label
              >
              {#if preferences.followSystem}
                <fieldset class="palette-group">
                  <legend>Light mode colors</legend>
                  <div class="palette-options">
                    {#each paletteCatalog.filter((palette) => palette.scheme === 'light') as palette (palette.id)}
                      <label
                        class="palette-choice"
                        class:chosen={preferences.lightPalette === palette.id}
                        ><input
                          type="radio"
                          name="light-palette"
                          value={palette.id}
                          aria-label={`${palette.name} in light mode`}
                          checked={preferences.lightPalette === palette.id}
                          onchange={() =>
                            onpreferences({
                              ...preferences,
                              lightPalette: palette.id,
                            })}
                        /><span
                          class="palette-art"
                          aria-hidden="true"
                          {@attach paint(palette.colors.bg)}
                          ><span
                            class="art-panel"
                            {@attach paint(palette.colors.panel)}
                          ></span><span
                            class="art-in"
                            {@attach paint(palette.colors.bubbleIn)}
                          ></span><span
                            class="art-out"
                            {@attach paint(palette.colors.bubbleOut)}
                          ></span><span
                            class="art-dot"
                            {@attach paint(palette.colors.accent2)}
                          ></span></span
                        ><span class="palette-name">{palette.name}</span></label
                      >
                    {/each}
                  </div>
                </fieldset>
              {/if}
            </section>
            <section class="settings-card" aria-labelledby="text-size-heading">
              <div class="settings-card-heading">
                <div>
                  <h2 id="text-size-heading">Text size</h2>
                  <p class="muted small">
                    Scales every screen. Retro fonts get a little extra room.
                  </p>
                </div>
              </div>
              <fieldset class="size-options">
                <legend class="sr-only">Text size</legend>
                {#each textSizes as size (size)}
                  <label
                    class="size-choice"
                    class:chosen={preferences.textSize === size}
                    ><input
                      type="radio"
                      name="text-size"
                      value={size}
                      checked={preferences.textSize === size}
                      onchange={() =>
                        onpreferences({ ...preferences, textSize: size })}
                    /><span class="size-sample sample-{size}" aria-hidden="true"
                      >Aa</span
                    ><span>{textSizeLabels[size]}</span></label
                  >
                {/each}
              </fieldset>
              <label class="settings-toggle-row" for="effects"
                ><span
                  ><strong>Animations and effects</strong><small
                    >Glow, scanlines, blinking cursors and a little bounce. Your
                    system’s reduced-motion setting always wins.</small
                  ></span
                ><input
                  id="effects"
                  type="checkbox"
                  checked={preferences.effects}
                  onchange={(event) =>
                    onpreferences({
                      ...preferences,
                      effects: event.currentTarget.checked,
                    })}
                /><span class="toggle-track" aria-hidden="true"></span></label
              >
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
                {#each [{ key: 'closeToTray', label: 'Keep running when the window closes', help: 'Close to the tray. Choose Quit from the tray to exit.' }, { key: 'startAtLogin', label: 'Start WhatsUp when I sign in', help: 'Starts on this OS account only. This is off unless you enable it.' }, { key: 'notifications', label: 'Show new-message notifications', help: 'Notifications say only “New message” by default.' }, { key: 'notificationName', label: 'Include contact names in notifications', help: 'Allows names on the desktop and its notification history.' }, { key: 'notificationPreview', label: 'Include message previews in notifications', help: 'Allows message text on the desktop and its notification history.' }, { key: 'voiceCalls', label: 'Allow voice calls', help: 'Uses your microphone only during a call you place or answer. Off unless you turn it on. Headphones are recommended.' }, { key: 'callRingMuted', label: 'Silence the ring sound', help: 'Incoming calls still appear on screen.' }] as option}
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
                          !appPreferences.value.notifications) ||
                        (option.key === 'callRingMuted' &&
                          !appPreferences.value.voiceCalls)}
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
                {#if security.summary.backupRepairAvailable}
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
                {:else}
                  <p class="muted small">
                    This file or folder cannot be set aside safely. Keep its
                    contents and check its ownership, private permissions and
                    availability, then retry going online.
                  </p>
                {/if}
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
                <Icon name="lock" />
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
                <Icon name="shield" />
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
    grid-template-columns: 15rem minmax(0, 1fr);
    height: 100dvh;
    overflow: hidden;
  }
  .settings-sidebar {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
    padding: 1rem 0.75rem 0.85rem;
    background: color-mix(in srgb, var(--panel) 92%, transparent);
    border-right: var(--line) solid var(--border);
  }
  .settings-brand {
    display: flex;
    gap: 0.65rem;
    align-items: center;
    padding: 0.25rem;
    text-decoration: none;
  }
  .settings-brand > span {
    font-family: var(--font-display);
    font-weight: var(--display-weight);
    font-size: calc(1.4rem * var(--display-factor));
    letter-spacing: var(--display-tracking);
    text-transform: var(--display-case);
    line-height: 1.1;
  }
  .brand-period {
    color: var(--accent);
  }
  .settings-brand small {
    display: block;
    margin-top: 0.2rem;
    font-family: var(--font-body);
    font-weight: var(--body-weight);
    font-size: 0.72rem;
    letter-spacing: 0.12em;
    color: var(--faint);
  }
  .settings-nav-label {
    margin: 1.75rem 0.6rem 0.6rem;
    font-family: var(--font-display);
    font-size: calc(0.72rem * max(var(--display-factor), 0.8));
    font-weight: var(--display-weight);
    letter-spacing: 0.14em;
    color: var(--faint);
  }
  nav {
    display: grid;
    gap: 0.25rem;
  }
  nav button {
    display: flex;
    align-items: center;
    gap: 0.7rem;
    width: 100%;
    text-align: left;
    padding: 0.6rem 0.75rem;
    border: var(--line) solid transparent;
    border-radius: var(--radius);
    color: var(--muted);
    font-size: 0.95rem;
    font-weight: var(--strong-weight);
  }
  nav button:hover {
    color: var(--text);
    background: var(--hover);
  }
  nav button.selected {
    background: var(--accent-soft);
    border-color: var(--accent-line);
    color: var(--text);
    box-shadow: var(--glow);
  }
  nav button.selected :global(.icon) {
    color: var(--accent);
  }
  .settings-sidebar-bottom {
    margin-top: auto;
    padding: 1rem 0.5rem 0;
  }
  .settings-device {
    display: flex;
    align-items: flex-start;
    gap: 0.6rem;
    color: var(--muted);
  }
  .settings-device p {
    color: var(--text);
    font-size: 0.85rem;
    line-height: 1.5;
  }
  .settings-device small {
    display: block;
    color: var(--muted);
    font-size: 0.78rem;
  }
  .settings-version {
    display: flex;
    justify-content: space-between;
    margin-top: 0.9rem;
    padding-top: 0.75rem;
    border-top: var(--line) solid var(--border);
    font-size: 0.75rem;
    color: var(--faint);
  }
  .settings-main {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
  }
  .settings-topbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    min-height: 4.25rem;
    padding: 0.6rem 1.75rem;
    border-bottom: var(--line) solid var(--border);
    background: color-mix(in srgb, var(--panel) 75%, transparent);
  }
  .settings-topbar p {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.88rem;
    color: var(--muted);
  }
  .settings-topbar strong {
    color: var(--text);
  }
  .settings-scroll {
    flex: 1;
    min-height: 0;
    overflow: auto;
    overscroll-behavior: contain;
  }
  .settings-content {
    width: min(100%, 60rem);
    margin: 0 auto;
    padding: 1.75rem 1.75rem 3rem;
  }
  .settings-page-heading {
    margin-bottom: 1.4rem;
  }
  .settings-page-heading .eyebrow {
    margin-bottom: 0.5rem;
  }
  .settings-page-heading h1 {
    margin-bottom: 0.4rem;
    font-size: calc(2rem * var(--display-factor));
    text-transform: var(--display-case);
    outline: 0;
  }
  :global([data-skin='arcade']) .settings-page-heading h1 {
    font-size: 1rem;
    line-height: 1.6;
  }
  .settings-page-heading > .muted {
    font-size: 1rem;
  }
  .settings-card {
    padding: 1.25rem;
    border-radius: var(--radius-lg);
    border: var(--line) solid var(--border);
    background: var(--surface);
    box-shadow: var(--shadow-1);
  }
  .settings-card + .settings-card {
    margin-top: 1rem;
  }
  .settings-card-heading {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 1rem;
    margin-bottom: 1rem;
  }
  .settings-card-heading > :global(.icon) {
    color: var(--accent);
    font-size: 1.3rem;
  }
  .settings-card h2 {
    font-size: calc(1.1rem * var(--display-factor));
    line-height: 1.4;
    overflow-wrap: anywhere;
  }
  :global([data-skin='arcade']) .settings-card h2 {
    font-size: 0.65rem;
    line-height: 1.7;
  }
  .settings-card-heading p {
    margin-top: 0.25rem;
  }
  .settings-field {
    display: block;
    margin-bottom: 0.5rem;
    font-size: 0.9rem;
    font-weight: var(--strong-weight);
  }
  .settings-input {
    width: 100%;
    max-width: 32rem;
    padding: 0.65rem 0.85rem;
    background: var(--bg);
    color: var(--text);
    border: var(--line) solid var(--border);
    border-radius: var(--radius-sm);
    font-size: 1rem;
  }
  .settings-input:focus-visible {
    border-color: var(--accent);
    outline: none;
    box-shadow: 0 0 0 3px var(--accent-soft);
  }
  .settings-input:disabled {
    opacity: 0.5;
  }
  .settings-input::placeholder {
    color: var(--faint);
  }
  .field-help {
    margin-top: 0.5rem;
  }
  .settings-toggle-row {
    position: relative;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1.25rem;
    padding: 0.9rem 0;
    margin-top: 1rem;
    border-top: var(--line) solid var(--border);
    cursor: pointer;
  }
  .settings-toggle-row strong {
    display: block;
    font-size: 0.95rem;
  }
  .settings-toggle-row small {
    display: block;
    margin-top: 0.2rem;
    color: var(--muted);
    font-size: 0.85rem;
    line-height: 1.5;
  }
  .settings-toggle-row input {
    position: absolute;
    right: 0;
    width: 2.6rem;
    height: 1.5rem;
    opacity: 0;
  }
  .toggle-track {
    display: block;
    position: relative;
    flex-shrink: 0;
    width: 2.6rem;
    height: 1.5rem;
    border-radius: var(--radius-pill);
    background: var(--raised);
    border: var(--line) solid var(--border);
    pointer-events: none;
  }
  .toggle-track::after {
    content: '';
    position: absolute;
    top: 50%;
    left: 0.2rem;
    width: 1rem;
    height: 1rem;
    border-radius: var(--radius-pill);
    background: var(--muted);
    transform: translateY(-50%);
    transition: transform 140ms;
  }
  .settings-toggle-row input:checked + .toggle-track {
    background: var(--accent);
    border-color: var(--accent);
    box-shadow: var(--glow);
  }
  .settings-toggle-row input:checked + .toggle-track::after {
    background: var(--on-accent);
    transform: translate(1.05rem, -50%);
  }
  .settings-toggle-row input:focus-visible + .toggle-track {
    outline: 2px solid var(--accent);
    outline-offset: 3px;
  }
  .settings-toggle-row input:disabled + .toggle-track {
    opacity: 0.4;
  }
  .app-preference-row {
    margin-top: 0;
  }
  .app-preference-row:first-of-type {
    margin-top: 0.5rem;
    border-top: 0;
  }
  .app-preference-copy {
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .settings-save-row {
    display: flex;
    align-items: center;
    gap: 1rem;
    margin-top: 1.1rem;
  }
  .settings-inline-notice {
    margin-top: 0.85rem;
    font-size: 0.9rem;
    color: var(--accent);
  }
  .settings-empty-note {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.75rem;
    margin-top: 1.1rem;
    padding: 0.75rem 0.9rem;
    border-radius: var(--radius);
    background: var(--accent-soft);
    color: var(--muted);
    font-size: 0.9rem;
  }
  .settings-empty-note p {
    flex: 1;
    min-width: 11rem;
  }
  .settings-details {
    margin: 1.25rem 0 0;
  }
  .settings-details > div {
    margin-top: 0.9rem;
    padding-top: 0.9rem;
    border-top: var(--line) solid var(--border);
  }
  dt {
    margin-bottom: 0.4rem;
    font-size: 0.85rem;
    color: var(--muted);
  }
  dd {
    margin: 0;
    font-size: 1rem;
    overflow-wrap: anywhere;
  }
  .settings-page-empty {
    display: grid;
    gap: 0.75rem;
    justify-items: start;
  }
  .settings-page-empty > :global(.icon) {
    color: var(--accent);
  }
  .settings-storage-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 0.75rem;
    font-size: 0.95rem;
  }
  .settings-body-copy {
    font-size: 0.95rem;
    line-height: 1.7;
    color: var(--muted);
  }

  .appearance-preview {
    display: grid;
    grid-template-columns: minmax(0, 1.2fr) minmax(0, 1fr);
    gap: 1.25rem;
    align-items: center;
  }
  .preview-window {
    display: grid;
    gap: 0.5rem;
    padding: 0.85rem;
    border-radius: var(--radius);
    background: var(--bg);
    border: var(--line) solid var(--border);
  }
  .preview-head {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding-bottom: 0.5rem;
    border-bottom: var(--line) solid var(--border);
  }
  .preview-name {
    flex: 1;
    font-family: var(--font-display);
    font-weight: var(--display-weight);
    font-size: calc(1rem * var(--display-factor));
    text-transform: var(--display-case);
  }
  .preview-bubble {
    justify-self: start;
    max-width: 85%;
    padding: 0.45rem 0.8rem;
    border-radius: var(--bubble-radius) var(--bubble-radius)
      var(--bubble-radius) var(--bubble-tail);
    background: var(--bubble-in);
    color: var(--on-bubble-in);
    font-size: 0.95rem;
  }
  .preview-bubble.outgoing {
    justify-self: end;
    border-radius: var(--bubble-radius) var(--bubble-radius) var(--bubble-tail)
      var(--bubble-radius);
    background: var(--bubble-out);
    color: var(--on-bubble-out);
    box-shadow: var(--glow);
  }
  .preview-copy {
    display: grid;
    gap: 0.4rem;
    justify-items: start;
  }
  .preview-copy h2 {
    font-size: calc(1.6rem * var(--display-factor));
    text-transform: var(--display-case);
  }
  :global([data-skin='arcade']) .preview-copy h2 {
    font-size: 0.9rem;
  }

  fieldset {
    margin: 0;
    padding: 0;
    border: 0;
    min-width: 0;
  }
  label input[type='radio'] {
    position: absolute;
    width: 1px;
    height: 1px;
    opacity: 0;
  }
  .skin-options {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(10.5rem, 1fr));
    gap: 0.75rem;
  }
  .skin-choice,
  .palette-choice,
  .size-choice {
    position: relative;
    cursor: pointer;
    border: 2px solid var(--border);
    background: var(--panel);
    transition:
      border-color 140ms,
      transform 140ms,
      box-shadow 140ms;
  }
  .skin-choice:hover,
  .palette-choice:hover,
  .size-choice:hover {
    border-color: var(--accent-line);
    transform: translateY(-2px);
  }
  .skin-choice.chosen,
  .palette-choice.chosen,
  .size-choice.chosen {
    border-color: var(--accent);
    box-shadow:
      0 0 0 3px var(--accent-soft),
      var(--glow);
  }
  .skin-choice:focus-within,
  .palette-choice:focus-within,
  .size-choice:focus-within {
    outline: 2px solid var(--accent);
    outline-offset: 3px;
  }
  .skin-choice {
    display: grid;
    justify-items: start;
    gap: 0.35rem;
    padding: 0.85rem;
    border-radius: var(--radius);
  }
  .skin-art {
    display: grid;
    place-items: center;
    width: 100%;
    height: 4.5rem;
    margin-bottom: 0.25rem;
    border-radius: var(--radius-sm);
    background: var(--bg);
  }
  .skin-name {
    font-size: 1.05rem;
    font-weight: var(--strong-weight);
    color: var(--text);
  }
  .skin-tagline {
    font-size: 0.82rem;
    line-height: 1.4;
    color: var(--muted);
  }
  .skin-bubble .skin-name {
    font-family: 'Nunito', sans-serif;
    font-weight: 800;
  }
  .skin-arcade {
    border-radius: 0;
  }
  .skin-arcade .skin-name {
    font-family: 'Press Start 2P', monospace;
    font-size: 0.6rem;
    line-height: 1.8;
    text-transform: uppercase;
  }
  .skin-arcade .skin-tagline {
    font-family: 'VT323', monospace;
    font-size: 1.05rem;
    line-height: 1.1;
  }
  .skin-neon {
    border-radius: 4px;
  }
  .skin-neon .skin-name {
    font-family: 'Orbitron', sans-serif;
    font-weight: 800;
    font-size: 0.85rem;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }
  .skin-neon .skin-tagline {
    font-family: 'Rajdhani', sans-serif;
    font-weight: 600;
  }
  .skin-terminal {
    border-radius: 0;
  }
  .skin-terminal .skin-name,
  .skin-terminal .skin-tagline {
    font-family: 'JetBrains Mono', monospace;
  }
  .skin-terminal .skin-name {
    font-size: 0.95rem;
  }
  .skin-terminal .skin-tagline {
    font-size: 0.75rem;
  }
  .palette-group + .palette-group {
    margin-top: 1rem;
  }
  .palette-group legend {
    margin-bottom: 0.5rem;
    font-size: 0.82rem;
    font-weight: var(--strong-weight);
    color: var(--muted);
  }
  .palette-options {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(8.5rem, 1fr));
    gap: 0.6rem;
  }
  .palette-choice {
    display: grid;
    gap: 0.4rem;
    padding: 0.45rem;
    border-radius: var(--radius);
  }
  .palette-art {
    position: relative;
    display: block;
    height: 3.4rem;
    overflow: hidden;
    border-radius: var(--radius-sm);
    box-shadow: inset 0 0 0 1px rgb(127 127 127 / 0.25);
  }
  .palette-art > span {
    position: absolute;
    display: block;
  }
  .art-panel {
    inset: 0 auto 0 0;
    width: 28%;
  }
  .art-in {
    left: 36%;
    top: 18%;
    width: 38%;
    height: 22%;
    border-radius: 999px;
  }
  .art-out {
    right: 8%;
    bottom: 16%;
    width: 44%;
    height: 24%;
    border-radius: 999px;
  }
  .art-dot {
    left: 9%;
    top: 18%;
    width: 0.65rem;
    height: 0.65rem;
    border-radius: 50%;
  }
  .palette-name {
    font-size: 0.85rem;
    font-weight: var(--strong-weight);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .size-options {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 0.6rem;
  }
  .size-choice {
    display: grid;
    justify-items: center;
    gap: 0.2rem;
    padding: 0.65rem 0.4rem;
    border-radius: var(--radius);
    font-size: 0.85rem;
    text-align: center;
  }
  .size-sample {
    font-family: var(--font-display);
    font-weight: var(--display-weight);
    line-height: 1.2;
    color: var(--accent);
  }
  .sample-compact {
    font-size: 0.95rem;
  }
  .sample-comfortable {
    font-size: 1.2rem;
  }
  .sample-large {
    font-size: 1.45rem;
  }
  .sample-huge {
    font-size: 1.7rem;
  }

  @media (max-width: 760px) {
    .settings-workspace {
      grid-template-columns: 12rem minmax(0, 1fr);
    }
    .settings-sidebar {
      padding-inline: 0.5rem;
    }
    nav button {
      padding-inline: 0.55rem;
      gap: 0.5rem;
      font-size: 0.88rem;
    }
    .settings-topbar {
      padding: 0.6rem 1.1rem;
    }
    .settings-content {
      padding: 1.25rem 1.1rem 2.5rem;
    }
    .appearance-preview {
      grid-template-columns: 1fr;
    }
  }
  @media (max-width: 560px) {
    .settings-workspace {
      grid-template-columns: 1fr;
      grid-template-rows: auto minmax(0, 1fr);
    }
    .settings-sidebar {
      padding: 0.5rem 0.75rem;
      border-right: 0;
      border-bottom: var(--line) solid var(--border);
    }
    .settings-brand,
    .settings-nav-label,
    .settings-sidebar-bottom {
      display: none;
    }
    nav {
      display: flex;
      gap: 0.3rem;
      overflow-x: auto;
    }
    nav button {
      width: auto;
      flex: 1;
      justify-content: center;
      padding: 0.5rem 0.65rem;
      white-space: nowrap;
    }
    nav button :global(.icon) {
      display: none;
    }
    .settings-topbar {
      min-height: 3.5rem;
      padding: 0.5rem 0.9rem;
      gap: 0.5rem;
    }
    .settings-topbar p {
      gap: 0.3rem;
      font-size: 0.8rem;
    }
    .settings-content {
      padding: 1.1rem 0.9rem 2rem;
    }
    .size-options {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
  }
</style>
