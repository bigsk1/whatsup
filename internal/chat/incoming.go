package chat

import "whatsup/internal/message"

// GetVisibleIncoming is a backend-only notification lookup guarded by the same
// exact local approval as history. Hidden incoming tombstones and outgoing text
// cannot become a preview; there is no filesystem or frontend lookup authority.
func (c *Conversation) GetVisibleIncoming(id string) (item Message, err error) {
	if !validID(id) {
		return Message{}, ErrFrame
	}
	err = c.withPeer(c.peer(), func() error {
		stored, err := c.store.Get(id)
		if err != nil {
			return c.repositoryError(err)
		}
		if stored.ID != id || validateStored(stored) != nil {
			c.failed = true
			return ErrCorrupt
		}
		if stored.Direction != Incoming {
			return message.ErrNotFound
		}
		item = stored
		return nil
	})
	if err != nil {
		return Message{}, err
	}
	return item, nil
}
