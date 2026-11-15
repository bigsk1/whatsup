import { describe, expect, it } from 'vitest';
import { conversationTimeline, inlineMediaItem } from './timeline';
import type { ConversationMessage } from '../messaging';
import type { AttachmentItem } from './attachments';

const message = (id: string, createdAt: string): ConversationMessage => ({
  id,
  createdAt,
  direction: 'outgoing',
  text: 'Synthetic',
  delivered: true,
});
const attachment = (id: string, recordedAt?: string): AttachmentItem => ({
  id,
  recordedAt,
  name: 'Synthetic image.png',
  size: 10,
  mediaType: 'image/png',
  direction: 'incoming',
  state: 'complete',
  transferredBytes: 10,
  previewURL: '',
  warning: '',
});
describe('local conversation media timeline', () => {
  it('interleaves complete images/video with messages using local stored times', () => {
    const entries = conversationTimeline(
      [
        message('a', '2026-11-14T12:00:00Z'),
        message('c', '2026-11-14T12:02:00Z'),
      ],
      [attachment('b', '2026-11-14T12:01:00Z')],
    );
    expect(entries.map((entry) => entry.key)).toEqual([
      'message:a',
      'attachment:b',
      'message:c',
    ]);
  });
  it('preserves message order for equal times and never invents legacy attachment dates', () => {
    const entries = conversationTimeline(
      [
        message('z', '2026-11-14T12:00:00Z'),
        message('a', '2026-11-14T12:00:00Z'),
      ],
      [attachment('legacy')],
    );
    expect(entries.map((entry) => entry.key)).toEqual([
      'message:z',
      'message:a',
      'attachment:legacy',
    ]);
    expect(
      entries[2].kind === 'attachment' && entries[2].attachment.recordedAt,
    ).toBeUndefined();
  });
  it('never previews unfinished, original-file-mode or active content', () => {
    for (const update of [
      { state: 'offered' },
      { state: 'receiving' },
      { mediaType: 'application/octet-stream' },
      { mediaType: 'image/svg+xml' },
      { mediaType: 'text/html' },
    ]) {
      const item = { ...attachment('x'), ...update };
      expect(inlineMediaItem(item)).toBe(false);
      expect(conversationTimeline([], [item])).toEqual([]);
    }
    expect(
      inlineMediaItem({ ...attachment('x'), mediaType: 'video/mp4' }),
    ).toBe(true);
  });
});
