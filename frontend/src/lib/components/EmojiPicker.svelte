<script lang="ts">
  import Icon from '../icons/Icon.svelte';

  let {
    disabled,
    onpick,
  }: { disabled: boolean; onpick: (emoji: string) => void } = $props();

  const groups = [
    {
      name: 'Smileys',
      emoji:
        '😀 😃 😄 😁 😆 😅 🤣 😂 🙂 😉 😊 😇 🥰 😍 🤩 😘 😋 😛 😜 🤪 😎 🤓 🥳 😏 😌 😴 🤔 🤨 😐 🙄 😬 😮 😲 🥺 😢 😭 😤 😡 🤯 😱 🤗 🤭 🫡 🫠',
    },
    {
      name: 'Hearts',
      emoji:
        '❤️ 🧡 💛 💚 💙 💜 🖤 🤍 🤎 💕 💞 💓 💗 💖 💘 💝 💯 💥 ✨ 🔥 ⭐ 🌟 💫 🎉',
    },
    {
      name: 'Hands',
      emoji: '👍 👎 👌 ✌️ 🤞 🤟 🤘 🤙 👋 👏 🙌 🫶 🙏 💪 👀 🤝 ✋ 👊 ☝️ 🫵',
    },
    {
      name: 'Fun',
      emoji:
        '🎮 🕹️ 👾 🎲 🎯 🏆 🎵 🎶 🎸 🎧 📸 🎬 📚 💻 🚀 🛸 🤖 👻 💀 🎃 🎂 🎁 🎈 🪩',
    },
    {
      name: 'Food',
      emoji: '☕ 🍵 🍺 🍷 🍕 🍔 🍟 🌮 🍜 🍣 🍩 🍪 🍰 🍫 🍿 🍎 🍓 🍉 🥑 🌶️',
    },
    {
      name: 'Nature',
      emoji: '🐶 🐱 🦊 🐻 🐼 🐨 🐸 🐵 🦄 🐝 🦋 🐢 🐙 🌸 🌻 🌈 ☀️ 🌙 ⛄ 🌊',
    },
  ].map((group) => ({ ...group, emoji: group.emoji.split(' ') }));

  let open = $state(false);
  let active = $state(0);
  let root = $state<HTMLDivElement>();
  let trigger = $state<HTMLButtonElement>();

  function close(restore = true) {
    if (!open) return;
    open = false;
    if (restore) trigger?.focus();
  }

  $effect(() => {
    if (disabled) open = false;
  });
</script>

<svelte:window
  onpointerdown={(event) => {
    if (open && root && !root.contains(event.target as Node)) close(false);
  }}
/>

<div class="emoji-root" bind:this={root}>
  <button
    bind:this={trigger}
    type="button"
    class="icon-button emoji-button"
    aria-label="Choose emoji"
    aria-expanded={open}
    aria-haspopup="dialog"
    {disabled}
    onclick={() => (open = !open)}><Icon name="smile" /></button
  >
  {#if open}
    <div
      class="emoji-panel"
      role="dialog"
      aria-label="Emoji"
      tabindex="-1"
      onkeydown={(event) => {
        if (event.key === 'Escape') {
          event.preventDefault();
          event.stopPropagation();
          close();
        }
      }}
    >
      <div class="emoji-tabs" role="tablist" aria-label="Emoji groups">
        {#each groups as group, index (group.name)}
          <button
            type="button"
            role="tab"
            aria-selected={active === index}
            class:active={active === index}
            onclick={() => (active = index)}
            ><span aria-hidden="true">{group.emoji[0]}</span><span
              class="sr-only">{group.name}</span
            ></button
          >
        {/each}
      </div>
      <div class="emoji-grid" role="tabpanel" aria-label={groups[active].name}>
        {#each groups[active].emoji as emoji (emoji)}
          <button
            type="button"
            class="emoji"
            onclick={() => {
              onpick(emoji);
            }}>{emoji}</button
          >
        {/each}
      </div>
    </div>
  {/if}
</div>

<style>
  .emoji-root {
    position: relative;
    display: flex;
  }
  .emoji-panel {
    position: absolute;
    right: -3rem;
    bottom: calc(100% + 0.75rem);
    z-index: 30;
    width: min(21rem, calc(100vw - 2rem));
    padding: 0.5rem;
    border-radius: var(--radius-lg);
    border: var(--line) solid var(--border);
    background: var(--panel);
    box-shadow: var(--shadow-2);
  }
  .emoji-tabs {
    display: flex;
    gap: 0.2rem;
    padding-bottom: 0.4rem;
    margin-bottom: 0.4rem;
    border-bottom: var(--line) solid var(--border);
  }
  .emoji-tabs button {
    flex: 1;
    padding: 0.3rem 0;
    border-radius: var(--radius-sm);
    font-size: 1.1rem;
    filter: grayscale(0.7);
    opacity: 0.75;
  }
  .emoji-tabs button.active,
  .emoji-tabs button:hover {
    background: var(--accent-soft);
    filter: none;
    opacity: 1;
  }
  .emoji-grid {
    display: grid;
    grid-template-columns: repeat(8, 1fr);
    gap: 0.1rem;
    max-height: 12.5rem;
    overflow-y: auto;
  }
  .emoji {
    aspect-ratio: 1;
    border-radius: var(--radius-sm);
    font-family:
      'Segoe UI Emoji', 'Noto Color Emoji', 'Apple Color Emoji', sans-serif;
    font-size: 1.35rem;
    line-height: 1;
    text-shadow: none;
  }
  .emoji:hover,
  .emoji:focus-visible {
    background: var(--hover);
    transform: scale(1.15);
  }
</style>
