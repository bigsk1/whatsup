<script lang="ts">
  import type { ContactSummary } from '../state/bridge';
  import { contactLabel, connectionLabel } from '../state/contacts';
  import { presenceTone } from '../presence';
  import Avatar from './Avatar.svelte';

  let {
    contacts,
    selectedID,
    onselect,
    photoURLs = {},
  }: {
    contacts: ContactSummary[];
    selectedID: string;
    onselect: (contactID: string) => void;
    photoURLs?: Record<string, string>;
  } = $props();
</script>

<div class="contact-list" aria-label="Saved conversations">
  {#each contacts as contact (contact.contactID)}
    <button
      type="button"
      class="approved-contact"
      class:selected={selectedID === contact.contactID}
      aria-label={`Conversation with ${contactLabel(contact)}`}
      aria-pressed={selectedID === contact.contactID}
      title={contactLabel(contact)}
      onclick={() => onselect(contact.contactID)}
    >
      <Avatar
        name={contactLabel(contact)}
        seed={contact.peerFingerprint}
        presence={presenceTone(contact.state)}
        photoURL={photoURLs[contact.contactID] ?? ''}
      />
      <span class="contact-details">
        <strong>{contactLabel(contact)}</strong>
        <span class="contact-status tone-{presenceTone(contact.state)}"
          >{connectionLabel(contact.state)}</span
        >
        <span class="contact-meta"
          ><span aria-label="Connection path"
            >{contact.state === 'connected' ? contact.path : 'Unknown'}</span
          ><code>{contact.peerFingerprint.slice(0, 12)}…</code></span
        >
      </span>
      {#if contact.unread > 0}
        <span
          class="unread-count"
          aria-label={`${contact.unread} unread messages`}
          >{contact.unread > 99 ? '99+' : contact.unread}</span
        >
      {/if}
    </button>
  {/each}
</div>

<style>
  .contact-list {
    display: grid;
    gap: 0.3rem;
  }
  .approved-contact {
    position: relative;
    display: flex;
    align-items: center;
    gap: 0.7rem;
    width: 100%;
    margin: 0;
    padding: 0.55rem 0.6rem;
    border: var(--line) solid transparent;
    border-radius: var(--radius);
    text-align: left;
    color: inherit;
  }
  .approved-contact:hover {
    background: var(--hover);
  }
  .approved-contact.selected {
    background: var(--accent-soft);
    border-color: var(--accent-line);
    box-shadow: var(--glow);
  }
  :global([data-skin='terminal']) .approved-contact.selected {
    background: var(--accent);
    color: var(--on-accent);
    text-shadow: none;
  }
  :global([data-skin='terminal']) .approved-contact.selected .contact-status,
  :global([data-skin='terminal']) .approved-contact.selected .contact-meta,
  :global([data-skin='terminal']) .approved-contact.selected :global(.avatar) {
    color: inherit;
  }
  .approved-contact :global(.presence) {
    border-color: var(--panel);
  }
  .contact-details {
    display: grid;
    flex: 1;
    min-width: 0;
    gap: 0.05rem;
  }
  .contact-details strong {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 1rem;
  }
  .contact-status {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 0.8rem;
    font-weight: var(--strong-weight);
    color: var(--tone);
  }
  .contact-meta {
    display: flex;
    gap: 0.4rem;
    min-width: 0;
    font-size: 0.72rem;
    color: var(--faint);
    white-space: nowrap;
  }
  .contact-meta code {
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .unread-count {
    flex-shrink: 0;
    min-width: 1.45rem;
    padding: 0.1rem 0.4rem;
    border-radius: var(--radius-pill);
    background: var(--accent);
    color: var(--on-accent);
    font-size: 0.75rem;
    font-weight: var(--strong-weight);
    text-align: center;
  }
  @media (max-width: 820px) {
    .approved-contact {
      justify-content: center;
      padding: 0.4rem 0;
    }
    .contact-details {
      display: none;
    }
    .unread-count {
      position: absolute;
      top: 0.1rem;
      right: 0.1rem;
    }
  }
</style>
