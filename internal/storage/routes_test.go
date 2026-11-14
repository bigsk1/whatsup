package storage

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"tailscale.com/types/key"
	"testing"
	"time"
	"whatsup/internal/message"
	"whatsup/internal/relaychange"
	"whatsup/internal/sessionauth"
)

type routeFixture struct {
	a, b   *ApplicationState
	ra, rb PendingRecord
	ka, kb *memoryKeys
	da, db string
}

func makeRouteFixture(t *testing.T) routeFixture {
	t.Helper()
	f := routeFixture{da: privateDirectory(t), db: privateDirectory(t), ka: &memoryKeys{}, kb: &memoryKeys{}}
	_, f.a = openApplicationTest(t, f.da, f.ka)
	_, f.b = openApplicationTest(t, f.db, f.kb)
	ia, _ := f.a.Identity()
	ib, _ := f.b.Identity()
	f.ra = ongoingRecord(t, f.a, 61)
	client := key.NewNode()
	private, _ := client.MarshalText()
	defer clear(private)
	f.ra.Peer = ib.PublicKey()
	f.ra.PeerNode = client.Public().String()
	f.rb = f.ra
	f.rb.Role = "joiner"
	f.rb.Local, f.rb.Peer = ib.PublicKey(), ia.PublicKey()
	f.rb.LocalNodePrivate = string(private)
	f.rb.LocalNode, f.rb.PeerNode = f.ra.PeerNode, f.ra.LocalNode
	for i, app := range []*ApplicationState{f.a, f.b} {
		record := f.ra
		if i == 1 {
			record = f.rb
		}
		if err := app.SavePendingContact(record); err != nil {
			t.Fatal(err)
		}
		if _, err := app.CommitPendingContact(record); err != nil {
			t.Fatal(err)
		}
		pin := &identifiedPin{approvedPin: &approvedPin{local: record.Local, peer: record.Peer}, id: record.ID}
		if err := app.ApproveCommittedConversation(record, pin); err != nil {
			t.Fatal(err)
		}
		chat, err := app.ChatStore(pin)
		if err != nil {
			t.Fatal(err)
		}
		if err := chat.Queue(message.Message{ID: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(i + 80)}, 16)), Direction: message.Outgoing, Text: "synthetic retained route history", CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}
