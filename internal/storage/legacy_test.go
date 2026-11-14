package storage

func legacySnapshotForTest(state applicationEnvelope) applicationEnvelopeV4 {
	old := applicationEnvelopeV4{Version: 4, Identity: state.Identity, Contacts: []contactSectionV1{}, PendingContacts: []pendingRecordDiskV1{}, CommittedContacts: []pendingRecordDiskV1{}}
	for _, c := range state.Contacts {
		old.Contacts = append(old.Contacts, contactSectionV1{Version: c.Version, Local: c.Local, Peer: c.Peer, Chat: c.Chat})
	}
	convert := func(records []pendingRecordDisk) []pendingRecordDiskV1 {
		result := []pendingRecordDiskV1{}
		for _, r := range records {
			result = append(result, pendingRecordDiskV1{Version: r.Version, ID: r.ID, Role: r.Role, Local: r.Local, Peer: r.Peer, LocalNodePrivate: r.LocalNodePrivate, LocalNode: r.LocalNode, PeerNode: r.PeerNode, PresharedKey: r.PresharedKey})
		}
		return result
	}
	old.PendingContacts, old.CommittedContacts = convert(state.PendingContacts), convert(state.CommittedContacts)
	return old
}
