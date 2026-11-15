<script lang="ts">
  import Icon from '../icons/Icon.svelte';
  import type { IconName } from '../icons/phosphor';
  import type { AttachmentDraft, AttachmentItem } from '../state/attachments';
  import { inlineMediaItem } from '../state/timeline';
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
    ondelete,
    wireAllowed,
    stageAllowed,
    wireNotice,
    inlineMedia = false,
    dragActive = false,
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
    ondelete: (item: AttachmentItem) => void;
    wireAllowed: boolean;
    stageAllowed: boolean;
    wireNotice: string;
    inlineMedia?: boolean;
    dragActive?: boolean;
  } = $props();
  let original = $state(false);
  let relay = $state<Record<string, boolean>>({});
  const contentID = $props.id();
  let expanded = $state(false);
  $effect(() => {
    if (dragActive) expanded = true;
  });
  let previousTasks = new Set<string>();
  $effect(() => {
    const tasks = [
      ...drafts.map((draft) => `draft:${draft.handle}`),
      ...items
        .filter(
          (item) => item.direction === 'incoming' && item.state === 'offered',
        )
        .map((item) => `offer:${item.id}`),
    ];
    if (tasks.some((task) => !previousTasks.has(task))) expanded = true;
    previousTasks = new Set(tasks);
  });
  const size = (bytes: number) =>
    bytes >= 2 ** 30
      ? `${(bytes / 2 ** 30).toFixed(2)} GiB`
      : bytes >= 2 ** 20
        ? `${(bytes / 2 ** 20).toFixed(1)} MiB`
        : `${Math.ceil(bytes / 1024)} KiB`;
  const viewableImage = (type: string) =>
    ['image/png', 'image/jpeg', 'image/gif', 'image/webp'].includes(type);
  const playableVideo = (type: string) =>
    type === 'video/mp4' || type === 'video/webm';
  const kindIcon = (type: string): IconName =>
    type.startsWith('image/')
      ? 'image'
      : type.startsWith('video/')
        ? 'play'
        : 'file';
  const pending = $derived(
    drafts.length +
      items.filter(
        (item) => item.direction === 'incoming' && item.state === 'offered',
      ).length,
  );
  const stateTone = (state: string) =>
    state === 'complete'
      ? 'tone-ok'
      : state === 'declined' || state === 'failed'
        ? 'tone-danger'
        : state === 'offered' || state === 'paused'
          ? 'tone-warn'
          : 'tone-idle';
</script>

<section
  class="attachments"
  class:drag-active={dragActive}
  data-drop-active={dragActive}
  aria-label="Attachments"
