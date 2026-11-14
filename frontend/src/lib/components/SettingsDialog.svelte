<script lang="ts">
  import { LockKeyhole, Palette, ShieldCheck, X } from '@lucide/svelte';
  import type { Status } from '../backend';
  import type { Accent, Preferences, Theme } from '../themes/preferences';

  let {
    status,
    preferences,
    native,
    busy,
    onpreferences,
    onstorage,
  }: {
    status: Status;
    preferences: Preferences;
    native: boolean;
    busy: boolean;
    onpreferences: (value: Preferences) => void;
    onstorage: () => void;
  } = $props();
  let dialog: HTMLDialogElement;

  export function show() {
    dialog.showModal();
  }
</script>

<dialog
  bind:this={dialog}
  aria-labelledby="settings-title"
  class="settings-dialog"
>
  <div class="dialog-heading">
    <div class="dialog-title">
      <div class="small-icon"><Palette size={19} strokeWidth={1.8} /></div>
      <div>
        <p class="eyebrow">MAKE IT YOURS</p>
        <h2 id="settings-title">Settings</h2>
      </div>
    </div>
    <button
      class="icon-button"
      aria-label="Close settings"
      onclick={() => dialog.close()}><X size={19} /></button
    >
  </div>

  <div class="dialog-body">
    <section aria-labelledby="appearance-heading" class="settings-section">
      <h3 id="appearance-heading">Appearance</h3>
      <p class="muted small">A little more you. Saved only on this device.</p>
      <fieldset class="theme-options">
        <legend>Theme</legend>
        {#each ['dark', 'light', 'system'] as theme}
          <label class="choice" class:chosen={preferences.theme === theme}>
            <input
              type="radio"
              name="theme"
              value={theme}
              checked={preferences.theme === theme}
              onchange={() =>
                onpreferences({ ...preferences, theme: theme as Theme })}
            />
            <span
              class="theme-preview"
              class:light-preview={theme === 'light'}
              class:system-preview={theme === 'system'}
              ><span></span><span></span></span
            >
            <span class="choice-label"
              >{theme[0].toUpperCase() + theme.slice(1)}</span
            >
          </label>
        {/each}
      </fieldset>
      <fieldset class="accent-options">
        <legend>Accent color</legend>
        {#each ['teal', 'blue', 'rose'] as accent}
          <label
            class="accent-choice"
            class:chosen={preferences.accent === accent}
          >
            <input
              type="radio"
              name="accent"
              value={accent}
              checked={preferences.accent === accent}
              onchange={() =>
                onpreferences({ ...preferences, accent: accent as Accent })}
            />
            <span class="accent-swatch" data-color={accent}></span>
            <span>{accent[0].toUpperCase() + accent.slice(1)}</span>
          </label>
        {/each}
      </fieldset>
    </section>

    <section aria-labelledby="storage-heading" class="settings-section">
      <div class="section-label">
        <LockKeyhole size={17} />
        <h3 id="storage-heading">Local storage</h3>
      </div>
      <p class="muted small">
        {native
          ? 'Your OS credential store protects the local encryption key.'
          : 'Local storage is available in the desktop app.'}
      </p>
      <div class="storage-row">
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
              ? 'Lock storage'
              : 'Unlock storage'}</button
        >
      </div>
      {#if status.identityFingerprint}
        <label class="fingerprint-label" for="identity-fingerprint"
          >Your identity fingerprint</label
        >
        <output id="identity-fingerprint" class="fingerprint"
          >{status.identityFingerprint}</output
        >
      {/if}
    </section>

    <div class="privacy-note">
      <ShieldCheck size={18} />
      <p>
        Appearance preferences and redacted troubleshooting logs stay on this
        device. WhatsUp has no analytics or remote crash reporting. Windows
        WebView2 may send its own diagnostics to Microsoft.
      </p>
    </div>
  </div>
  <div class="dialog-footer">
    <span class="muted small">WhatsUp · {status.version}</span><button
      class="primary-button compact"
      onclick={() => dialog.close()}>Done</button
    >
  </div>
</dialog>