func prepareRouteChange(t *testing.T, f routeFixture) (RouteCandidate, RouteCandidate) {
	t.Helper()
	s, err := f.a.BeginRelayChange(f.ra.ID, "https://new-relay.example/map.json")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := f.a.PendingRelayIntent(f.ra.ID)
	if err != nil || !intent.NeedsRegion || intent.RequestID != s.RequestID {
		t.Fatal("intent not persisted")
	}
	if _, err := f.a.CompleteRelayProposal(f.ra.ID, s.RequestID, 901); err != nil {
		t.Fatal(err)
	}
	carry, err := f.a.ExportRelayChange(f.ra.ID)
	if err != nil {
		t.Fatal(err)
	}
	review, err := f.b.ImportRelayChange(f.rb.ID, carry)
	if err != nil || review.LocalApproved || !review.PeerApproved || !review.CanApprove {
		t.Fatal("import granted local consent")
	}
	for _, app := range []*ApplicationState{f.a, f.b} {
		plan, err := app.ConnectionPlan(f.ra.ID)
		if err != nil || len(plan.Candidates) != 1 || plan.Candidates[0].Epoch() != 0 {
			t.Fatal("unapproved candidate became eligible")
		}
	}
	if _, err := f.b.ApproveRelayChange(f.rb.ID, review.RequestID); err != nil {
		t.Fatal(err)
	}
	approval, err := f.b.ExportRelayChange(f.rb.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.ImportRelayChange(f.ra.ID, approval); err != nil {
		t.Fatal(err)
	}
	a, err := f.a.ConnectionPlan(f.ra.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.b.ConnectionPlan(f.rb.ID)
	if err != nil || len(a.Candidates) != 2 || len(b.Candidates) != 2 {
		t.Fatal("approved overlap missing")
	}
	if a.Candidates[0].Association() != b.Candidates[0].Association() || a.Candidates[0].Epoch() != 1 || a.Candidates[1].Epoch() != 0 {
		t.Fatal("reciprocal candidates disagree")
	}
	return a.Candidates[0], b.Candidates[0]
}
func routeSessions(t *testing.T, f routeFixture, a, b RouteCandidate) (*sessionauth.Session, *sessionauth.Session) {
	t.Helper()
	left, right := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	ia, _ := f.a.Identity()
	ib, _ := f.b.Identity()
	type result struct {
		s   *sessionauth.Session
		err error
	}
	done := make(chan result, 1)
	go func() {
		s, err := sessionauth.Authenticate(ctx, left, sessionauth.Options{Local: ia, Peer: ib.PublicKey(), ContactID: f.ra.ID, Association: a.Association(), Server: true})
		done <- result{s, err}
	}()
	sb, err := sessionauth.Authenticate(ctx, right, sessionauth.Options{Local: ib, Peer: ia.PublicKey(), ContactID: f.rb.ID, Association: b.Association()})
	ra := <-done
	if err != nil || ra.err != nil {
		t.Fatalf("pinned route TLS: %v %v", err, ra.err)
	}
	t.Cleanup(func() { ra.s.Close(); sb.Close() })
	return ra.s, sb
}
func TestRelayMigrationPreservesBaseAndLostCommitReconciles(t *testing.T) {
	f := makeRouteFixture(t)
	oldA, _ := f.a.ConnectionPlan(f.ra.ID)
	masterA := bytes.Clone(f.a.vault.master)
	a, b := prepareRouteChange(t, f)
	record, err := a.Record()
	if err != nil || record.LocalNodePrivate != f.ra.LocalNodePrivate || record.PresharedKey != f.ra.PresharedKey || record.LocalNode != f.ra.LocalNode || record.PeerNode != f.ra.PeerNode || record.RelayServiceURL == f.ra.RelayServiceURL {
		t.Fatal("candidate changed credentials")
	}
	sa, sb := routeSessions(t, f, a, b)
	pa, err := a.Authenticated(sa)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := b.Authenticated(sb)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticated(sb); err == nil {
		t.Fatal("reflected session proof accepted")
	}
	if _, err := f.a.NoteRouteAuthenticated(pa); err != nil {
		t.Fatal(err)
	}
	ca, err := f.a.CreateRelayCommit(pa)
	if err != nil || len(ca) > 512 {
		t.Fatal("local commit unavailable")
	}
	again, err := f.a.CreateRelayCommit(pa)
	if err != nil || !bytes.Equal(ca, again) {
		t.Fatal("commit retry changed signed bytes")
	}
	cb, err := f.b.CreateRelayCommit(pb)
	if err != nil {
		t.Fatal(err)
	}
	oldB, _ := f.b.ConnectionPlan(f.rb.ID)
	oldSA, oldSB := routeSessions(t, f, oldA.Candidates[0], oldB.Candidates[1])
	oldProof, err := oldA.Candidates[0].Authenticated(oldSA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.AcceptRelayCommit(oldProof, cb); err == nil {
		t.Fatal("peer signature on old route substituted for new-route TLS")
	}
	if _, err := f.a.CreateRelayCommit(oldProof); err == nil {
		t.Fatal("old-route TLS minted new-route commit")
	}
	oldSA.Close()
	oldSB.Close()
	if _, err := f.a.AcceptRelayCommit(pa, cb); err != nil {
		t.Fatal(err)
	}
	if oldA.Candidates[0].Validate() == nil {
		t.Fatal("old route retained authority after final")
	}
	if plan, _ := f.a.ConnectionPlan(f.ra.ID); len(plan.Candidates) != 1 || plan.Candidates[0].Epoch() != 1 {
		t.Fatal("old route not retired")
	}
	sa.Close()
	sb.Close()
	if pa.Validate() == nil {
		t.Fatal("closed TLS retained proof")
	}
	f.a.Close()
	f.b.Close()
	_, f.a = openApplicationTest(t, f.da, f.ka)
	_, f.b = openApplicationTest(t, f.db, f.kb)
	ap, _ := f.a.ConnectionPlan(f.ra.ID)
	bp, _ := f.b.ConnectionPlan(f.rb.ID)
	sa, sb = routeSessions(t, f, ap.Candidates[0], bp.Candidates[0])
	pa, _ = ap.Candidates[0].Authenticated(sa)
	pb, _ = bp.Candidates[0].Authenticated(sb)
	replay, err := f.a.CreateRelayCommit(pa)
	if err != nil || !bytes.Equal(replay, ca) {
		t.Fatal("lost final receipt not retained across restart")
	}
	if _, err := f.b.AcceptRelayCommit(pb, replay); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.AcceptRelayCommit(pb, replay); err != nil {
		t.Fatal("final receipt replay not idempotent")
	}
	for i, app := range []*ApplicationState{f.a, f.b} {
		records, err := app.CommittedContacts()
		expected := f.ra
		if i == 1 {
			expected = f.rb
		}
		if err != nil || len(records) != 1 || records[0] != expected || app.state.Contacts[0].StoreID != expected.ID {
			t.Fatal("migration changed immutable association/history scope")
		}
	}
	if !bytes.Equal(f.a.vault.master, masterA) {
		t.Fatal("migration changed master")
	}
}
func TestRelayCarryRejectsSubstitutionAndNeedsFreshLocalConsent(t *testing.T) {
	f := makeRouteFixture(t)
	s, err := f.a.BeginRelayChange(f.ra.ID, "https://new-relay.example/map.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.CompleteRelayProposal(f.ra.ID, s.RequestID, 901); err != nil {
		t.Fatal(err)
	}
	carry, _ := f.a.ExportRelayChange(f.ra.ID)
	signed, err := relaychange.Parse(carry)
	if err != nil {
		t.Fatal(err)
	}
	raw := signed.Bytes()
	for _, secret := range []string{f.ra.PresharedKey, f.ra.LocalNodePrivate, f.ra.ServerEndpoint} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("carry disclosed a transport credential")
		}
	}
	bad := relaychange.Clone(signed)
	bad.Payload[len(bad.Payload)-1] ^= 1
	text, _ := relaychange.Format(bad)
	if _, err := f.b.ImportRelayChange(f.rb.ID, text); err == nil {
		t.Fatal("unsigned change accepted")
	}
	if _, err := f.a.ImportRelayChange(f.ra.ID, carry); err == nil {
		t.Fatal("local signature imported as peer consent")
	}
	review, err := f.b.ImportRelayChange(f.rb.ID, carry)
	if err != nil {
		t.Fatal(err)
	}
	before := f.b.state.Routes[0].Revision
	if _, err := f.b.ImportRelayChange(f.rb.ID, carry); err != nil || f.b.state.Routes[0].Revision != before {
		t.Fatal("duplicate proposal changed state")
	}
	if _, err := f.b.DeclineRelayChange(f.rb.ID, review.RequestID); err != nil {
		t.Fatal(err)
	}
	decision, _ := f.b.ExportRelayChange(f.rb.ID)
	if _, err := f.a.ImportRelayChange(f.ra.ID, decision); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.ImportRelayChange(f.rb.ID, carry); err != nil {
		t.Fatal(err)
	}
	if plan, _ := f.b.ConnectionPlan(f.rb.ID); len(plan.Candidates) != 1 {
		t.Fatal("retired proposal restored consent")
	}
	if err := f.a.MarkPeerRemoved(f.ra, base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{99}, 16))); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.ConnectionPlan(f.ra.ID); err == nil {
		t.Fatal("removed peer gained connection plan")
	}
	if _, err := f.a.BeginRelayChange(f.ra.ID, "https://elsewhere.example/map"); err == nil {
		t.Fatal("removed peer admitted route change")
	}
}
func TestRelayCandidateProofWrongEpochAndFailedPersistence(t *testing.T) {
	f := makeRouteFixture(t)
	a, b := prepareRouteChange(t, f)
	sa, sb := routeSessions(t, f, a, b)
	proof, err := a.Authenticated(sa)
	if err != nil {
		t.Fatal(err)
	}
	foreign := RouteCandidate{state: &routeCandidateState{application: f.a, base: f.ra, route: a.state.route, epoch: 2, association: a.Association(), request: a.state.request}}
	if foreign.Validate() == nil {
		t.Fatal("foreign epoch admitted")
	}
	old := cloneRoute(f.a.state.Routes[0])
	f.a.save = func([]byte) error { return errors.New("synthetic route commit disk fault") }
	if _, err := f.a.NoteRouteAuthenticated(proof); err == nil {
		t.Fatal("failed durable note reported success")
	}
	if f.a.state.Routes[0].Pending.Authenticated || old.Pending.Authenticated {
		t.Fatal("failed write mutated shared pending state")
	}
	if a.Validate() == nil {
		t.Fatal("uncertain coordinator retained candidate")
	}
	sa.Close()
	sb.Close()
}

