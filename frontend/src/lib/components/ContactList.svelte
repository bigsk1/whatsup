<script lang="ts">
  import { Shield } from '@lucide/svelte';
  import type { ContactSummary } from '../state/bridge';
  import { contactLabel, connectionLabel } from '../state/contacts';

  let {
    contacts,
    selectedID,
    onselect,
  }: {
    contacts: ContactSummary[];
    selectedID: string;
    onselect: (contactID: string) => void;
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
      onclick={() => onselect(contact.contactID)}
    >
      <span class="contact-avatar"><Shield size={21} /></span>
      <span class="contact-details">
        <strong>{contactLabel(contact)}</strong>
        <span>{connectionLabel(contact.state)}</span>
        <code>{contact.peerFingerprint.slice(0, 12)}…</code>
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
  .approved-contact {
    display: flex;
    align-items: center;
    gap: 12px;
    margin: 0;
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
  .contact-details span,
  .contact-details code {
    display: block;
  }
  .approved-contact strong {
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .contact-details {
    min-width: 0;
  }
  .contact-details span,
  .contact-details code {
    margin-top: 5px;
    font-size: 10px;
    color: var(--muted);
  }

  .contact-list {
    display: grid;
    gap: 0.5rem;
  }
  .approved-contact {
    width: 100%;
    text-align: left;
    color: inherit;
    font: inherit;
    cursor: pointer;
  }
  .approved-contact:not(.selected) {
    background: transparent;
  }
  .contact-details {
    display: grid;
    flex: 1;
    min-width: 0;
  }
  .unread-count {
    padding: 0.15rem 0.4rem;
    border-radius: 1rem;
    background: var(--accent);
    color: var(--bg);
    font-size: 0.75rem;
  }
</style>
