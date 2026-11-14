package relaychange

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"whatsup/internal/identity"
)

func fixture(t *testing.T) (*identity.Identity, Statement) {
	t.Helper()
	local, err := identity.FromSeed(bytes.Repeat([]byte{41}, 32))
	if err != nil {
		t.Fatal(err)
	}
	id := func(n byte) string { return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{n}, 16)) }
	return local, Statement{Kind: Proposal, ContactID: id(1), Base: [32]byte{1}, Old: [32]byte{2}, Epoch: 1, Nonce: id(3), Proposer: 1, Signer: 1, ServiceURL: "https://relay.example/map.json", RegionID: 900}
}
func TestSignedRelayCarryCanonicalAndBounded(t *testing.T) {
	local, statement := fixture(t)
	statement.ServiceURL = "https://relay.example/" + strings.Repeat("p", 2048-len("https://relay.example/"))
	signed, err := Sign(local, statement)
	if err != nil {
		t.Fatal(err)
	}
	text, err := Format(signed)
	if err != nil || len(text) >= 3200 || len(signed.Bytes()) > MaxWire {
		t.Fatal("maximum URL exceeded control bound")
	}
	parsed, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	got, err := parsed.Verify(local.PublicKey())
	if err != nil || got != statement {
		t.Fatal("canonical carry changed")
	}
	for _, bad := range []string{text + "=", text + "\n", " " + text, text + "A", Prefix + strings.Repeat("a", MaxText)} {
		if _, err := Parse(bad); err == nil {
			t.Fatal("noncanonical or oversized carry accepted")
		}
	}
	other, _ := identity.FromSeed(bytes.Repeat([]byte{42}, 32))
	if _, err := parsed.Verify(other.PublicKey()); err == nil {
		t.Fatal("wrong identity verified")
	}
	changed := Clone(parsed)
	changed.Payload[len(changed.Payload)-1] ^= 1
	if _, err := changed.Verify(local.PublicKey()); err == nil {
		t.Fatal("changed region verified")
	}
	if _, err := ParseBytes(append(signed.Bytes(), 0)); err == nil {
		t.Fatal("trailing wire data accepted")
	}
}
func TestDecisionDomainsAndCommitBound(t *testing.T) {
	local, p := fixture(t)
	proposal, err := Sign(local, p)
	if err != nil {
		t.Fatal(err)
	}
	peer, _ := identity.FromSeed(bytes.Repeat([]byte{43}, 32))
	decision := p
	decision.ServiceURL, decision.RegionID = "", 0
	decision.Kind = Approval
	decision.Signer = 2
	decision.ProposalHash = proposal.Digest()
	approval, err := Sign(peer, decision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := approval.Verify(peer.PublicKey()); err != nil {
		t.Fatal(err)
	}
	decision.Kind = Commit
	decision.Association = [32]byte{7}
	commit, err := Sign(peer, decision)
	if err != nil || len(commit.Bytes()) > 512 {
		t.Fatal("commit bound")
	}
	changed := Clone(approval)
	changed.Payload[1] = Commit
	if _, err := changed.Verify(peer.PublicKey()); err == nil {
		t.Fatal("approval reflected as commit")
	}
	decision.Kind = Decline
	if _, err := Sign(peer, decision); err == nil {
		t.Fatal("commit association smuggled into decision")
	}
}
func FuzzRelayCarryParser(f *testing.F) {
	f.Add("whatsup-relay:v1:")
	f.Add("not-a-route")
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > MaxText+1 {
			return
		}
		s, err := Parse(text)
		if err == nil {
			round, err := Format(s)
			if err != nil || round != text {
				t.Fatal("parser lost canonical bytes")
			}
		}
	})
}