func TestRelayCandidateCannotReviveAfterExactRecordRecommit(t *testing.T) {
	f := makeRouteFixture(t)
	old, err := f.a.ConnectionPlan(f.ra.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.a.ForgetCommittedContact(f.ra, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := f.a.SavePendingContact(f.ra); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.CommitPendingContact(f.ra); err != nil {
		t.Fatal(err)
	}
	pin := &identifiedPin{approvedPin: &approvedPin{local: f.ra.Local, peer: f.ra.Peer}, id: f.ra.ID}
	if err := f.a.ApproveCommittedConversation(f.ra, pin); err != nil {
		t.Fatal(err)
	}
	fresh, err := f.a.ConnectionPlan(f.ra.ID)
	if err != nil || len(fresh.Candidates) != 1 {
		t.Fatal("fresh approved association unavailable")
	}
	if old.Candidates[0].Validate() == nil {
		t.Fatal("forgotten route capability revived after exact recommit")
	}
	if fresh.Candidates[0].Validate() != nil {
		t.Fatal("fresh route failed")
	}
}

func TestRelayDisplayHostMatchesCanonicalBrowserHost(t *testing.T) {
	for raw, want := range map[string]string{"https://Relay.Example:443/map": "relay.example", "https://Relay.Example:8443/map": "relay.example:8443", "https://[2001:4860::8888]:443/map": "[2001:4860::8888]"} {
		if relayDisplayHost(raw) != want {
			t.Fatal("display host mismatch")
		}
	}
}

func TestRelaySignedPeerContextSubstitutionsNeverAuthorizeDiscovery(t *testing.T) {
	f := makeRouteFixture(t)
	s, err := f.a.BeginRelayChange(f.ra.ID, "https://new-relay.example/map")
	if err != nil {
		t.Fatal(err)
	}
	f.a.CompleteRelayProposal(f.ra.ID, s.RequestID, 901)
	carry, _ := f.a.ExportRelayChange(f.ra.ID)
	signed, _ := relaychange.Parse(carry)
	original, _ := signed.Statement()
	signer, _ := f.a.Identity()
	for _, change := range []func(*relaychange.Statement){func(s *relaychange.Statement) { s.Base[0] ^= 1 }, func(s *relaychange.Statement) { s.Old[0] ^= 1 }, func(s *relaychange.Statement) { s.Epoch++ }, func(s *relaychange.Statement) {
		s.ContactID = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{29}, 16))
	}, func(s *relaychange.Statement) { s.ServiceURL = "https://127.0.0.1/map" }, func(s *relaychange.Statement) { s.Proposer = 2; s.Signer = 2 }} {
		statement := original
		change(&statement)
		bad, err := relaychange.Sign(signer, statement)
		if err != nil {
			t.Fatal(err)
		}
		text, _ := relaychange.Format(bad)
		if _, err := f.b.ImportRelayChange(f.rb.ID, text); err == nil {
			t.Fatal("signed but wrong-context proposal accepted")
		}
		if len(f.b.state.Routes) != 0 {
			t.Fatal("rejected proposal persisted authority")
		}
	}
}

