<script lang="ts">
  import Icon from '../icons/Icon.svelte';
  import { showCamera } from '../call/video';
  import Avatar from './Avatar.svelte';
  import type { CallStats, CallView, CameraState } from '../state/calls';

  let {
    call,
    name,
    seed = '',
    photoURL = '',
    path,
    notice,
    busy,
    camera = 'off',
    cameraHeld = false,
    remotePicture = false,
    onanswer,
    onend,
    onmute,
    oncamera,
    oncanvas,
    localStream,
    ondismiss,
    readStats,
  }: {
    /** What the initials avatar's colour is derived from: their fingerprint. */
    seed?: string;
    /** Their profile picture, if they share one and photos are shown. */
    photoURL?: string;
    /** This window's camera: off, starting or on. */
    camera?: CameraState;
    /** The call is relayed and video waits for the person's choice. */
    cameraHeld?: boolean;
    /** The other person's first picture has been painted. */
    remotePicture?: boolean;
    /** Switch the camera; overRelay is the explicit choice to use a relay. */
    oncamera?: (call: CallView, overRelay: boolean) => void;
    /** Hand over the canvas the other person's picture is painted on. */
    oncanvas?: (canvas: HTMLCanvasElement | null) => void;
    /** This device's live camera stream for the small preview. */
    localStream?: () => MediaStream | null;
    /** The one call in progress on this device, or null. */
    call: CallView | null;
    /** Display name of the person on that call. */
    name: string;
    /** Direct, Relayed or Unknown for that contact's connection. */
    path: string;
    notice: string;
    busy: boolean;
    onanswer: (call: CallView, video: boolean) => void;
    onend: (call: CallView) => void;
    onmute: (call: CallView) => void;
    ondismiss: () => void;
    /** Audio counters for the answered call; numbers only. */
    readStats?: () => CallStats | null;
  } = $props();

  let dialog: HTMLDialogElement;
  let declineButton: HTMLButtonElement;
  let now = $state(Date.now());
  let showDetails = $state(false);
  let stats = $state<CallStats | null>(null);
  let remoteCanvas = $state<HTMLCanvasElement | null>(null);
  let localPreview = $state<HTMLVideoElement | null>(null);

  const incoming = $derived(call?.state === 'incoming');
  const answered = $derived(
    call?.state === 'connecting' || call?.state === 'active',
  );

  $effect(() => {
    if (!dialog) return;
    if (incoming && !dialog.open) {
      dialog.showModal();
      // Decline has the focus, so Enter or Space cannot answer by accident.
      declineButton.focus();
    } else if (!incoming && dialog.open) dialog.close();
  });

  $effect(() => {
    if (call?.state !== 'active') return;
    now = Date.now();
    const timer = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(timer);
  });

  $effect(() => {
    // Read the counters once a second, only while the readout is open.
    if (!showDetails || !answered || !readStats) {
      stats = null;
      return;
    }
    stats = readStats();
    const timer = setInterval(() => (stats = readStats()), 1000);
    return () => clearInterval(timer);
  });

  $effect(() => {
    if (!call) showDetails = false;
  });

  const showVideo = $derived(answered && call?.video === true);

  $effect(() => {
    // The picture area exists only during an answered video call.
    const canvas = showVideo ? remoteCanvas : null;
    oncanvas?.(canvas);
    return () => oncanvas?.(null);
  });

  $effect(() => {
    // The small preview shows this device's own camera, mirrored. It follows
    // the camera state: off means no stream is attached at all.
    //
    // It is attached once and left alone until the camera itself changes.
    // This runs again whenever the call screen refreshes, and an element
    // must never let go of a camera that is still running: one engine locks
    // the whole window up when that happens. So nothing is undone here; the
    // camera is always stopped before its state reaches 'off', and only then
    // is the picture detached.
    const preview = localPreview;
    if (!preview) return;
    const stream = camera === 'on' ? (localStream?.() ?? null) : null;
    if (preview.srcObject === stream) return;
    if (stream) void showCamera(preview, stream).catch(() => {});
    else preview.srcObject = null;
  });

  // Their live picture is shown only while their camera is on and a picture
  // has actually been painted. Until then, and whenever their camera is off,
  // the area shows who they are instead of an empty frame.
  const livePicture = $derived(call?.peerCameraOn === true && remotePicture);
  const videoStatus = $derived(
    livePicture
      ? ''
      : call?.peerCameraOn
        ? 'Starting video…'
        : 'Their camera is off',
  );

  function elapsed(since: number): string {
    const seconds = Math.max(0, Math.floor((now - since) / 1000));
    const pad = (value: number) => String(value).padStart(2, '0');
    const hours = Math.floor(seconds / 3600);
    return (
      (hours ? hours + ':' : '') +
      pad(Math.floor((seconds % 3600) / 60)) +
      ':' +
      pad(seconds % 60)
    );
  }

  const status = $derived(
    !call
      ? ''
      : call.state === 'outgoing'
        ? call.peerRinging
          ? 'Ringing…'
          : 'Calling…'
        : call.state === 'connecting'
          ? 'Connecting…'
          : call.state === 'active'
            ? elapsed(call.activeSince)
            : '',
  );
