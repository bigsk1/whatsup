package pairing

import (
	"whatsup/internal/sessionauth"
	"whatsup/internal/storage"
)

// RestoreLocalContact restores only previously durable LOCAL approval for
// history and encrypted offline queueing. It proves no live network session.
// The receipt guard is used only for construction, never around ChatStore IO.
func RestoreLocalContact(receipt *storage.LocalContactReceipt) (*Contact, error) {
	if receipt == nil {
		return nil, ErrPeer
	}
	var contact *Contact
	err := receipt.WithLocalApproval(func() error {
		var err error
		contact, err = newContact(receipt.Local(), receipt.Peer())
		if err == nil {
			contact.state.contactID = receipt.ContactID()
		}
		return err
	})
	if err != nil {
		return nil, ErrPeer
	}
	return contact, nil
}

// ReconfirmAuthenticated uses a fresh automatic mutual identity proof for the
// exact committed credentials. No new invitation or user fingerprint prompt is
// required. A caller separately persists local approval and owns network IO.
func ReconfirmAuthenticated(session *sessionauth.Session, record storage.PendingRecord) (*Contact, error) {
	binding, err := storage.ContactBinding(record)
	if err != nil || session == nil || !session.Matches(record.Local, record.Peer, record.ID, binding) {
		return nil, ErrPeer
	}
	var contact *Contact
	err = session.WithLivePeer(record.Peer, func() error {
		var createErr error
		contact, createErr = newContact(record.Local, record.Peer)
		if createErr == nil {
			contact.state.contactID = record.ID
		}
		return createErr
	})
	if err != nil {
		return nil, ErrPeer
	}
	return contact, nil
}
