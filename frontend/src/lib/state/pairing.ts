import { writable } from 'svelte/store';
import { Events } from '@wailsio/runtime';
import {
  approvePairing,
  denyPairing,
  createInvitation,
  joinInvitation,
  cancelInvitation,
  hasPairingIdentity,
  requestPairingStatus,
  unavailablePairing,
  type PairingStatus,
} from '../pairing';

export function createPairing(options: {
  enabled: () => boolean;
  available: () => boolean;
  contactCount: () => number;
  changed: () => void;
}) {
  let pairing: PairingStatus = { ...unavailablePairing };
  let pairingNotice = '';
  let actionRequest = '';
  let actionKind: 'approve' | 'deny' | '' = '';
  let pairingEpoch = 0;
  let requestSequence = 0;
  let acceptedSequence = 0;
  let activePoll: number | undefined;
  let activeAction = 0;
  let stopEvents: (() => void) | undefined;
  let refreshAgain = false;
  let invitationBusy = false;
  let invitationSequence = 0;
  const state = writable<{
    pairing: PairingStatus;
    pairingNotice: string;
    actionRequest: string;
    actionKind: 'approve' | 'deny' | '';
    invitationBusy: boolean;
  }>({
    pairing,
    pairingNotice,
    actionRequest,
    actionKind,
    invitationBusy,
  });
  function emit() {
    state.set({
      pairing,
      pairingNotice,
      actionRequest,
      actionKind,
      invitationBusy,
    });
  }
  function canStart() {
    return (
      options.enabled() &&
      options.available() &&
      options.contactCount() < 32 &&
      !invitationBusy &&
      !hasPairingIdentity(pairing) &&
      pairing.state !== 'connecting'
    );
  }
  function clearPairing() {
    stopEvents?.();
    stopEvents = undefined;
    window.removeEventListener('focus', recoverPairing);
    window.removeEventListener('online', recoverPairing);
    document.removeEventListener('visibilitychange', recoverPairing);
    refreshAgain = false;
    pairingEpoch += 1;
    activePoll = undefined;
    pairing = { ...unavailablePairing };
    pairingNotice = '';
    actionRequest = '';
    actionKind = '';
    invitationSequence += 1;
    invitationBusy = false;
    emit();
  }
  async function beginInvitation(url: string, consent: boolean) {
    if (!canStart()) throw new Error('Invitation setup is unavailable.');
    const epoch = pairingEpoch;
    const operation = ++invitationSequence;
    invitationBusy = true;
    emit();
    try {
      const share = await createInvitation(url, consent);
      if (
        epoch !== pairingEpoch ||
        operation !== invitationSequence ||
        !options.enabled()
      )
        throw new Error('Invitation setup is no longer available.');
      return share;
    } finally {
      if (operation === invitationSequence) invitationBusy = false;
      emit();
    }
  }

  async function openInvitation(link: string, url: string, consent: boolean) {
    if (!canStart()) throw new Error('Invitation setup is unavailable.');
    const epoch = pairingEpoch;
    const operation = ++invitationSequence;
    const sequence = ++requestSequence;
    invitationBusy = true;
    emit();
    try {
      const value = await joinInvitation(link, url, consent);
      if (
        epoch !== pairingEpoch ||
        operation !== invitationSequence ||
        !options.enabled()
      )
        throw new Error('Invitation setup is no longer available.');
      if (
        sequence >= acceptedSequence &&
        (!hasPairingIdentity(pairing) || pairing.requestID === value.requestID)
      ) {
        acceptedSequence = sequence;
        pairing = value;
      }
    } finally {
      if (operation === invitationSequence) invitationBusy = false;
      emit();
    }
  }

  async function stopInvitation() {
    if (!options.enabled())
      throw new Error('Invitation cancellation is unavailable.');
    clearPairing();
    const epoch = pairingEpoch;
    const operation = ++invitationSequence;
    invitationBusy = true;
    emit();
    try {
      const value = await cancelInvitation();
      if (
        epoch === pairingEpoch &&
        operation === invitationSequence &&
        options.enabled()
      ) {
        acceptedSequence = ++requestSequence;
        pairing = value;
      }
    } finally {
      if (operation === invitationSequence) {
        invitationBusy = false;
        startPairingEvents();
      }
      emit();
    }
  }

  function recoverPairing() {
    if (!document.hidden && options.enabled()) void refreshPairing();
  }
  function startPairingEvents() {
    if (!options.enabled()) return;
    if (!stopEvents) {
      stopEvents = Events.On('whatsup:pairing-changed', (event) => {
        const hint = event.data;
        if (
          hint &&
          typeof hint === 'object' &&
          !Array.isArray(hint) &&
          Object.keys(hint).length === 1 &&
          Number.isSafeInteger(hint.revision) &&
          hint.revision > 0
        )
          void refreshPairing();
      });
      window.addEventListener('focus', recoverPairing);
      window.addEventListener('online', recoverPairing);
      document.addEventListener('visibilitychange', recoverPairing);
    }
    void refreshPairing();
  }

  async function refreshPairing() {
    if (!options.enabled()) return;
    if (activePoll !== undefined) {
      refreshAgain = true;
      return;
    }
    const epoch = pairingEpoch;
    const sequence = ++requestSequence;
    activePoll = sequence;
    try {
      const value = await requestPairingStatus();
      if (
        epoch === pairingEpoch &&
        options.enabled() &&
        sequence >= acceptedSequence
      ) {
        acceptedSequence = sequence;
        if (
          value.requestID !== pairing.requestID ||
          value.state !== pairing.state
        )
          pairingNotice = '';
        const contactChanged =
          value.state === 'completed' && pairing.state !== 'completed';
        pairing = value;
        if (contactChanged) options.changed();
      }
    } catch {
      if (
        epoch === pairingEpoch &&
        options.enabled() &&
        sequence >= acceptedSequence
      ) {
        acceptedSequence = sequence;
        pairing = { ...unavailablePairing };
        pairingNotice = '';
      }
    } finally {
      if (activePoll === sequence) activePoll = undefined;
      emit();
      if (refreshAgain && epoch === pairingEpoch && options.enabled()) {
        refreshAgain = false;
        queueMicrotask(() => void refreshPairing());
      }
    }
  }

  async function decidePairing(
    selectedRequest: string,
    approve: boolean,
    fingerprint = '',
  ) {
    if (
      !options.enabled() ||
      selectedRequest !== pairing.requestID ||
      selectedRequest === '' ||
      (approve && pairing.state !== 'awaiting-approval') ||
      (actionRequest === selectedRequest && (approve || actionKind === 'deny'))
    )
      return;
    const epoch = pairingEpoch;
    const sequence = ++requestSequence;
    activeAction = sequence;
    actionRequest = selectedRequest;
    actionKind = approve ? 'approve' : 'deny';
    pairingNotice = '';
    emit();
    try {
      const value = approve
        ? await approvePairing(selectedRequest, fingerprint)
        : await denyPairing(selectedRequest);
      if (
        epoch === pairingEpoch &&
        options.enabled() &&
        pairing.requestID === selectedRequest &&
        sequence >= acceptedSequence
      ) {
        acceptedSequence = sequence;
        pairing = value;
      }
    } catch {
      if (
        epoch === pairingEpoch &&
        options.enabled() &&
        pairing.requestID === selectedRequest &&
        sequence >= acceptedSequence
      ) {
        acceptedSequence = sequence;
        pairingNotice = approve
          ? 'Pairing approval could not be confirmed. Check the request before trying again.'
          : 'Pairing cancellation could not be confirmed. Try again or lock storage to cancel.';
      }
    } finally {
      if (activeAction === sequence) {
        actionRequest = '';
        actionKind = '';
      }
      emit();
    }
  }

  return {
    state,
    start: startPairingEvents,
    stop: clearPairing,
    create: beginInvitation,
    join: openInvitation,
    cancel: stopInvitation,
    decide: decidePairing,
  };
}