func TestRelayPeerAbortBeforeItsCommitReleasesBothCandidateSets(t *testing.T) {
	f := makeRouteFixture(t)
	a, b := prepareRouteChange(t, f)
	sa, sb := routeSessions(t, f, a, b)
	pa, _ := a.Authenticated(sa)
	pb, _ := b.Authenticated(sb)
	commit, err := f.a.CreateRelayCommit(pa)
	if err != nil {
		t.Fatal(err)
	}
	request := a.RequestID()
	if _, err := f.a.CancelRelayChange(f.ra.ID, request); err == nil {
		t.Fatal("local committed vote was revoked")
	}
	if _, err := f.b.CancelRelayChange(f.rb.ID, request); err != nil {
		t.Fatal(err)
	}
	cancel, _ := f.b.ExportRelayChange(f.rb.ID)
	if _, err := f.a.ImportRelayChange(f.ra.ID, cancel); err != nil {
		t.Fatal("peer's pre-commit abort was ignored", err)
	}
	if _, err := f.b.AcceptRelayCommit(pb, commit); err == nil {
		t.Fatal("delayed vote completed a cancelled migration")
	}
	for _, app := range []*ApplicationState{f.a, f.b} {
		plan, err := app.ConnectionPlan(f.ra.ID)
		if err != nil || len(plan.Candidates) != 1 || plan.Candidates[0].Epoch() != 0 {
			t.Fatal("abort left conflicting route candidates")
		}
	}
}

func TestRelaySelectingCurrentRouteClearsUnsignedIntent(t *testing.T) {
	f := makeRouteFixture(t)
	s, err := f.a.BeginRelayChange(f.ra.ID, f.ra.RelayServiceURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.CompleteRelayProposal(f.ra.ID, s.RequestID, 900); err != nil {
		t.Fatal(err)
	}
	intent, err := f.a.PendingRelayIntent(f.ra.ID)
	if err != nil || intent.NeedsRegion || intent.RequestID != "" {
		t.Fatal("no-op route kept retrying map fetch")
	}
}
