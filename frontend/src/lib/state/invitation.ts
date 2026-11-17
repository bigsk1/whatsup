import { writable } from 'svelte/store';
import { hasDesktopBridge } from '../backend';
import { copyText } from '../clipboard';
import {
  validConnectionService,
  validInvitationLink,
  type InvitationShare,
  type PairingStatus,
} from '../pairing';

export const publicRelayMap = 'https://tailcat.dev/derpmap.json';
export interface InvitationInspection {
  serviceURL: string;
  host: string;
}

export function validateInspection(value: unknown): InvitationInspection {
  if (!value || typeof value !== 'object' || Array.isArray(value))
    throw new Error('Invalid invitation service.');
  const record = value as Record<string, unknown>;
  if (
    Object.keys(record).length !== 2 ||
    typeof record.serviceURL !== 'string' ||
    !validConnectionService(record.serviceURL) ||
    typeof record.host !== 'string' ||
    record.host !== new URL(record.serviceURL).host
  )
    throw new Error('Invalid invitation service.');
  return { serviceURL: record.serviceURL, host: record.host };
}

export async function inspectInvitation(
  link: string,
): Promise<InvitationInspection> {
  if (!hasDesktopBridge() || !validInvitationLink(link))
    throw new Error(
      'Check the private invitation before reviewing its service.',
    );
  const service =
    await import('../../../bindings/whatsup/internal/desktop/service');
  return validateInspection(await service.InspectInvitation(link));
}

