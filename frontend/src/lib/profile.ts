import { hasDesktopBridge } from './backend';
import { validDisplayName } from './names';
export { validDisplayName } from './names';
import { validateConversation, type ConversationSnapshot } from './messaging';

export interface LocalProfile {
  displayName: string;
  shareDisplayName: boolean;
}

export function validateProfile(value: unknown): LocalProfile {
  const invalid = () => {
    throw new Error('The desktop returned an invalid profile.');
  };
  if (!value || typeof value !== 'object' || Array.isArray(value))
    return invalid();
  const record = value as Record<string, unknown>;
  if (
    Object.keys(record).length !== 2 ||
    typeof record.displayName !== 'string' ||
    !validDisplayName(record.displayName) ||
    typeof record.shareDisplayName !== 'boolean'
  )
    return invalid();
  return {
    displayName: record.displayName,
    shareDisplayName: record.shareDisplayName,
  };
}

async function service() {
  if (!hasDesktopBridge())
    throw new Error('Open the desktop app to use profiles.');
  return import('../../bindings/whatsup/internal/desktop/service');
}

export async function requestProfile(): Promise<LocalProfile> {
  try {
    return validateProfile(await (await service()).GetProfile());
  } catch {
    throw new Error('Your profile could not be loaded.');
  }
}

export async function saveProfile(value: LocalProfile): Promise<LocalProfile> {
  const profile = validateProfile(value);
  try {
    const result = validateProfile(
      await (
        await service()
      ).SetProfile(profile.displayName, profile.shareDisplayName),
    );
    if (
      result.displayName !== profile.displayName ||
      result.shareDisplayName !== profile.shareDisplayName
    )
      throw new Error('Profile save could not be confirmed.');
    return result;
  } catch {
    throw new Error('Your profile could not be saved.');
  }
}

export async function saveContactName(
  contactID: string,
  localName: string,
): Promise<ConversationSnapshot> {
  if (
    !/^[A-Za-z0-9_-]{21}[AQgw]$/.test(contactID) ||
    contactID === 'A'.repeat(22) ||
    !validDisplayName(localName)
  )
    throw new Error('Enter a name within the supported limit.');
  try {
    const value = validateConversation(
      await (await service()).SetContactName(contactID, localName),
    );
    if (value.contactID !== contactID || value.localName !== localName)
      throw new Error('Contact name could not be confirmed.');
    return value;
  } catch {
    throw new Error('The contact name could not be saved.');
  }
}
