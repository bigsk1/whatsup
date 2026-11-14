package channel

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestPairingTLSContextCannotBeOmittedChangedOrDowngraded(t *testing.T) {
	for _, mode := range []string{"match", "omitted-client", "omitted-server", "changed"} {
		t.Run(mode, func(t *testing.T) {
			owner, candidate := device(t), device(t)
			id := [16]byte{1}
			binding := [32]byte{17, 44, 121}
			serverBinding, clientBinding := binding, binding
			switch mode {
			case "omitted-client":
				clientBinding = [32]byte{}
			case "omitted-server":
				serverBinding = [32]byte{}
			case "changed":
				clientBinding[31] ^= 1
			}
			left, right := net.Pipe()
			defer left.Close()
			defer right.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			expires := time.Now().Add(time.Minute)
			done := make(chan sessionResult, 1)
			go func() {
				session, err := NewServer(ctx, left, owner, id, expires, serverBinding)
				done <- sessionResult{session, err}
			}()
			client, clientErr := NewClient(ctx, right, candidate, owner.PublicKey(), id, expires, clientBinding)
			server := <-done
			if client != nil {
				defer client.Close()
			}
			if server.session != nil {
				defer server.session.Close()
			}
			if mode != "match" {
				if clientErr == nil || server.err == nil {
					t.Fatal("TLS accepted missing, changed or downgraded invitation context")
				}
				return
			}
			if clientErr != nil || server.err != nil || client.ContextBinding() != binding || server.session.ContextBinding() != binding {
				t.Fatal("matching invitation context did not authenticate")
			}
			state := client.state.tls.ConnectionState()
			if state.NegotiatedProtocol != invitationProtocol(id, binding) || state.NegotiatedProtocol == invitationProtocol(id) {
				t.Fatal("v2 session negotiated a legacy fallback")
			}
		})
	}
}