export function createInvitationForm(options: {
  enabled: () => boolean;
  pairing: () => PairingStatus;
  create: (url: string, consent: boolean) => Promise<InvitationShare>;
  join: (link: string, url: string, consent: boolean) => Promise<void>;
  cancel: () => Promise<void>;
  close: () => void;
  inspect?: typeof inspectInvitation;
  copy?: (value: string) => Promise<void>;
}) {
  type Stage =
    'form' | 'starting' | 'share' | 'joining' | 'failed' | 'canceling';
  const empty = () => ({
    mode: 'create' as 'create' | 'join',
    stage: 'form' as Stage,
    relayURL: '',
    consent: false,
    pastedLink: '',
    privateLink: '',
    expiresAt: '',
    error: '',
    copyNotice: '',
    relayCopyNotice: '',
    inspection: null as InvitationInspection | null,
    inspecting: false,
  });
  let current = empty();
  const state = writable(current);
  let operation = 0;
  let inspectionOperation = 0;
  let expiryTimer: ReturnType<typeof setTimeout> | undefined;
  let watchTerminal = false;
  let terminalBaseline: PairingStatus | undefined;
  const publish = (value: Partial<typeof current>) => {
    current = { ...current, ...value };
    state.set(current);
  };
  const live = (epoch: number) => epoch === operation && options.enabled();
  function reset() {
    operation += 1;
    inspectionOperation += 1;
    if (expiryTimer !== undefined) clearTimeout(expiryTimer);
    expiryTimer = undefined;
    watchTerminal = false;
    terminalBaseline = undefined;
    current = empty();
    state.set(current);
  }
  function ready() {
    return (
      options.enabled() &&
      current.stage === 'form' &&
      current.consent &&
      validConnectionService(current.relayURL.trim()) &&
      (current.mode === 'create' ||
        (current.inspection !== null &&
          validInvitationLink(current.pastedLink.trim())))
    );
  }
  async function inspect() {
    if (
      !options.enabled() ||
      current.stage !== 'form' ||
      current.mode !== 'join' ||
      !validInvitationLink(current.pastedLink.trim())
    )
      return;
    const epoch = operation,
      request = ++inspectionOperation,
      link = current.pastedLink.trim();
    publish({
      inspection: null,
      relayURL: '',
      consent: false,
      inspecting: true,
      error: '',
    });
    try {
      const inspection = validateInspection(
        await (options.inspect ?? inspectInvitation)(link),
      );
      if (
        live(epoch) &&
        request === inspectionOperation &&
        current.mode === 'join' &&
        current.pastedLink.trim() === link
      )
        publish({ inspection, relayURL: inspection.serviceURL });
    } catch {
      if (live(epoch) && request === inspectionOperation)
        publish({
          error:
            'This invitation could not be reviewed. Ask for a new invitation from a compatible app.',
        });
    } finally {
      if (live(epoch) && request === inspectionOperation)
        publish({ inspecting: false });
    }
  }
  async function start() {
    if (!ready()) return;
    const mode = current.mode,
      url = current.relayURL.trim(),
      link = current.pastedLink.trim(),
      epoch = ++operation;
    publish({
      relayURL: '',
      pastedLink: '',
      consent: false,
      inspection: null,
      error: '',
      stage: mode === 'create' ? 'starting' : 'joining',
    });
    try {
      if (mode === 'join') {
        await options.join(link, url, true);
        if (!live(epoch)) return;
      } else {
        const share = await options.create(url, true);
        if (!live(epoch)) return;
        publish({
          privateLink: share.link,
          expiresAt: share.expiresAt,
          stage: 'share',
        });
        expiryTimer = setTimeout(
          () => void cancel(),
          Math.min(
            600000,
            Math.max(0, Date.parse(share.expiresAt) - Date.now()),
          ),
        );
      }
      terminalBaseline = options.pairing();
      watchTerminal = true;
    } catch {
      if (live(epoch))
        publish({
          privateLink: '',
          pastedLink: '',
          stage: 'failed',
          error:
            mode === 'create'
              ? 'The invitation could not be created. Cancel this attempt, then check the connection service before trying again.'
              : 'The invitation could not be opened. Cancel this attempt, then check the link and connection service before trying again.',
        });
    }
  }
  async function cancel() {
    if (current.stage === 'canceling') return;
    reset();
    const epoch = operation;
    publish({ stage: 'canceling' });
    try {
      await options.cancel();
      if (epoch !== operation) return;
      publish({ stage: 'form' });
      options.close();
    } catch {
      if (live(epoch))
        publish({
          stage: 'failed',
          error:
            'Cancellation could not be confirmed. Try Cancel again or close WhatsUp.',
        });
    }
  }
  async function copy(relay: boolean) {
    if (
      !options.enabled() ||
      (relay
        ? current.stage !== 'form'
        : current.stage !== 'share' || current.privateLink === '')
    )
      return;
    const epoch = operation;
    try {
      await (options.copy ?? copyText)(
        relay ? publicRelayMap : current.privateLink,
      );
      if (live(epoch))
        publish(
          relay
            ? {
                relayCopyNotice:
                  'Public relay URL copied. Paste it into the field if you want to use it.',
              }
            : { copyNotice: 'Invitation copied.' },
        );
    } catch {
      if (live(epoch))
        publish(
          relay
            ? {
                relayCopyNotice:
                  'Copy is unavailable. Select the public relay URL and copy it.',
              }
            : {
                copyNotice:
                  'Copy is unavailable. Select the private link and copy it.',
              },
        );
    }
  }
  return {
    state,
    reset,
    ready,
    inspect,
    start,
    cancel,
    copyLink: () => copy(false),
    copyRelayURL: () => copy(true),
    setMode(mode: 'create' | 'join') {
      reset();
      publish({ mode });
    },
    setURL(relayURL: string) {
      if (current.mode === 'create') publish({ relayURL, consent: false });
    },
    usePublicRelay() {
      if (current.mode === 'create' && current.relayURL.trim() === '')
        publish({ relayURL: publicRelayMap, consent: false });
    },
    setConsent(consent: boolean) {
      publish({ consent });
    },
    setLink(pastedLink: string) {
      inspectionOperation += 1;
      publish({
        pastedLink,
        inspection: null,
        inspecting: false,
        relayURL: '',
        consent: false,
        error: '',
      });
    },
    observe(pairing: PairingStatus) {
      if (
        watchTerminal &&
        pairing !== terminalBaseline &&
        ['failed', 'expired', 'canceled', 'denied'].includes(pairing.state)
      ) {
        const ended = pairing.state;
        reset();
        publish({
          stage: 'failed',
          error:
            ended === 'expired'
              ? 'This invitation expired. Cancel this attempt, then use a new private invitation.'
              : ended === 'failed'
                ? 'The invitation connection failed. Cancel this attempt, then check the link and connection service before trying again.'
                : 'This invitation was canceled or denied. Cancel this attempt before starting another invitation.',
        });
      }
    },
  };
}
