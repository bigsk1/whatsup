import type { ConversationMessage } from '../messaging';
import type { AttachmentItem } from './attachments';

export const inlineMediaItem = (item: AttachmentItem) =>
  item.state === 'complete' &&
  [
    'image/png',
    'image/jpeg',
    'image/webp',
    'image/gif',
    'video/mp4',
    'video/webm',
  ].includes(item.mediaType);
export type TimelineEntry =
  | { key: string; kind: 'message'; message: ConversationMessage }
  | { key: string; kind: 'attachment'; attachment: AttachmentItem };

// These are local record times. Older attachments without a stored timestamp
// remain visible at the end, labelled as saved media rather than given a date.
export function conversationTimeline(
  messages: ConversationMessage[],
  attachments: AttachmentItem[],
): TimelineEntry[] {
  const entries = [
    ...messages.map((message, index) => ({
      time: message.createdAt,
      index,
      entry: {
        key: 'message:' + message.id,
        kind: 'message' as const,
        message,
      },
    })),
    ...attachments.filter(inlineMediaItem).map((attachment, index) => ({
      time: attachment.recordedAt ?? '',
      index: messages.length + index,
      entry: {
        key: 'attachment:' + attachment.id,
        kind: 'attachment' as const,
        attachment,
      },
    })),
  ];
  const order = (time: string) =>
    time ? Date.parse(time) : Number.MAX_SAFE_INTEGER;
  return entries
    .sort(
      (left, right) =>
        order(left.time) - order(right.time) || left.index - right.index,
    )
    .map(({ entry }) => entry);
}
