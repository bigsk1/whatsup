<script lang="ts">
  import type { AttachmentDraft, AttachmentItem } from '../state/attachments';
  let {
    items,
    drafts,
    busy,
    notice,
    videos,
    saved,
    autoAcceptImages,
    hasMore,
    onolder,
    onpick,
    onpaste,
    onsend,
    ondiscard,
    ondecision,
    onautoaccept,
    onpreview,
    onsave,
    onreveal,
    wireAllowed,
    stageAllowed,
    wireNotice,
  }: {
    items: AttachmentItem[];
    drafts: AttachmentDraft[];
    busy: boolean;
    notice: string;
    videos: Record<string, string>;
    saved: Record<string, string>;
    autoAcceptImages: boolean;
    hasMore: boolean;
    onolder: () => void;
    onpick: (original: boolean) => void;
    onpaste: () => void;
    onsend: (draft: AttachmentDraft, allow: boolean) => void;
    ondiscard: (draft: AttachmentDraft) => void;
    ondecision: (item: AttachmentItem, action: string) => void;
    onautoaccept: (enabled: boolean) => void;
    onpreview: (item: AttachmentItem) => void;
    onsave: (item: AttachmentItem) => void;
    onreveal: (item: AttachmentItem) => void;
    wireAllowed: boolean;
    stageAllowed: boolean;
    wireNotice: string;
  } = $props();
  let original = $state(false);
  let relay = $state<Record<string, boolean>>({});
  const size = (bytes: number) =>
    bytes >= 2 ** 30
      ? `${(bytes / 2 ** 30).toFixed(2)} GiB`
      : bytes >= 2 ** 20
        ? `${(bytes / 2 ** 20).toFixed(1)} MiB`
        : `${Math.ceil(bytes / 1024)} KiB`;
</script>

