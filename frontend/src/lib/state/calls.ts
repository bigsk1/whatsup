import { Events, Stream } from '@wailsio/runtime';
import { writable } from 'svelte/store';
import { hasDesktopBridge } from '../backend';
import {
  createAudioEngine,
  type AudioFailure,
  type AudioStage,
  type AudioStats,
} from '../call/audio';

export type { AudioStats };
/**
 * Audio counters, the state of the page's link to the desktop, and video
 * counters when the call is a video call.
 */
export type CallStats = AudioStats & {
  link: MediaLinkState | 'unknown';
  video: VideoStats | null;
};
import {
  callMediaStream,
  decodeMedia,
  encodeAudio,
  encodeHello,
  encodeVideo,
  validCallID,
  type ReceivedMedia,
} from '../call/protocol';
import {
  createVideoEngine,
  type CameraState,
  type VideoEngine,
  type VideoFailure,
  type VideoStats,
} from '../call/video';

export type { CameraState, VideoStats };
import { createRinger, type Ringer } from '../call/ring';
import { validID } from './bridge';

export type CallStateName =
  'idle' | 'outgoing' | 'incoming' | 'connecting' | 'active';
export type CallAvailability =
  'ready' | 'calls-off' | 'offline' | 'peer-update' | 'busy' | 'blocked';

export interface CallView {
  contactID: string;
  state: CallStateName;
  callID: string;
  peerRinging: boolean;
  micMuted: boolean;
  peerMicMuted: boolean;
  activeSince: number;
  availability: CallAvailability;
  lastEnd: string;
  lastOutgoing: boolean;
  lastConnected: boolean;
  /** The call in progress is a video call. */
  video: boolean;
  /** This device's camera, as the desktop recorded it. */
  cameraOn: boolean;
  /** The other device's camera, as it last announced it. */
  peerCameraOn: boolean;
}

const states = ['idle', 'outgoing', 'incoming', 'connecting', 'active'];
const availabilities = [
  'ready',
  'calls-off',
  'offline',
  'peer-update',
  'busy',
  'blocked',
];
const ends = [
  '',
  'hung-up',
  'peer-hung-up',
  'cancelled',
  'declined',
  'peer-declined',
  'peer-busy',
  'peer-unavailable',
  'no-answer',
  'missed',
  'connection-lost',
  'media-failed',
  'device-error',
  'protocol',
  'interrupted',
];

export function validateCallView(value: unknown, contactID: string): CallView {
  const invalid = () => new Error('The desktop returned invalid call data.');
  if (!value || typeof value !== 'object' || Array.isArray(value))
    throw invalid();
  const record = value as Record<string, unknown>;
  if (
    Object.keys(record).length !== 14 ||
    record.contactID !== contactID ||
    !validID(contactID) ||
    typeof record.state !== 'string' ||
    !states.includes(record.state) ||
    typeof record.callID !== 'string' ||
    (record.state === 'idle'
      ? record.callID !== ''
      : !validCallID(record.callID)) ||
    typeof record.peerRinging !== 'boolean' ||
    typeof record.micMuted !== 'boolean' ||
    typeof record.peerMicMuted !== 'boolean' ||
    !Number.isSafeInteger(record.activeSince) ||
    (record.activeSince as number) < 0 ||
    typeof record.availability !== 'string' ||
    !availabilities.includes(record.availability) ||
    typeof record.lastEnd !== 'string' ||
    !ends.includes(record.lastEnd) ||
    typeof record.lastOutgoing !== 'boolean' ||
    typeof record.lastConnected !== 'boolean' ||
    typeof record.video !== 'boolean' ||
    typeof record.cameraOn !== 'boolean' ||
    typeof record.peerCameraOn !== 'boolean' ||
    // Camera state means nothing outside a video call in progress.
    (!record.video && (record.cameraOn || record.peerCameraOn)) ||
    (record.state === 'idle' && record.video)
  )
    throw invalid();
  return {
    contactID,
    state: record.state as CallStateName,
    callID: record.callID,
    peerRinging: record.peerRinging,
    micMuted: record.micMuted,
    peerMicMuted: record.peerMicMuted,
    activeSince: record.activeSince as number,
    availability: record.availability as CallAvailability,
    lastEnd: record.lastEnd,
    lastOutgoing: record.lastOutgoing,
    lastConnected: record.lastConnected,
    video: record.video,
    cameraOn: record.cameraOn,
    peerCameraOn: record.peerCameraOn,
  };
}

