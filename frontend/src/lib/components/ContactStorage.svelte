<script lang="ts">
  import type { ContactStorageState } from '../state/storage';
  let {
    value,
    onreload,
  }: { value: ContactStorageState; onreload: () => void } = $props();
  const bytes = (amount: number) =>
    amount >= 2 ** 30
      ? `${(amount / 2 ** 30).toFixed(2)} GiB`
      : amount >= 2 ** 20
        ? `${(amount / 2 ** 20).toFixed(1)} MiB`
        : amount >= 1024
          ? `${(amount / 1024).toFixed(1)} KiB`
          : `${amount} B`;
</script>

<div aria-labelledby="contact-storage-heading" aria-busy={value.loading}>
  <div class="settings-card-heading">
    <div>
      <h2 id="contact-storage-heading">Storage on this device</h2>
      <p class="muted small">Separate budgets for this contact.</p>
    </div>
    <button class="text-button" disabled={value.loading} onclick={onreload}
      >Refresh storage usage</button
    >
  </div>
  {#if value.usage}
    {@const usage = value.usage}
    <div class="storage-budget">
      <div>
        <strong>Message text</strong><span
          >{bytes(usage.messagesUsedBytes)} of {bytes(
            usage.messagesQuotaBytes,
          )}</span
        >
      </div>
      <progress
        aria-label="Message text storage"
        max={usage.messagesQuotaBytes}
        value={Math.min(usage.messagesUsedBytes, usage.messagesQuotaBytes)}
      ></progress>
      {#if usage.messagesUsedBytes >= usage.messagesQuotaBytes}<p role="status">
          Message storage full
        </p>{/if}
    </div>
    <div class="storage-budget">
      <div>
        <strong>Photos, videos &amp; files</strong><span
          >{bytes(usage.attachmentsUsedBytes)} of {bytes(
            usage.attachmentsQuotaBytes,
          )}</span
        >
      </div>
      <progress
        aria-label="Attachment storage"
        max={usage.attachmentsQuotaBytes}
        value={Math.min(
          usage.attachmentsUsedBytes,
          usage.attachmentsQuotaBytes,
        )}
      ></progress>
      {#if usage.attachmentsUsedBytes >= usage.attachmentsQuotaBytes}<p
          role="status"
        >
          Attachment storage full
        </p>{/if}
    </div>
    <p class="muted small">
      Usage includes encrypted records and space reserved for queued or accepted
      files, including unfinished transfers. Small records remain to prevent
      duplicate delivery after deletion. Copies saved outside WhatsUp are
      excluded. These budgets do not measure free disk space.
    </p>
  {:else if value.loading}<p class="muted small">Loading storage usage…</p>{/if}
  {#if value.notice}<p role="alert">{value.notice}</p>{/if}
</div>

<style>
  .settings-card-heading {
    display: flex;
    flex-wrap: wrap;
    align-items: start;
    justify-content: space-between;
    gap: 0.75rem;
    margin-bottom: 1rem;
  }
  h2 {
    font-size: calc(1.1rem * var(--display-factor));
    line-height: 1.4;
    overflow-wrap: anywhere;
  }
  :global([data-skin='arcade']) h2 {
    font-size: 0.65rem;
    line-height: 1.7;
  }
  .storage-budget {
    margin-block: 1rem;
  }
  .storage-budget > div {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem 1rem;
    justify-content: space-between;
  }
  .storage-budget span {
    color: var(--muted);
  }
  progress {
    display: block;
    width: 100%;
    height: 0.6rem;
    margin-top: 0.5rem;
    accent-color: var(--accent);
  }
</style>
