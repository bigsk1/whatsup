<script lang="ts">
  import type { AttachmentItem } from '../state/attachments';
  import Icon from '../icons/Icon.svelte';
  let {
    item,
    busy,
    video,
    saved,
    onpreview,
    onsave,
    onreveal,
    ondelete,
  }: {
    item: AttachmentItem;
    busy: boolean;
    video: string;
    saved: boolean;
    onpreview: (item: AttachmentItem) => void;
    onsave: (item: AttachmentItem) => void;
    onreveal: (item: AttachmentItem) => void;
    ondelete: (item: AttachmentItem) => void;
  } = $props();
  const isVideo = $derived(
    item.mediaType === 'video/mp4' || item.mediaType === 'video/webm',
  );
</script>

<article
  class="message-bubble inline-attachment"
  class:outgoing={item.direction === 'outgoing'}
  aria-label={item.direction === 'outgoing'
    ? 'Sent attachment'
    : 'Received attachment'}
>
  <p class="media-name" dir="auto"><strong>{item.name}</strong></p>
  {#if isVideo}
    {#if video}<video src={video} controls playsinline preload="none"
        ><track kind="captions" /></video
      >
    {:else}<button
        class="secondary-button compact"
        disabled={busy}
        onclick={() => onpreview(item)}><Icon name="play" />Play video</button
      >{/if}
    <p class="hint">Linux playback depends on installed GStreamer codecs.</p>
  {:else}
    {#if item.previewURL}<button
        class="inline-image"
        aria-label="View full image"
        disabled={busy}
        onclick={() => onpreview(item)}
        ><img src={item.previewURL} alt={item.name} loading="lazy" /></button
      >{/if}
    <button class="text-button" disabled={busy} onclick={() => onpreview(item)}
      >View image</button
    >
  {/if}
  <div class="media-actions">
    <button class="text-button" disabled={busy} onclick={() => onsave(item)}
      >Save…</button
    >
    {#if saved}<button
        class="text-button"
        disabled={busy}
        onclick={() => onreveal(item)}>Show in folder</button
      >{/if}
    <button class="text-button" disabled={busy} onclick={() => ondelete(item)}
      >Delete attachment…</button
    >
  </div>
  <footer>
    {#if item.recordedAt}<time datetime={item.recordedAt}
        >{new Date(item.recordedAt).toLocaleTimeString([], {
          hour: '2-digit',
          minute: '2-digit',
        })}</time
      >
    {:else}<span>Saved attachment</span>{/if}
    <span>{item.direction === 'outgoing' ? 'Sent' : 'Received'}</span>
  </footer>
</article>

<style>
  .inline-attachment {
    min-width: 0;
    max-width: min(100%, 32rem);
  }
  .media-name {
    overflow-wrap: anywhere;
    margin-bottom: 0.5rem;
  }
  .inline-image {
    display: block;
    max-width: 100%;
    padding: 0;
    border: 0;
    background: transparent;
  }
  img,
  video {
    display: block;
    width: auto;
    max-width: 100%;
    max-height: 22rem;
    object-fit: contain;
    border-radius: var(--radius-sm);
  }
  .media-actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.75rem;
    margin-top: 0.6rem;
  }
</style>