>
  <button
    type="button"
    class="attachment-toggle"
    aria-label={expanded ? 'Collapse attachments' : 'Expand attachments'}
    aria-expanded={expanded}
    aria-controls={contentID}
    aria-describedby={`${contentID}-summary`}
    onclick={() => (expanded = !expanded)}
  >
    <span class="toggle-icon"><Icon name="folder" /></span>
    <strong>Photos, videos & files</strong>
    {#if pending > 0}<span class="pending-badge">{pending} waiting</span>{/if}
    <span class="attachment-count" id={`${contentID}-summary`}
      >{items.length + drafts.length}
      {items.length + drafts.length === 1 ? 'file' : 'files'}</span
    >
    <span class="chevron"><Icon name="chevronDown" size="1.1em" /></span>
  </button>
  {#if notice}<p class="panel-alert" role="alert">{notice}</p>{/if}
  <div class="attachment-content" id={contentID} hidden={!expanded}>
    {#if expanded}
      <div class="attachment-actions">
        <button
          type="button"
          class="secondary-button compact"
          disabled={busy || !stageAllowed}
          onclick={() => onpick(original)}
          ><Icon name="attach" size="1.1em" />Choose file…</button
        >
        <button
          type="button"
          class="secondary-button compact"
          disabled={busy || !stageAllowed}
          onclick={onpaste}
          ><Icon name="image" size="1.1em" />Paste image</button
        >
        <label class="check-row"
          ><input
            type="checkbox"
            bind:checked={original}
            disabled={busy || !stageAllowed}
          /> Send as file (keep original bytes)</label
        >
      </div>
      <div
        class="drop-hint"
        class:drag-active={dragActive}
        role={dragActive ? 'status' : undefined}
      >
        <Icon name="dragDrop" size="1.6em" />
        <p>
          <strong
            >{dragActive
              ? 'Drop a file to prepare an attachment.'
              : 'Drop a file anywhere in this window.'}</strong
          >
          Images are re-encoded to remove metadata by default. Files stay local until
          you choose Send. One file per drop, up to 2 GiB.
        </p>
      </div>
      <label class="image-permission check-row"
        ><input
          type="checkbox"
          checked={autoAcceptImages}
          disabled={busy}
          onchange={(event) => onautoaccept(event.currentTarget.checked)}
        /> Automatically accept images up to 25 MB from this contact</label
      >
      {#if wireNotice}<p class="panel-status" role="status">
          {wireNotice}
        </p>{/if}
      {#each drafts as draft (draft.handle)}
        <article class="attachment-draft" aria-label="Attachment preview">
          <div class="media">
            {#if draft.previewURL}<img
                src={draft.previewURL}
                alt="Ready to send"
                loading="lazy"
              />{:else}<span class="kind"
                ><Icon name={kindIcon(draft.mediaType)} size="1.6em" /></span
              >{/if}
          </div>
          <div class="details">
            <p dir="auto"><strong>{draft.name}</strong> · {size(draft.size)}</p>
            <span class="state-badge tone-warn">Ready to send</span>
            {#if draft.requiresRelayWarning}
              <label class="check-row"
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
            <div class="card-actions">
              <button
                type="button"
                class="primary-button compact"
                disabled={busy ||
                  !wireAllowed ||
                  (draft.requiresRelayWarning && !relay[draft.handle])}
                onclick={() => onsend(draft, Boolean(relay[draft.handle]))}
                ><Icon name="send" size="1.1em" />Send file</button
              >
              <button
                type="button"
                class="text-button"
                disabled={busy}
                onclick={() => ondiscard(draft)}>Discard</button
              >
            </div>
          </div>
        </article>
      {/each}
      <div class="attachment-list">
        {#if inlineMedia && items.some(inlineMediaItem)}<p class="hint">
            Completed photos and videos appear in the conversation.
          </p>{/if}
        {#each items as item (item.id)}
          {#if !inlineMedia || !inlineMediaItem(item)}
            <article
              class:incoming={item.direction === 'incoming'}
              aria-label={item.direction === 'incoming'
                ? 'Received attachment'
                : 'Sent attachment'}
            >
              <div class="media">
                {#if item.previewURL}<img
                    src={item.previewURL}
                    alt={item.name}
                    loading="lazy"
                  />{:else}<span class="kind"
                    ><Icon name={kindIcon(item.mediaType)} size="1.6em" /></span
                  >{/if}
              </div>
              <div class="details">
                <p dir="auto">
                  <strong>{item.name}</strong> · {size(item.size)}<span
                    class="state-badge {stateTone(item.state)}"
                    ><Icon
                      name={item.direction === 'incoming' ? 'download' : 'send'}
                      size="1em"
                    />
                    · {item.state}</span
                  >
                </p>
                {#if item.state === 'complete' && playableVideo(item.mediaType) && videos[item.id]}<video
                    src={videos[item.id]}
                    controls
                    playsinline
                    preload="none"
                    ><track kind="captions" />Your system cannot play this
                    video. Save it to view with another app.</video
                  >{/if}
                {#if item.state !== 'complete' && item.state !== 'declined' && item.state !== 'failed' && !(item.state === 'offered' && item.direction === 'incoming') && item.state !== 'paused'}
                  <div class="progress-row">
                    <progress
                      max={item.size || 1}
                      value={item.transferredBytes}
                      aria-label="File transfer progress"
                    ></progress>
                    <span
                      >{size(item.transferredBytes)} / {size(item.size)}</span
                    >
                  </div>
                {/if}
                <div class="card-actions">
                  {#if item.state === 'offered' && item.direction === 'incoming'}
                    <button
                      type="button"
                      class="primary-button compact"
                      disabled={busy || !wireAllowed}
                      onclick={() => ondecision(item, 'accept')}
                      ><Icon name="check" size="1.1em" />Accept</button
                    >
                    <button
                      type="button"
                      class="text-button"
                      disabled={busy}
                      onclick={() => ondecision(item, 'decline')}
                      >Decline</button
                    >
                  {:else if item.state === 'paused'}
                    <button
                      type="button"
                      class="secondary-button compact"
                      disabled={busy || !wireAllowed}
                      onclick={() => ondecision(item, 'resume')}
                      ><Icon name="play" size="1.1em" />Resume</button
                    >
                  {:else if item.state === 'complete'}
                    {#if viewableImage(item.mediaType)}
                      <button
                        type="button"
                        class="secondary-button compact"
                        disabled={busy}
                        onclick={() => onpreview(item)}
                        ><Icon name="eye" size="1.1em" />View image</button
                      >
                    {/if}
                    {#if playableVideo(item.mediaType) && !videos[item.id]}<button
                        type="button"
                        class="secondary-button compact"
                        disabled={busy}
                        onclick={() => onpreview(item)}
                        ><Icon name="play" size="1.1em" />Play video</button
                      >{/if}
                    <button
                      type="button"
                      class="secondary-button compact"
                      disabled={busy}
                      onclick={() => onsave(item)}
                      ><Icon name="download" size="1.1em" />Save…</button
                    >
                    {#if saved[item.id]}<button
                        type="button"
                        class="text-button"
                        disabled={busy}
                        onclick={() => onreveal(item)}>Show in folder</button
                      >{/if}
                  {:else if item.state !== 'declined' && item.state !== 'failed'}
                    <button
                      type="button"
                      class="text-button"
                      disabled={busy}
                      onclick={() => ondecision(item, 'cancel')}
                      >Pause transfer</button
                    >
                  {/if}
                  {#if item.state !== 'complete' && item.state !== 'declined' && item.state !== 'failed' && !(item.state === 'offered' && item.direction === 'incoming')}
                    <button
                      type="button"
                      class="text-button"
                      disabled={busy}
                      onclick={() => ondecision(item, 'withdraw')}
                      >Cancel transfer</button
                    >
                  {/if}
                  <button
                    type="button"
                    class="text-button"
                    disabled={busy}
                    onclick={() => ondelete(item)}>Delete attachment…</button
                  >
                </div>
                {#if item.state === 'complete' && playableVideo(item.mediaType)}
                  <p class="hint">
                    Linux playback depends on installed GStreamer codecs.
                  </p>
                {/if}
                {#if item.warning === 'cancellation-pending'}<p
                    class="hint"
                    role="status"
                  >
                    Cancelled on this device. Waiting for the other device to
                    confirm; it may need an update.
                  </p>{:else if item.warning === 'peer-completed'}<p
                    class="hint"
                    role="status"
                  >
                    The other device completed this file before cancellation and
                    may keep its copy.
                  </p>{/if}
                {#if item.warning === 'relay-approval'}<p
                    class="hint"
                    role="status"
                  >
                    This large transfer may use relay bandwidth.
                  </p>
                  <button
                    type="button"
                    class="secondary-button compact"
                    disabled={busy || !wireAllowed}
                    onclick={() => ondecision(item, 'allow-relay')}
                    >Allow relay bandwidth</button
                  >{/if}
              </div>
            </article>
          {/if}
        {/each}
      </div>
      {#if hasMore}<button
          type="button"
          class="text-button"
          disabled={busy}
          onclick={onolder}>Load older attachments</button
        >{/if}
    {/if}
  </div>
</section>

<style>
  .attachments {
    display: flex;
    flex-direction: column;
    flex-shrink: 0;
    max-height: 340px;
    margin: 0 1.25rem;
    padding: 0.35rem 0.6rem;
    overflow: hidden;
    border: var(--line) solid var(--border);
    border-radius: var(--radius-lg);
    background: color-mix(in srgb, var(--surface) 80%, transparent);
    font-size: 0.9rem;
  }
  .attachment-toggle {
    display: flex;
    flex-shrink: 0;
    align-items: center;
    gap: 0.55rem;
    width: 100%;
    padding: 0.25rem 0.15rem;
    text-align: left;
    border-radius: var(--radius-sm);
  }
  .attachments.drag-active {
    border-color: var(--accent);
  }
  .toggle-icon {
    display: grid;
    place-items: center;
    width: 1.9rem;
    height: 1.9rem;
    border-radius: var(--radius-sm);
    background: var(--accent-soft);
    color: var(--accent);
  }
  .attachment-toggle strong {
    font-size: 0.92rem;
  }
  .pending-badge {
    padding: 0.05rem 0.5rem;
    border-radius: var(--radius-pill);
    background: var(--warn-soft);
    color: var(--warn);
    font-size: 0.75rem;
    font-weight: var(--strong-weight);
  }
  .attachment-count {
    margin-left: auto;
    color: var(--muted);
    font-size: 0.8rem;
  }
  .chevron {
    display: grid;
    flex: none;
    color: var(--muted);
    transition: transform 160ms;
  }
  .attachment-toggle[aria-expanded='true'] .chevron {
    transform: rotate(180deg);
  }
  .attachment-content {
    min-height: 0;
    margin-top: 0.5rem;
    padding: 0 0.15rem 0.35rem;
    overflow: auto;
  }
  .attachment-actions,
  .card-actions {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }
  .check-row {
    display: inline-flex;
    align-items: flex-start;
    gap: 0.45rem;
    line-height: 1.4;
    color: var(--muted);
  }
  .check-row input {
    margin: 0.2rem 0 0;
    accent-color: var(--accent);
  }
  .drop-hint {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    margin: 0.6rem 0;
    padding: 0.6rem 0.8rem;
    border: 2px dashed var(--accent-line);
    border-radius: var(--radius);
    color: var(--accent);
  }
  .drop-hint p {
    font-size: 0.82rem;
    line-height: 1.45;
    color: var(--muted);
  }
  .drop-hint.drag-active {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .drop-hint strong {
    display: block;
    color: var(--text);
  }
  .image-permission {
    display: flex;
    margin: 0.25rem 0 0.5rem;
  }
  .panel-alert,
  .panel-status {
    margin: 0.35rem 0;
    padding: 0.45rem 0.65rem;
    border-radius: var(--radius-sm);
    background: var(--warn-soft);
    font-size: 0.85rem;
  }
  .panel-status {
    background: var(--accent-soft);
  }
  .attachment-list {
    display: grid;
    gap: 0.5rem;
  }
  article {
    display: flex;
    gap: 0.75rem;
    padding: 0.55rem;
    border-radius: var(--radius);
    background: var(--panel);
    border: var(--line) solid var(--border);
  }
  .attachment-draft {
    margin-bottom: 0.5rem;
    border-color: var(--accent-line);
  }
  .media {
    flex-shrink: 0;
  }
  .media img {
    display: block;
    width: 4.5rem;
    height: 4.5rem;
    object-fit: cover;
    border-radius: var(--radius-sm);
    background: var(--bg);
  }
  .kind {
    display: grid;
    place-items: center;
    width: 4.5rem;
    height: 4.5rem;
    border-radius: var(--radius-sm);
    background: var(--accent-soft);
    color: var(--accent);
  }
  .incoming .kind {
    background: color-mix(in srgb, var(--accent-2) 16%, transparent);
    color: var(--accent-2);
  }
  .details {
    display: grid;
    align-content: start;
    gap: 0.4rem;
    min-width: 0;
    flex: 1;
  }
  .details p {
    overflow-wrap: anywhere;
    unicode-bidi: isolate;
  }
  .state-badge {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    margin-left: 0.4rem;
    padding: 0 0.45rem;
    border-radius: var(--radius-pill);
    background: var(--tone-soft);
    color: var(--tone);
    font-size: 0.75rem;
    font-weight: var(--strong-weight);
    vertical-align: middle;
  }
  .attachment-draft .state-badge {
    justify-self: start;
    margin-left: 0;
  }
  .progress-row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.8rem;
    color: var(--muted);
  }
  progress {
    flex: 1;
    max-width: 14rem;
    height: 0.5rem;
    accent-color: var(--accent);
  }
  video {
    display: block;
    width: 100%;
    max-height: 260px;
    border-radius: var(--radius-sm);
    background: #000;
  }
  .hint {
    font-size: 0.8rem;
    color: var(--faint);
  }
  @media (max-width: 720px) {
    .attachments {
      margin: 0 0.75rem;
    }
    .drop-hint {
      display: none;
    }
  }
  @media (max-height: 620px) {
    .attachments {
      max-height: min(340px, 42vh);
    }
    .drop-hint {
      display: none;
    }
  }
</style>