</script>

<dialog
  bind:this={dialog}
  class="settings-dialog call-dialog"
  aria-labelledby="incoming-call-title"
  aria-describedby="incoming-call-description"
  oncancel={(event) => {
    // Escape neither answers nor declines; the call keeps ringing.
    event.preventDefault();
  }}
>
  <div class="dialog-heading">
    <div class="dialog-title">
      <div class="small-icon">
        <Icon name={call?.video ? 'video' : 'phone'} />
      </div>
      <h2 id="incoming-call-title">
        {call?.video ? 'Incoming video call' : 'Incoming voice call'}
      </h2>
    </div>
  </div>
  <div class="dialog-body">
    <p id="incoming-call-description" class="caller" dir="auto">{name}</p>
    <p class="muted small">
      {call?.video
        ? 'Your microphone and camera turn on only if you accept with video. Headphones are recommended.'
        : 'Your microphone turns on only if you accept. Headphones are recommended.'}
    </p>
  </div>
  <div class="dialog-footer">
    <button
      bind:this={declineButton}
      class="secondary-button compact"
      disabled={busy || !call}
      onclick={() => call && onend(call)}
      ><Icon name="phoneEnd" size="1.1em" /><span>Decline</span></button
    >
    {#if call?.video}
      <button
        class="secondary-button compact"
        disabled={busy || !call}
        onclick={() => call && onanswer(call, false)}
        ><Icon name="phone" size="1.1em" /><span>Accept without video</span
        ></button
      >
      <button
        class="primary-button compact"
        disabled={busy || !call}
        onclick={() => call && onanswer(call, true)}
        ><Icon name="video" size="1.1em" /><span>Accept with video</span
        ></button
      >
    {:else}
      <button
        class="primary-button compact"
        disabled={busy || !call}
        onclick={() => call && onanswer(call, false)}
        ><Icon name="phone" size="1.1em" /><span>Accept</span></button
      >
    {/if}
  </div>
</dialog>

{#if call && !incoming}
  <section
    class="call-bar"
    class:with-video={showVideo}
    aria-label={call.video ? 'Video call' : 'Voice call'}
    aria-live="polite"
  >
    <span class="call-icon"><Icon name={call.video ? 'video' : 'phone'} /></span
    >
    <span class="call-who">
      <strong dir="auto">{name}</strong>
      <span class="call-status">{status}{answered ? ` · ${path}` : ''}</span>
    </span>
    {#if answered}
      <span class="mic-state" class:muted={call.micMuted}>
        <Icon name={call.micMuted ? 'micOff' : 'mic'} size="1.1em" />
        {call.micMuted ? 'Muted' : 'Microphone on'}
      </span>
      {#if call.peerMicMuted}<span class="peer-muted">They are muted</span>{/if}
      <button
        type="button"
        class="icon-button"
        aria-pressed={call.micMuted}
        aria-label={call.micMuted ? 'Unmute microphone' : 'Mute microphone'}
        title={call.micMuted ? 'Unmute microphone' : 'Mute microphone'}
        onclick={() => call && onmute(call)}
        ><Icon name={call.micMuted ? 'micOff' : 'mic'} /></button
      >
      {#if call.video}
        <button
          type="button"
          class="icon-button"
          aria-pressed={camera !== 'off'}
          disabled={camera === 'starting'}
          aria-label={camera === 'off' ? 'Turn camera on' : 'Turn camera off'}
          title={camera === 'off' ? 'Turn camera on' : 'Turn camera off'}
          onclick={() => call && oncamera?.(call, false)}
          ><Icon name={camera === 'off' ? 'videoOff' : 'video'} /></button
        >
      {/if}
      <button
        type="button"
        class="secondary-button compact"
        aria-expanded={showDetails}
        aria-controls="call-details"
        onclick={() => (showDetails = !showDetails)}>Details</button
      >
    {/if}
    <button
      type="button"
      class="danger-button compact end-call"
      aria-label={answered ? 'Hang up' : 'Cancel call'}
      onclick={() => call && onend(call)}
      ><Icon name="phoneEnd" size="1.1em" /><span
        >{answered ? 'Hang up' : 'Cancel'}</span
      ></button
    >
    {#if showVideo}
      <div class="call-video">
        <canvas
          bind:this={remoteCanvas}
          class="remote-video"
          class:waiting={!livePicture}
          aria-label="Their video"
          width="640"
          height="360"
        ></canvas>
        {#if !livePicture}
          <div class="video-placeholder">
            <Avatar {name} seed={seed || name} size="large" {photoURL} />
            <p class="video-status" role="status">{videoStatus}</p>
          </div>
        {/if}
        <video
          bind:this={localPreview}
          class="local-video"
          class:off={camera !== 'on'}
          aria-label="Your camera"
          playsinline
        ></video>
        {#if cameraHeld && camera === 'off'}
          <div class="video-held">
            <p>This call is using a relay, so your video is not being sent.</p>
            <button
              type="button"
              class="secondary-button compact"
              onclick={() => call && oncamera?.(call, true)}
              >Send video over the relay</button
            >
          </div>
        {/if}
      </div>
    {/if}
    {#if showDetails && answered}
      <dl id="call-details" class="call-details" aria-live="off">
        {#if stats}
          <div>
            <dt>Your audio</dt>
            <dd>
              microphone {stats.captured} · sent {stats.sent} · level {stats.micLevel}%
            </dd>
          </div>
          <div>
            <dt>Their audio</dt>
            <dd>
              received {stats.received} · decoded {stats.decoded} · played {stats.playedSeconds}
              s · level {stats.incomingLevel}%
            </dd>
          </div>
          <div>
            <dt>Sound output</dt>
            <dd>{stats.output} · audio link {stats.link}</dd>
          </div>
          {#if stats.video}
            <div>
              <dt>Your video</dt>
              <dd>
                camera {stats.video.camera} · captured {stats.video.captured} · sent
                {stats.video.sent} · keyframes {stats.video.keyframesSent}
              </dd>
            </div>
            <div>
              <dt>Their video</dt>
              <dd>
                received {stats.video.received} · keyframes {stats.video
                  .keyframesReceived} · decoded {stats.video.decoded} · shown {stats
                  .video.shown}{stats.video.waitingForKeyframe
                  ? ' · waiting for a keyframe'
                  : ''}
              </dd>
            </div>
          {/if}
        {:else}
          <div>
            <dt>Audio</dt>
            <dd>starting…</dd>
          </div>
        {/if}
      </dl>
    {/if}
  </section>
{/if}

{#if notice}
  <div class="call-notice" role="status">
    <span>{notice}</span>
    <button
      type="button"
      class="icon-button"
      aria-label="Dismiss call notice"
      onclick={ondismiss}><Icon name="close" size="1em" /></button
    >
  </div>
{/if}

<style>
  .call-dialog {
    max-width: 440px;
  }
  .caller {
    font-size: 1.25rem;
    font-weight: var(--strong-weight, 700);
    overflow-wrap: anywhere;
  }
  .dialog-body p + p {
    margin-top: 12px;
  }
  .dialog-footer {
    gap: 12px;
    flex-wrap: wrap;
  }
  .call-bar,
  .call-notice {
    position: fixed;
    right: 20px;
    z-index: 30;
    display: flex;
    align-items: center;
    gap: 12px;
    max-width: min(560px, calc(100vw - 40px));
    padding: 10px 14px;
    border: 1px solid var(--accent-line, var(--border));
    border-radius: var(--radius, 14px);
    background: var(--raised, var(--panel));
    color: var(--text);
    box-shadow: var(--shadow-1, 0 8px 24px rgb(0 0 0 / 0.35));
  }
  .call-bar {
    top: 76px;
    flex-wrap: wrap;
  }
  .call-details {
    flex-basis: 100%;
    margin: 0;
    display: grid;
    gap: 4px;
    color: var(--muted);
    font-size: 0.85rem;
    font-variant-numeric: tabular-nums;
  }
  .call-details div {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
  }
  .call-details dt {
    color: var(--text);
    min-width: 6.5em;
  }
  .call-details dd {
    margin: 0;
  }
  .call-bar.with-video {
    max-width: min(680px, calc(100vw - 40px));
  }
  .call-video {
    position: relative;
    flex-basis: 100%;
    aspect-ratio: 16 / 9;
    border-radius: calc(var(--radius, 14px) - 4px);
    overflow: hidden;
    background: #000;
  }
  .remote-video {
    display: block;
    width: 100%;
    height: 100%;
    object-fit: contain;
    background: #000;
  }
  .remote-video.waiting {
    /* The last painted picture must not linger behind the avatar. */
    visibility: hidden;
  }
  .video-placeholder {
    position: absolute;
    inset: 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 12px;
    pointer-events: none;
  }
  .video-placeholder :global(.avatar) {
    width: clamp(64px, 22%, 128px);
    height: auto;
    aspect-ratio: 1;
    font-size: clamp(1.6rem, 5vw, 3rem);
  }
  .video-status {
    margin: 0;
    color: #fff;
    font-size: 0.95rem;
    text-shadow: 0 1px 2px rgb(0 0 0 / 0.8);
  }
  .local-video {
    position: absolute;
    right: 10px;
    bottom: 10px;
    width: 26%;
    aspect-ratio: 16 / 9;
    object-fit: cover;
    border-radius: 8px;
    border: 1px solid rgb(255 255 255 / 0.5);
    background: #111;
    /* A mirror image is what people expect of their own preview. Only the
       preview is mirrored; the other person sees the picture unmirrored. */
    transform: scaleX(-1);
  }
  .local-video.off {
    display: none;
  }
  .video-held {
    position: absolute;
    left: 10px;
    right: 10px;
    bottom: 10px;
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
    padding: 8px 10px;
    border-radius: 8px;
    background: rgb(0 0 0 / 0.72);
    color: #fff;
    font-size: 0.85rem;
  }
  .video-held p {
    margin: 0;
    flex: 1 1 12em;
  }
  .call-notice {
    bottom: 84px;
    border-color: var(--border);
  }
  .call-icon {
    color: var(--accent);
    display: inline-flex;
  }
  .call-who {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .call-who strong {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .call-status,
  .peer-muted {
    color: var(--muted);
    font-size: 0.85rem;
    font-variant-numeric: tabular-nums;
  }
  .mic-state {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    color: var(--ok, var(--accent));
    font-size: 0.85rem;
    white-space: nowrap;
  }
  .mic-state.muted {
    color: var(--warn, var(--muted));
  }
  .end-call {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    white-space: nowrap;
  }
</style>
