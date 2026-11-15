<script lang="ts">
  import type { Skin } from '../themes/catalog';
  import { look } from '../themes/look.svelte';
  import { pixelGrid, pixelRuns } from './pixel-art';

  let {
    size = '2.5rem',
    skin,
    label = '',
  }: { size?: string; skin?: Skin; label?: string } = $props();

  const uid = $props.id();
  const active = $derived(skin ?? look.skin);
  const bubble =
    'M362 262H662A150 150 0 0 1 812 412V542A150 150 0 0 1 662 692H470L262 826L287 672A150 150 0 0 1 212 542V412A150 150 0 0 1 362 262Z';
  const boxyBubble = 'M212 262H812V692H470L262 826L300 692H212Z';
  const spark =
    'M818 130Q832 212 914 226Q832 240 818 322Q804 240 722 226Q804 212 818 130ZM912 318Q917 345 944 350Q917 355 912 382Q907 355 880 350Q907 345 912 318Z';
</script>

<svg
  class="logo logo-{active}"
  viewBox={active === 'arcade'
    ? `0 0 ${pixelGrid} ${pixelGrid}`
    : '0 0 1024 1024'}
  width={size}
  height={size}
  role={label ? 'img' : undefined}
  aria-label={label || undefined}
  aria-hidden={label ? undefined : 'true'}
  focusable="false"
>
  {#if active === 'arcade'}
    <g shape-rendering="crispEdges">
      {#each pixelRuns as run (`${run.x}-${run.y}`)}
        <rect
          class="px-{run.tone}"
          x={run.x}
          y={run.y}
          width={run.width}
          height="1"
        />
      {/each}
    </g>
  {:else if active === 'neon'}
    <rect class="neon-frame" x="40" y="40" width="944" height="944" rx="96" />
    <path class="neon-bubble" d={bubble} />
    <g class="eyes">
      <ellipse class="neon-eye" cx="412" cy="450" rx="40" ry="54" />
      <ellipse class="neon-eye" cx="612" cy="450" rx="40" ry="54" />
    </g>
    <path class="neon-smile" d="M404 560Q512 650 620 560" />
    <path class="neon-spark" d={spark} />
  {:else if active === 'terminal'}
    <rect class="term-frame" x="36" y="36" width="952" height="952" rx="40" />
    <path class="term-bubble" d={boxyBubble} />
    <g class="eyes">
      <rect class="term-eye" x="372" y="392" width="80" height="112" />
      <rect class="term-eye" x="572" y="392" width="80" height="112" />
    </g>
    <path class="term-smile" d="M404 566L452 610H572L620 566" />
    <rect class="term-cursor" x="742" y="140" width="150" height="44" />
  {:else}
    <defs>
      <linearGradient id="{uid}-tile" x1="0" y1="0" x2="1" y2="1">
        <stop class="stop-a" offset="0" />
        <stop class="stop-b" offset="1" />
      </linearGradient>
    </defs>
    <rect fill="url(#{uid}-tile)" width="1024" height="1024" rx="232" />
    <path class="bubble-shadow" d={bubble} transform="translate(0 24)" />
    <path class="bubble-body" d={bubble} />
    <g class="eyes">
      <ellipse class="face" cx="412" cy="450" rx="44" ry="58" />
      <ellipse class="face" cx="612" cy="450" rx="44" ry="58" />
      <circle class="shine" cx="428" cy="428" r="15" />
      <circle class="shine" cx="628" cy="428" r="15" />
    </g>
    <circle class="cheek" cx="336" cy="560" r="34" />
    <circle class="cheek" cx="688" cy="560" r="34" />
    <path class="smile" d="M404 560Q512 650 620 560" />
    <path class="spark" d={spark} />
  {/if}
</svg>

<style>
  .logo {
    display: block;
    flex-shrink: 0;
    overflow: visible;
  }
  .stop-a {
    stop-color: var(--accent);
  }
  .stop-b {
    stop-color: var(--accent-2);
  }
  .bubble-shadow {
    fill: #000;
    opacity: 0.18;
  }
  .bubble-body,
  .shine {
    fill: #fff;
  }
  .face {
    fill: #1b1340;
  }
  .cheek {
    fill: #ff8fb1;
    opacity: 0.75;
  }
  .smile {
    fill: none;
    stroke: #1b1340;
    stroke-width: 36;
    stroke-linecap: round;
  }
  .spark,
  .neon-spark,
  .px-spark {
    fill: var(--warn);
  }

  .px-tile,
  .px-eye,
  .px-mouth {
    fill: var(--accent);
  }
  .px-bubble {
    fill: var(--on-accent);
  }

  .neon-frame {
    fill: var(--panel);
    stroke: var(--accent-2);
    stroke-width: 36;
  }
  .neon-bubble,
  .neon-smile {
    fill: none;
    stroke: var(--accent);
    stroke-width: 40;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .neon-eye {
    fill: var(--accent);
  }
  :global([data-effects='on']) .logo-neon {
    filter: drop-shadow(0 0 0.12em var(--accent))
      drop-shadow(0 0 0.04em var(--accent-2));
  }

  .term-frame {
    fill: var(--panel);
    stroke: var(--accent);
    stroke-width: 32;
  }
  .term-bubble,
  .term-smile {
    fill: none;
    stroke: var(--accent);
    stroke-width: 40;
    stroke-linejoin: miter;
  }
  .term-eye,
  .term-cursor {
    fill: var(--accent);
  }

  .eyes {
    transform-box: fill-box;
    transform-origin: center;
  }
  @media (prefers-reduced-motion: no-preference) {
    :global([data-effects='on']) .eyes {
      animation: blink 7s infinite;
    }
    :global([data-effects='on']) .term-cursor {
      animation: cursor 1.1s steps(1) infinite;
    }
  }
  @keyframes blink {
    0%,
    94%,
    100% {
      transform: scaleY(1);
    }
    96% {
      transform: scaleY(0.1);
    }
  }
  @keyframes cursor {
    50% {
      opacity: 0;
    }
  }
</style>
