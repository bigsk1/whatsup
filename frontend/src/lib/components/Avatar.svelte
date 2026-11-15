<script lang="ts">
  import { avatarTone, initial, type Tone } from '../presence';

  let {
    name,
    seed,
    size = '',
    presence,
  }: {
    name: string;
    seed: string;
    size?: '' | 'small' | 'large';
    presence?: Tone;
  } = $props();
</script>

<span
  class="avatar avatar-tone-{avatarTone(seed || name)} {size}"
  aria-hidden="true"
  ><span class="avatar-letter">{initial(name)}</span>{#if presence}<span
      class="presence tone-{presence}"
    ></span>{/if}</span
>