export function validateCallHint(value: unknown): string | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  return Object.keys(record).length === 1 && validID(record.contactID)
    ? record.contactID
    : null;
}

async function service() {
  if (!hasDesktopBridge()) throw new Error('Open the desktop app to call.');
  return import('../../../bindings/whatsup/internal/desktop/service');
}

export interface CallBridge {
  get(contactID: string): Promise<unknown>;
  start(contactID: string, video: boolean): Promise<unknown>;
  answer(contactID: string, callID: string, video: boolean): Promise<unknown>;
  end(contactID: string, callID: string): Promise<unknown>;
  setMuted(contactID: string, callID: string, muted: boolean): Promise<unknown>;
  setCamera(
    contactID: string,
    callID: string,
    on: boolean,
    overRelay: boolean,
  ): Promise<unknown>;
  subscribe(callback: (hint: unknown) => void): () => void;
}

export const callBridge: CallBridge = {
  get: async (contactID) => (await service()).GetCall(contactID),
  start: async (contactID, video) =>
    (await service()).StartCall(contactID, video),
  answer: async (contactID, callID, video) =>
    (await service()).AnswerCall(contactID, callID, video),
  setCamera: async (contactID, callID, on, overRelay) =>
    (await service()).SetCallCamera(contactID, callID, on, overRelay),
  end: async (contactID, callID) =>
    (await service()).EndCall(contactID, callID),
  setMuted: async (contactID, callID, muted) =>
    (await service()).SetCallMuted(contactID, callID, muted),
  subscribe(callback) {
    return Events.On('whatsup:call-changed', (event) => callback(event.data));
  },
};

export type MediaLinkState = 'connecting' | 'open' | 'closed';

export interface MediaLink {
  send(frame: Uint8Array): void;
  close(): void;
  /** Whether audio can currently pass between the page and the desktop. */
  state?(): MediaLinkState;
}

/** The part of a stream socket the media link uses. */
export interface MediaSocket {
  binaryType: string;
  readyState: number;
  send(data: Uint8Array<ArrayBuffer>): void;
  close(): void;
  addEventListener(type: string, listener: (event: Event) => void): void;
}

const linkRetryMs = 300;
const linkMaxOpens = 6;

export type OpenMedia = (
  callID: string,
  receive: (frame: ReceivedMedia) => void,
) => MediaLink;

/**
 * Encoded audio and video to and from Go over a stream socket. If the socket ends while
 * the call is still answered, a new one is opened and names the call again,
 * a bounded number of times: a call must not stay up with no way for audio to
 * pass. Go accepts a stream only for an answered call, so a reopen after the
 * call has ended simply closes again.
 */
export function createMediaLink(
  connect: () => MediaSocket,
  callID: string,
  receive: (frame: ReceivedMedia) => void,
  retryMs = linkRetryMs,
): MediaLink {
  const OPEN = 1;
  let socket: MediaSocket | null = null;
  let ready = false;
  let finished = false;
  let opens = 0;
  let timer: ReturnType<typeof setTimeout> | null = null;

  function open() {
    if (finished || opens >= linkMaxOpens) return;
    opens += 1;
    const current = connect();
    socket = current;
    ready = false;
    current.binaryType = 'arraybuffer';
    current.addEventListener('open', () => {
      if (finished || socket !== current) return;
      current.send(encodeHello(callID) as Uint8Array<ArrayBuffer>);
      ready = true;
    });
    current.addEventListener('message', (event) => {
      if (finished || socket !== current) return;
      const frame = decodeMedia((event as MessageEvent).data);
      if (frame) receive(frame);
    });
    current.addEventListener('close', () => {
      if (finished || socket !== current) return;
      ready = false;
      socket = null;
      timer = setTimeout(open, retryMs);
    });
  }
  open();

  return {
    // Media produced before the stream is up is dropped, never queued: late
    // audio or video is worse than none.
    send(frame) {
      if (ready && socket?.readyState === OPEN)
        socket.send(frame as Uint8Array<ArrayBuffer>);
    },
    close() {
      finished = true;
      ready = false;
      if (timer) clearTimeout(timer);
      socket?.close();
      socket = null;
    },
    state: () =>
      finished || (!socket && opens >= linkMaxOpens)
        ? 'closed'
        : ready && socket?.readyState === OPEN
          ? 'open'
          : 'connecting',
  };
}

