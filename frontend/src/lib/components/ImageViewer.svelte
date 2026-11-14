<script lang="ts">
  let {
    image,
    onclose,
  }: {
    image: { name: string; url: string } | null;
    onclose: () => void;
  } = $props();
  let dialog: HTMLDialogElement;
  let actualSize = $state(false);
  $effect(() => {
    if (!dialog) return;
    if (image) {
      actualSize = false;
      if (!dialog.open) dialog.showModal();
    } else if (dialog.open) dialog.close();
  });
</script>

<dialog
  bind:this={dialog}
  aria-label="Image preview"
  oncancel={onclose}
  {onclose}
>
  {#if image}
    <div class="viewer-controls">
      <strong dir="auto">{image.name}</strong>
      <button
        type="button"
        class="secondary-button compact"
        aria-pressed={actualSize}
        onclick={() => (actualSize = !actualSize)}
        >{actualSize ? 'Fit image' : 'Actual size'}</button
      >
      <button type="button" class="secondary-button compact" onclick={onclose}
        >Close image</button
      >
    </div>
    <div class="image-scroll">
      <img src={image.url} alt={image.name} class:actual={actualSize} />
    </div>
  {/if}
</dialog>

<style>
  dialog {
    width: min(94vw, 1400px);
    max-height: 92vh;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 12px;
  }
  dialog::backdrop {
    background: rgb(0 0 0 / 70%);
  }
  .viewer-controls {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-bottom: 12px;
  }
  strong {
    flex: 1;
    overflow-wrap: anywhere;
  }
  .image-scroll {
    overflow: auto;
    max-height: 76vh;
  }
  img {
    display: block;
    max-width: 100%;
    max-height: 76vh;
    object-fit: contain;
    margin: auto;
  }
  img.actual {
    max-width: none;
    max-height: none;
  }
</style>
