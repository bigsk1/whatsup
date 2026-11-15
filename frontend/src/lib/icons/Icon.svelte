<script lang="ts">
  import { look } from '../themes/look.svelte';
  import { pixelPaths } from './pixel-paths';
  import { phosphorIcons, type IconName } from './phosphor';

  let {
    name,
    size = '1.25em',
    label = '',
  }: { name: IconName; size?: number | string; label?: string } = $props();

  const dimension = $derived(typeof size === 'number' ? `${size}px` : size);
  const pixel = $derived(look.skin === 'arcade' || look.skin === 'terminal');
  const solid: ReadonlySet<IconName> = new Set([
    'chevronDown',
    'chevronRight',
    'arrowLeft',
    'close',
    'plus',
  ]);
  const weight = $derived(
    look.skin === 'neon' ? 'light' : solid.has(name) ? 'bold' : 'duotone',
  );
  const Glyph = $derived(phosphorIcons[name]);
</script>

{#if pixel}
  <svg
    class="icon pixel-icon"
    viewBox="0 0 24 24"
    width={dimension}
    height={dimension}
    fill="currentColor"
    shape-rendering="crispEdges"
    aria-hidden={label ? undefined : 'true'}
    role={label ? 'img' : undefined}
    aria-label={label || undefined}
    focusable="false"><path d={pixelPaths[name]} /></svg
  >
{:else}
  <Glyph
    class="icon"
    size={dimension}
    {weight}
    aria-hidden={label ? undefined : 'true'}
    role={label ? 'img' : 'presentation'}
    aria-label={label || undefined}
    focusable="false"
  />
{/if}

<style>
  :global(.icon) {
    display: inline-block;
    flex-shrink: 0;
    vertical-align: middle;
  }
</style>