<section class="attachments" aria-label="Attachments">
  <div class="attachment-actions">
    <button
      type="button"
      class="secondary-button compact"
      disabled={busy || !stageAllowed}
      onclick={() => onpick(original)}>Choose file…</button
    >
    <button
      type="button"
      class="secondary-button compact"
      disabled={busy || !stageAllowed}
      onclick={onpaste}>Paste image</button
    >
    <label
      ><input
        type="checkbox"
        bind:checked={original}
        disabled={busy || !stageAllowed}
      /> Send as file (keep original bytes)</label
    >
  </div>
  <p class="hint">
    Images are re-encoded to remove metadata by default. Files stay local until
    you choose Send. One file per drop, up to 2 GiB.
  </p>
  <label class="image-permission"
    ><input
      type="checkbox"
      checked={autoAcceptImages}
      disabled={busy}
      onchange={(event) => onautoaccept(event.currentTarget.checked)}
    /> Automatically accept images up to 25 MB from this contact</label
  >
  {#if notice}<p role="alert">{notice}</p>{/if}
  {#if wireNotice}<p role="status">{wireNotice}</p>{/if}
  {#each drafts as draft (draft.handle)}
    <article class="attachment-draft" aria-label="Attachment preview">
      {#if draft.previewURL}<img
          src={draft.previewURL}
          alt="Ready to send"
          loading="lazy"
        />{/if}
      <p dir="auto"><strong>{draft.name}</strong> · {size(draft.size)}</p>
      {#if draft.requiresRelayWarning}
        <label
          ><input
            type="checkbox"
            checked={Boolean(relay[draft.handle])}
            onchange={(event) =>
              (relay = {
                ...relay,
                [draft.handle]: event.currentTarget.checked,
              })}
          /> This large transfer may use relay bandwidth. Allow it to continue?</label
        >
      {/if}
      <button
        type="button"
        class="secondary-button compact"
        disabled={busy ||
          !wireAllowed ||
          (draft.requiresRelayWarning && !relay[draft.handle])}
        onclick={() => onsend(draft, Boolean(relay[draft.handle]))}
        >Send file</button
      >
      <button
        type="button"
        class="text-button"
        disabled={busy}
        onclick={() => ondiscard(draft)}>Discard</button
      >
    </article>
  {/each}
  <div class="attachment-list">
    {#each items as item (item.id)}
      <article
        aria-label={item.direction === 'incoming'
          ? 'Received attachment'
          : 'Sent attachment'}
      >
        <p dir="auto">
          <strong>{item.name}</strong> · {size(item.size)} · {item.state}
        </p>
        {#if item.previewURL}<img
            src={item.previewURL}
            alt={item.name}
            loading="lazy"
          />{/if}
        {#if item.state === 'offered' && item.direction === 'incoming'}
          <button
            type="button"
            class="secondary-button compact"
            disabled={busy || !wireAllowed}
            onclick={() => ondecision(item, 'accept')}>Accept</button
          >
          <button
            type="button"
            class="text-button"
            disabled={busy}
            onclick={() => ondecision(item, 'decline')}>Decline</button
          >
        {:else if item.state === 'paused'}
          <button
            type="button"
            class="secondary-button compact"
            disabled={busy || !wireAllowed}
            onclick={() => ondecision(item, 'resume')}>Resume</button
          >
        {:else if item.state === 'complete'}
          {#if item.mediaType === 'video/mp4' || item.mediaType === 'video/webm'}
            {#if videos[item.id]}<video
                src={videos[item.id]}
                controls
                playsinline
                preload="none"
                ><track kind="captions" />Your system cannot play this video.
                Save it to view with another app.</video
              >
            {:else}<button
                type="button"
                class="secondary-button compact"
                disabled={busy}
                onclick={() => onpreview(item)}>Play video</button
              >{/if}
            <p class="hint">
              Linux playback depends on installed GStreamer codecs.
            </p>
          {/if}
          <button
            type="button"
            class="secondary-button compact"
            disabled={busy}
            onclick={() => onsave(item)}>Save…</button
          >
          {#if saved[item.id]}<button
              type="button"
              class="text-button"
              disabled={busy}
              onclick={() => onreveal(item)}>Show in folder</button
            >{/if}
        {:else if item.state !== 'declined' && item.state !== 'failed'}
          <progress
            max={item.size || 1}
            value={item.transferredBytes}
            aria-label="File transfer progress"
          ></progress>
          <span>{size(item.transferredBytes)} / {size(item.size)}</span>
          <button
            type="button"
            class="text-button"
            disabled={busy}
            onclick={() => ondecision(item, 'cancel')}>Cancel transfer</button
          >
        {/if}
        {#if item.warning === 'relay-approval'}<p role="status">
            This large transfer may use relay bandwidth.
          </p>
          <button
            type="button"
            class="secondary-button compact"
            disabled={busy || !wireAllowed}
            onclick={() => ondecision(item, 'allow-relay')}
            >Allow relay bandwidth</button
          >{/if}
      </article>
    {/each}
  </div>
  {#if hasMore}<button
      type="button"
      class="text-button"
      disabled={busy}
      onclick={onolder}>Load older attachments</button
    >{/if}
</section>

<style>
  .attachments {
    padding: 0.8rem 1.2rem;
    border-top: 1px solid var(--border);
    font-size: 0.85rem;
    max-height: 330px;
    overflow: auto;
    flex-shrink: 0;
  }
  .attachment-actions {
    display: flex;
    align-items: center;
    gap: 0.7rem;
    flex-wrap: wrap;
  }
  .hint {
    opacity: 0.7;
    font-size: 0.8rem;
    margin: 0.5rem 0;
  }
  .image-permission {
    display: block;
    margin: 0.5rem 0;
  }
  .attachment-list {
    max-height: 250px;
    overflow: auto;
  }
  article {
    padding: 0.6rem 0;
    border-top: 1px solid var(--border);
  }
  article p {
    overflow-wrap: anywhere;
    unicode-bidi: isolate;
  }
  img {
    max-width: 160px;
    max-height: 140px;
    object-fit: contain;
    display: block;
    margin: 0.4rem 0;
  }
  video {
    max-width: 100%;
    max-height: 300px;
    display: block;
    margin: 0.5rem 0;
  }
  progress {
    max-width: 180px;
    margin-right: 0.5rem;
  }
  label {
    line-height: 1.5;
  }
  input[type='checkbox'] {
    margin-right: 0.35rem;
  }
</style>