/** Encoded audio to and from Go over the app's own origin. No port is opened. */
export const openStreamMedia: OpenMedia = (callID, receive) =>
  createMediaLink(
    () => Stream(callMediaStream) as unknown as MediaSocket,
    callID,
    receive,
  );

export interface CallEngine {
  start(): Promise<void>;
  stop(): void;
  receive(timestamp: number, data: Uint8Array): void;
  setMuted(muted: boolean): void;
  stats?(): AudioStats | null;
}

export type EngineFactory = (options: {
  send(timestamp: number, data: Uint8Array): void;
  failed(reason: AudioFailure, stage?: AudioStage): void;
}) => CallEngine;

/** The part of the video engine the store uses. */
export type CallVideo = Pick<
  VideoEngine,
  | 'startCamera'
  | 'stopCamera'
  | 'stop'
  | 'keyframe'
  | 'receive'
  | 'setCanvas'
  | 'localStream'
  | 'camera'
  | 'hasPicture'
  | 'forgetPicture'
  | 'stats'
>;

export interface WindowFocus {
  has(): boolean;
  /** Calls back each time the window comes to the front; returns a stop. */
  subscribe(callback: () => void): () => void;
}

const browserWindowFocus: WindowFocus = {
  has: () => typeof document === 'undefined' || document.hasFocus(),
  subscribe(callback) {
    if (typeof window === 'undefined') return () => {};
    window.addEventListener('focus', callback);
    return () => window.removeEventListener('focus', callback);
  },
};

export type VideoFactory = (options: {
  send(timestamp: number, key: boolean, data: Uint8Array): void;
  failed(reason: VideoFailure): void;
  changed(): void;
}) => CallVideo;

const endNotices: Record<string, string> = {
  'peer-hung-up': 'Call ended.',
  'peer-declined': 'Call declined.',
  'peer-busy': 'They are on another call.',
  'peer-unavailable': 'They have voice calls switched off.',
  'no-answer': 'No answer.',
  missed: 'Missed call.',
  'connection-lost': 'Call ended: the connection was lost.',
  'media-failed': 'Call ended: audio could not be sent or received.',
  'device-error': 'Call ended: the microphone stopped working.',
  protocol: 'Call ended unexpectedly.',
  interrupted: 'Call ended.',
};

// A desktop may only ask about the camera while this window is in front, so
// these two say how to try again.
const cameraRetryHint =
  'Bring WhatsUp to the front and press the camera button to try again; your system may ask for permission first. The call continues with voice.';
const videoNotices: Record<VideoFailure, string> = {
  permission: 'WhatsUp was not allowed to use the camera. ' + cameraRetryHint,
  unavailable: 'The camera could not be started. ' + cameraRetryHint,
  'no-camera':
    'No camera was found on this device. The call continues with voice.',
  unsupported: 'This device cannot send video. The call continues with voice.',
  'no-picture': 'The camera gave no picture. The call continues with voice.',
  stuck:
    "This device's camera picture is not updating, so your video was switched off. The call continues with voice.",
  failed: 'The camera stopped working. The call continues with voice.',
};

