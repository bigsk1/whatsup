import { writable } from 'svelte/store';
import {
  requestProfile,
  saveProfile,
  saveContactName,
  type LocalProfile,
} from '../profile';
import type { ConversationSnapshot } from '../messaging';

export function createProfile(options: {
  enabled: () => boolean;
  selectedContact: () => string;
  apply: (value: ConversationSnapshot) => boolean;
}) {
  const empty = () => ({
    profile: null as LocalProfile | null,
    loading: false,
    saving: false,
    notice: '',
    contactSaving: false,
    contactNotice: '',
  });
  let current = empty();
  const state = writable(current);
  let generation = 0;
  let contactGeneration = 0;
  const publish = (value: Partial<typeof current>) => {
    current = { ...current, ...value };
    state.set(current);
  };
  const live = (epoch: number) => epoch === generation && options.enabled();
  function resetContact() {
    contactGeneration += 1;
    publish({ contactSaving: false, contactNotice: '' });
  }
  function stop() {
    generation += 1;
    contactGeneration += 1;
    current = empty();
    state.set(current);
  }
  async function refresh() {
    if (!options.enabled() || current.loading || current.saving) return;
    const epoch = generation;
    publish({ loading: true });
    try {
      const profile = await requestProfile();
      if (live(epoch)) publish({ profile });
    } catch {
      if (live(epoch))
        publish({
          profile: null,
          notice:
            'Your profile could not be loaded. Close settings and try again.',
        });
    } finally {
      if (epoch === generation) publish({ loading: false });
    }
  }
  async function save(value: LocalProfile) {
    if (
      !options.enabled() ||
      current.loading ||
      current.saving ||
      current.profile === null
    )
      return;
    const epoch = generation;
    publish({ saving: true, notice: '' });
    try {
      const profile = await saveProfile(value);
      if (live(epoch))
        publish({ profile, notice: 'Profile saved on this device.' });
    } catch {
      if (live(epoch))
        publish({
          notice:
            'Your profile could not be saved. Unlock storage and try again.',
        });
    } finally {
      if (epoch === generation) publish({ saving: false });
    }
  }
  async function saveName(contactID: string, name: string) {
    if (
      !options.enabled() ||
      current.contactSaving ||
      contactID === '' ||
      contactID !== options.selectedContact()
    )
      return;
    const epoch = generation;
    const contactEpoch = contactGeneration;
    const selected = () =>
      live(epoch) &&
      contactEpoch === contactGeneration &&
      contactID === options.selectedContact();
    publish({ contactSaving: true, contactNotice: '' });
    try {
      const result = await saveContactName(contactID, name);
      if (selected() && options.apply(result))
        publish({ contactNotice: 'Contact name saved only on this device.' });
    } catch {
      if (selected())
        publish({
          contactNotice:
            'The contact name could not be saved. Check the current contact and try again.',
        });
    } finally {
      if (selected()) publish({ contactSaving: false });
    }
  }
  return { state, refresh, save, saveName, resetContact, stop };
}
