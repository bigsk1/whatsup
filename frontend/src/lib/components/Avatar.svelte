<script lang="ts">
  import { avatarTone, initial, type Tone } from '../presence';
  import { mediaURL } from '../state/attachments';

  let {
    name,
    seed,
    size = '',
    presence,
    photoURL = '',
  }: {
    name: string;
    seed: string;
    size?: '' | 'small' | 'large';
    presence?: Tone;
    photoURL?: string;
  } = $props();
  let failed = $state('');
  const source = $derived(
    mediaURL(photoURL) && photoURL !== failed ? photoURL : '',
  );
</script>

<span
  class="avatar avatar-tone-{avatarTone(seed || name)} {size}"
  aria-hidden="true"
  >{#if source}<img
      class="avatar-photo"
      src={source}
      alt=""
      draggable="false"
      onerror={(event) => {
        const value = event.currentTarget.getAttribute('src');
        if (value === photoURL) failed = value;
      }}
    />{:else}<span class="avatar-letter">{initial(name)}</span
    >{/if}{#if presence}<span class="presence tone-{presence}"
    ></span>{/if}</span
>

<style>
  .avatar-photo {
    width: 100%;
    height: 100%;
    object-fit: cover;
    border-radius: inherit;
  }
</style>