const audioNotices: Record<AudioFailure, string> = {
  permission:
    'WhatsUp could not use the microphone. Allow microphone access for WhatsUp, then call again.',
  'no-microphone': 'No microphone was found on this device.',
  unsupported: 'This device cannot run call audio.',
  failed: 'Call audio stopped working.',
};

export interface CallsState {
  views: Record<string, CallView>;
  /** The one call in progress on this device, in any non-idle state. */
  current: CallView | null;
  notice: string;
  busy: boolean;
  /** This window's camera: off, starting or on. */
  camera: CameraState;
  /**
   * The call is relayed, so video is not sent until the person chooses to
   * send it over the relay.
   */
  cameraHeld: boolean;
  /** The other person's first picture has been painted. */
  remotePicture: boolean;
}

const answered = (view: CallView | null) =>
  view !== null && (view.state === 'connecting' || view.state === 'active');

export function createCalls(options: {
  /** Storage is unlocked and the bridge is usable. */
  enabled: () => boolean;
  ringMuted: () => boolean;
  bridge?: CallBridge;
  engine?: EngineFactory;
  ringer?: Ringer;
  openMedia?: OpenMedia;
  video?: VideoFactory;
  /** Whether the connection to this contact is currently relayed. */
  relayed?: (contactID: string) => boolean;
  /** Whether this window is in front, and a way to hear when it becomes so. */
  windowFocus?: WindowFocus;
  /** How long an ordinary "call ended" notice stays; 0 keeps it. */
  endNoticeMs?: number;
}) {
  const bridge = options.bridge ?? callBridge;
  const makeEngine: EngineFactory = options.engine ?? createAudioEngine;
  const ringer = options.ringer ?? createRinger(options.ringMuted);
  const openMedia = options.openMedia ?? openStreamMedia;
  const makeVideo: VideoFactory = options.video ?? createVideoEngine;
  const relayed = options.relayed ?? (() => false);
  const windowFocus = options.windowFocus ?? browserWindowFocus;
  // A call whose camera was turned down while this window was not in front.
  // Some desktops only ask about the camera for the window in front, so it is
  // tried once more, on its own, when the window next comes forward.
  let cameraOnFocus = '';
  const stopFocus = windowFocus.subscribe(() => {
    const callID = cameraOnFocus;
    cameraOnFocus = '';
    if (!callID || media?.callID !== callID || !media.video) return;
    if (media.video.camera() !== 'off' || current.cameraHeld) return;
    publish({ notice: '' });
    void cameraOn(media.contactID, callID, false);
  });

  const noVideo = {
    camera: 'off',
    cameraHeld: false,
    remotePicture: false,
  } as const;
  let current: CallsState = {
    views: {},
    current: null,
    notice: '',
    busy: false,
    ...noVideo,
  };
  const state = writable(current);
  const publish = (value: Partial<CallsState>) => {
    current = { ...current, ...value };
    state.set(current);
  };

  // An ordinary ending ("Call ended.", "No answer.") clears itself. A notice
  // about a problem stays until it is dismissed or the next call starts.
  const endNoticeMs = options.endNoticeMs ?? 6000;
  let noticeTimer: ReturnType<typeof setTimeout> | null = null;
  function passingNotice(text: string) {
    if (noticeTimer) clearTimeout(noticeTimer);
    noticeTimer = null;
    publish({ notice: text });
    if (endNoticeMs > 0)
      noticeTimer = setTimeout(() => {
        noticeTimer = null;
        if (current.notice === text) publish({ notice: '' });
      }, endNoticeMs);
  }

  let generation = 0;
  const sequence: Record<string, number> = {};
  let ringing = '';
  let media: {
    callID: string;
    contactID: string;
    link: MediaLink;
    engine: CallEngine;
    video: CallVideo | null;
    /** The person chose to send this call's video over a relay. */
    relayChosen: boolean;
  } | null = null;
  // Calls this device has ended on its own side: hung up, declined, or calls
  // switched off. Capture for such a call never starts again, whatever a
  // later view says. A failed or slow hang-up must not reopen the microphone.
  const endedHere: string[] = [];
  function endLocally(callID: string) {
    if (!callID || endedHere.includes(callID)) return;
    endedHere.push(callID);
    if (endedHere.length > 16) endedHere.shift();
  }

  function stopMedia() {
    if (!media) return;
    const stale = media;
    media = null;
    // Release the microphone and camera first; closing the stream cannot
    // block that.
    stale.engine.stop();
    stale.video?.stop();
    stale.link.close();
    cameraOnFocus = '';
    if (current.camera !== 'off' || current.cameraHeld || current.remotePicture)
      publish({ ...noVideo });
  }

  function startMedia(view: CallView) {
    stopMedia();
    const callID = view.callID;
    const contactID = view.contactID;
    const engine = makeEngine({
      send: (timestamp, data) => {
        if (media?.callID === callID)
          media.link.send(encodeAudio(timestamp, data));
      },
      failed: (reason, stage) => {
        if (media?.callID !== callID) return;
        // The step is one of four fixed words, kept so a failure on a real
        // device can be reported from the screen alone.
        const step = stage ? ` (step: ${stage})` : '';
        publish({ notice: audioNotices[reason] + step });
        void end(contactID, callID);
      },
    });
    // A video call decodes whatever video arrives from the start. This
    // device's own camera is a separate decision; see cameraOn.
    const video = view.video
      ? makeVideo({
          send: (timestamp, key, data) => {
            if (media?.callID === callID)
              media.link.send(encodeVideo(timestamp, key, data));
          },
          failed: (reason) => {
            if (media?.callID !== callID) return;
            // The camera is already released. The call carries on with voice.
            publish({ notice: videoNotices[reason] });
            if (
              (reason === 'permission' || reason === 'unavailable') &&
              !windowFocus.has()
            )
              cameraOnFocus = callID;
            void cameraOff(contactID, callID);
          },
          changed: () => {
            if (media?.callID === callID && media.video)
              publish({
                camera: media.video.camera(),
                remotePicture: media.video.hasPicture(),
              });
          },
        })
      : null;
    const link = openMedia(callID, (frame) => {
      if (media?.callID !== callID) return;
      if (frame.kind === 'audio') engine.receive(frame.timestamp, frame.data);
      else if (frame.kind === 'video')
        video?.receive(frame.timestamp, frame.key, frame.data);
      else video?.keyframe();
    });
    media = { callID, contactID, link, engine, video, relayChosen: false };
    engine.setMuted(view.micMuted);
    void engine.start();
    if (video) {
      // Over a relay, video waits for the person to choose it.
      if (relayed(contactID)) publish({ cameraHeld: true });
      else void cameraOn(contactID, callID, false);
    }
  }

  /**
   * Switch this device's camera on for the answered video call. The desktop
   * is told first: it opens the camera permission only for a camera it has
   * recorded as on, and refuses over a relay unless that was chosen.
   */
  async function cameraOn(
    contactID: string,
    callID: string,
    overRelay: boolean,
  ) {
    if (media?.callID !== callID || !media.video) return;
    if (endedHere.includes(callID) || media.video.camera() !== 'off') return;
    publish({ cameraHeld: false });
    // The desktop's own answer decides, not the view on screen: a refresh
    // that started later may be the one shown, and it can predate the change.
    const view = await run(contactID, () =>
      bridge.setCamera(contactID, callID, true, overRelay),
    );
    if (media?.callID !== callID || !media.video) return;
    if (!view || view.callID !== callID || !view.cameraOn) {
      // Refused: most likely the connection became relayed in the meantime.
      if (relayed(contactID)) publish({ cameraHeld: true });
      return;
    }
    if (overRelay) media.relayChosen = true;
    // The desktop approved this while the connection was direct. If the
    // connection turned relayed while it was being asked, the path handler saw
    // a camera that was still off and had nothing to stop, so the check is
    // made again here, before any camera opens: take the approval back and
    // offer the choice.
    if (!media.relayChosen && relayed(contactID)) {
      publish({ cameraHeld: true });
      await cameraOff(contactID, callID);
      return;
    }
    await media.video.startCamera();
  }

  /** Release the camera here first, then tell the desktop. */
  function cameraOff(contactID: string, callID: string) {
    if (media?.callID === callID) media.video?.stopCamera();
    if (endedHere.includes(callID)) return Promise.resolve();
    return run(contactID, () =>
      bridge.setCamera(contactID, callID, false, false),
    );
  }

  function settle(view: CallView | null, previous: CallView | null) {
    // Ring sounds follow the ringing states only.
    const ringKey =
      view && (view.state === 'incoming' || view.state === 'outgoing')
        ? view.state + view.callID
        : '';
    if (ringKey !== ringing) {
      ringing = ringKey;
      if (view?.state === 'incoming') ringer.ringtone();
      else if (view?.state === 'outgoing') ringer.ringback();
      else ringer.stop();
    }
    // The microphone follows answered calls only. A ringing call, incoming
    // or outgoing, never reaches this branch.
    if (answered(view) && !endedHere.includes(view!.callID)) {
      if (media?.callID !== view!.callID) startMedia(view!);
      else media.engine.setMuted(view!.micMuted);
      // Their camera is off: the last picture is no longer them. The next
      // one shown will be a fresh one.
      if (media?.video && !view!.peerCameraOn && media.video.hasPicture())
        media.video.forgetPicture();
    } else stopMedia();
    if (previous && previous.state !== 'idle' && !view) {
      const ended = current.views[previous.contactID];
      const text = ended ? (endNotices[ended.lastEnd] ?? '') : '';
      if (text && !current.notice) passingNotice(text);
    }
  }

  function apply(view: CallView) {
    const previous = current.current;
    const views = { ...current.views, [view.contactID]: view };
    const live = Object.values(views).find((v) => v.state !== 'idle') ?? null;
    const starting = live && (!previous || previous.callID !== live.callID);
    publish({
      views,
      current: live,
      ...(starting ? { notice: '' } : {}),
    });
    settle(live, previous);
  }

  /**
   * Makes a request and shows its answer, unless a later request for the same
   * contact has been made since. Returns the answer either way, or null when
   * the request failed or the store was reset meanwhile.
   */
  async function run(
    contactID: string,
    request: () => Promise<unknown>,
    quiet = false,
  ): Promise<CallView | null> {
    if (!options.enabled() || !validID(contactID)) return null;
    const epoch = generation;
    const turn = (sequence[contactID] = (sequence[contactID] ?? 0) + 1);
    try {
      const view = validateCallView(await request(), contactID);
      if (epoch !== generation) return null;
      if (turn === sequence[contactID]) apply(view);
      return view;
    } catch (error) {
      if (epoch !== generation || quiet) return null;
      const message = error instanceof Error ? error.message : '';
      publish({
        notice: /^[A-Z][^<>{}]{0,160}$/.test(message)
          ? message
          : 'The call could not be changed. Try again.',
      });
      // The view may have changed even though the action failed.
      void run(contactID, () => bridge.get(contactID), true);
      return null;
    }
  }

  async function act(contactID: string, request: () => Promise<unknown>) {
    if (current.busy) return;
    publish({ busy: true });
    try {
      await run(contactID, request);
    } finally {
      publish({ busy: false });
    }
  }

  const refresh = (contactID: string) =>
    run(contactID, () => bridge.get(contactID), true);

  function end(contactID: string, callID: string) {
    // Stop capturing before asking Go: hanging up must never wait on the
    // bridge to release the microphone. The call stays ended here even if the
    // request fails and a refresh still shows it as answered.
    endLocally(callID);
    if (media?.callID === callID) stopMedia();
    return run(contactID, () => bridge.end(contactID, callID));
  }

  let unsubscribe: (() => void) | null = null;

  return {
    state,
    /** Start listening for call changes from Go. */
    listen() {
      unsubscribe?.();
      unsubscribe = bridge.subscribe((hint) => {
        const contactID = validateCallHint(hint);
        if (contactID) void refresh(contactID);
      });
    },
    refresh,
    /** Place a voice call, or a video call when video is true. */
    start: (contactID: string, video = false) =>
      act(contactID, () => bridge.start(contactID, video)),
    /** Accept the ringing call, with video only if asked and offered. */
    answer(view: CallView, video = false) {
      if (view.state !== 'incoming') return Promise.resolve();
      return act(view.contactID, () =>
        bridge.answer(view.contactID, view.callID, video && view.video),
      );
    },
    /**
     * Switch this device's camera on or off in the answered video call.
     * overRelay is the person's explicit choice to send video over a relay.
     */
    toggleCamera(view: CallView, overRelay = false) {
      if (!answered(view) || !view.video || media?.callID !== view.callID)
        return Promise.resolve();
      // The person has decided: no automatic retry stands behind this.
      cameraOnFocus = '';
      if (media.video && media.video.camera() !== 'off')
        return cameraOff(view.contactID, view.callID);
      return cameraOn(view.contactID, view.callID, overRelay);
    },
    /** Where the other person's picture is painted; null detaches. */
    setVideoCanvas(canvas: HTMLCanvasElement | null) {
      media?.video?.setCanvas(canvas);
    },
    /** This device's live camera stream for the local preview, or null. */
    localStream: (): MediaStream | null => media?.video?.localStream() ?? null,
    end(view: CallView) {
      if (view.state === 'idle') return Promise.resolve();
      return end(view.contactID, view.callID);
    },
    toggleMute(view: CallView) {
      if (!answered(view)) return Promise.resolve();
      return run(view.contactID, () =>
        bridge.setMuted(view.contactID, view.callID, !view.micMuted),
      );
    },
    /**
     * The connection to a contact changed between direct and relayed.
     *
     * Video that was held back only because of a relay starts by itself once
     * the connection is direct; the person never had to decide anything. And
     * video that started on a direct connection stops when the connection
     * falls back to a relay, unless the person already chose to send this
     * call's video that way: the camera is released and the choice is offered
     * again. This follows the same sampled reading as the label on screen.
     */
    pathChanged(contactID: string) {
      if (!media || media.contactID !== contactID || !media.video) return;
      const callID = media.callID;
      if (relayed(contactID)) {
        if (media.relayChosen || media.video.camera() === 'off') return;
        cameraOnFocus = '';
        publish({ cameraHeld: true });
        void cameraOff(contactID, callID);
        return;
      }
      if (!current.cameraHeld || media.video.camera() !== 'off') return;
      void cameraOn(contactID, callID, false);
    },
    dismissNotice: () => publish({ notice: '' }),
    /**
     * Voice calls are being switched off, or capture must stop for another
     * local reason: release the microphone and the ring now, and keep them
     * released for the call in progress. Nothing here waits on the desktop.
     */
    stopCapture() {
      if (current.current) endLocally(current.current.callID);
      ringing = '';
      ringer.stop();
      stopMedia();
    },
    /**
     * Counters for the answered call's audio, or null when no call is
     * answered. Read on demand by the call details readout.
     */
    stats: (): CallStats | null => {
      if (!media) return null;
      const audio = media.engine.stats?.();
      return audio
        ? {
            ...audio,
            link: media.link.state?.() ?? 'unknown',
            video: media.video?.stats() ?? null,
          }
        : null;
    },
    /** Storage locked or the app is closing: release everything at once. */
    reset() {
      generation += 1;
      if (noticeTimer) clearTimeout(noticeTimer);
      noticeTimer = null;
      ringing = '';
      ringer.stop();
      stopMedia();
      endedHere.length = 0;
      publish({
        views: {},
        current: null,
        notice: '',
        busy: false,
        ...noVideo,
      });
    },
    destroy() {
      unsubscribe?.();
      unsubscribe = null;
      stopFocus();
      if (noticeTimer) clearTimeout(noticeTimer);
      noticeTimer = null;
      generation += 1;
      ringer.stop();
      stopMedia();
    },
  };
}
