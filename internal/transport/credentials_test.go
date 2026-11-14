package transport

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"tailscale.com/types/key"

	"whatsup/internal/pairing"
)

func TestLocalApprovalCannotActivateContactTransport(t *testing.T) {
	credentials := testCredentials(t)
	options := ServerOptions{
		Credentials: credentials, ApprovedPeer: PeerKey{node: key.NewNode().Public()},
		Policy: RelayFallback, AllowRelayFallback: true, DERPMapURL: "https://relay.example.test/derpmap.json",
	}
	if server, err := NewContactServer(options); !errors.Is(err, ErrMutualApprovalUnavailable) || server != nil {
		t.Fatal("local approval was treated as completed reciprocal authentication")
	}
	if credentials.state.claimed {
		t.Fatal("failed activation consumed prepared credentials")
	}
	for _, mutate := range []func(*verifiedBinding){
		func(binding *verifiedBinding) { binding.localIdentity = binding.peerIdentity },
		func(binding *verifiedBinding) { binding.peerIdentity = binding.localIdentity },
		func(binding *verifiedBinding) { binding.localNode = key.NewNode().Public() },
		func(binding *verifiedBinding) { binding.peerNode = key.NewNode().Public() },
	} {
		bindSyntheticCredentials(credentials, options.ApprovedPeer)
		mutate(credentials.state.binding)
		if server, err := NewContactServer(options); !errors.Is(err, ErrMutualApprovalUnavailable) || server != nil {
			t.Fatal("substituted identity-to-node binding activated a transport")
		}
	}
}

func TestOnePreparedBundleInstallsTheExactApprovedPSK(t *testing.T) {
	options := testOptions(t)
	preparedPSK := options.Credentials.state.psk
	preparedNode := options.Credentials.PeerKey()
	server, err := NewContactServer(options)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if !server.server.PresharedKey.Equal(preparedPSK) || server.server.Key.Public() != preparedNode.node {
		t.Fatal("adapter replaced the prepared credential bundle")
	}
	if server.server.DisablePresharedKey || server.server.PresharedKey.IsZero() {
		t.Fatal("adapter weakened the contact's PSK")
	}
}

func TestPreparedCredentialsCannotBeDuplicatedOrRevived(t *testing.T) {
	approval := testApproval(t)
	var prepared atomic.Int32
	var credential *ContactCredentials
	var credentialMu sync.Mutex
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			candidate, err := NewContactCredentials(approval)
			if err == nil {
				prepared.Add(1)
				credentialMu.Lock()
				credential = candidate
				credentialMu.Unlock()
			} else if !errors.Is(err, ErrInvalidCredentials) {
				t.Error("unexpected credential provisioning result")
			}
		})
	}
	wg.Wait()
	if prepared.Load() != 1 {
		t.Fatal("one approval generated unrelated ongoing credential sets")
	}
	copyOfCredentials := *credential
	credential.Revoke()
	if !copyOfCredentials.PeerKey().node.IsZero() || !credential.state.psk.IsZero() || !credential.state.node.IsZero() {
		t.Fatal("revocation failed to erase the shared bundle")
	}
	if _, err := NewContactCredentials(approval); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("revoked credentials were replaced under the same local approval")
	}
	for _, invalid := range []*pairing.Contact{nil, {}, testApproval(t)} {
		if invalid != nil {
			invalid.Revoke()
		}
		if _, err := NewContactCredentials(invalid); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatal("missing or revoked approval generated credentials")
		}
	}
}

func TestTransportPrivacyGatePrecedesAllNetworking(t *testing.T) {
	options := testOptions(t)
	server, err := NewContactServer(options)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	for range 2 {
		if err := server.Start(context.Background()); !errors.Is(err, ErrPrivacyUnavailable) {
			t.Fatal("unreviewed DERP statistics label reached the networking path")
		}
		if server.started || server.listener != nil || server.closed {
			t.Fatal("privacy failure started or changed the transport lifecycle")
		}
	}
}

func TestApprovalRevocationRejectsPreparedAdmission(t *testing.T) {
	options := testOptions(t)
	server, err := NewContactServer(options)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	options.Credentials.state.approval.Revoke()
	if server.admits(options.ApprovedPeer.node) || !options.Credentials.PeerKey().node.IsZero() {
		t.Fatal("revoked local approval retained transport admission")
	}
}
