package chat

import (
	"math"

	"whatsup/internal/message"
)

func (c *Conversation) ReadPage(before uint64, limit int) (page message.Page, err error) {
	if limit < 1 || limit > MaxPageMessages || before > math.MaxInt64 {
		return page, ErrPage
	}
	err = c.withPeer(c.peer(), func() error {
		repository, ok := c.store.(message.PagedRepository)
		if !ok {
			return ErrPage
		}
		stored, err := repository.ReadPage(before, limit)
		if err != nil {
			return c.repositoryError(err)
		}
		if len(stored.Messages) > limit || stored.NextCursor > math.MaxInt64 || !stored.HasMore && stored.NextCursor != 0 || stored.HasMore && (stored.NextCursor == 0 || before != 0 && stored.NextCursor >= before) {
			c.failed = true
			return ErrCorrupt
		}
		seen := make(map[string]bool, len(stored.Messages))
		var previous uint64
		total := 0
		for _, item := range stored.Messages {
			if validateStored(item) != nil || seen[item.ID] || item.Sequence <= previous || item.Sequence > math.MaxInt64 || before != 0 && item.Sequence >= before || stored.HasMore && item.Sequence < stored.NextCursor {
				c.failed = true
				return ErrCorrupt
			}
			seen[item.ID] = true
			previous = item.Sequence
			total += len(item.Text)
		}
		if total > MaxPageTextBytes {
			c.failed = true
			return ErrCorrupt
		}
		stored.Messages = append([]Message{}, stored.Messages...)
		page = stored
		return nil
	})
	return page, err
}

func (c *Conversation) UnreadCount() (count uint64, err error) {
	err = c.withPeer(c.peer(), func() error {
		repository, ok := c.store.(message.PagedRepository)
		if !ok {
			return ErrPage
		}
		var err error
		count, err = repository.UnreadCount()
		return c.repositoryError(err)
	})
	return count, err
}

func (c *Conversation) MarkRead(throughMessageID string) error {
	if !validID(throughMessageID) {
		return ErrFrame
	}
	return c.withPeer(c.peer(), func() error {
		repository, ok := c.store.(message.PagedRepository)
		if !ok {
			return ErrPage
		}
		return c.repositoryError(repository.MarkRead(throughMessageID))
	})
}
