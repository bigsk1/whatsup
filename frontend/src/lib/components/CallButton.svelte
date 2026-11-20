<script lang="ts">
  import Icon from '../icons/Icon.svelte';
  import type { CallView } from '../state/calls';

  let {
    name,
    view,
    voiceCalls,
    inCall,
    busy,
    onrequest,
  }: {
    name: string;
    /** This contact's call view, once it has been read from the desktop. */
    view: CallView | undefined;
    /** The local "Allow voice calls" setting. */
    voiceCalls: boolean;
    /** A call with anyone is in progress on this device. */
    inCall: boolean;
    busy: boolean;
    /** Ask to call, or to switch voice calls on. Nothing starts here. */
    onrequest: () => void;
  } = $props();

  const blocked = $derived(
    !voiceCalls
      ? ''
      : inCall || view?.availability === 'busy'
        ? 'You are already in a call'
        : !view
          ? 'Checking whether this contact can be called'
          : view.availability === 'offline'
            ? 'Connect to this contact before calling'
            : view.availability === 'peer-update'
              ? 'This contact needs a newer WhatsUp to take calls'
              : view.availability === 'blocked'
                ? 'Voice calls are unavailable until WhatsUp restarts: microphone access could not be secured'
                : view.availability === 'calls-off'
                  ? 'Checking voice call settings'
                  : '',
  );
  const label = $derived(
    !voiceCalls ? 'Voice call (switched off)' : blocked || `Call ${name}`,
  );
</script>

<button
  type="button"
  class="icon-button"
  aria-label={label}
  title={label}
  disabled={busy || blocked !== ''}
  onclick={onrequest}><Icon name="phone" /></button
>
