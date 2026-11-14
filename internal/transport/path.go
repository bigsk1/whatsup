package transport

import (
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"
)

// Path is a display-only observation. It cannot enable direct-only policy or
// authorize an application packet. No address or relay identity leaves Go.
type Path string

const (
	PathUnknown Path = "Unknown"
	PathDirect  Path = "Direct"
	PathRelayed Path = "Relayed"
)

func peerPath(status *ipnstate.Status, approved key.NodePublic) Path {
	if status == nil || approved.IsZero() {
		return PathUnknown
	}
	peer := status.Peer[approved]
	if peer == nil || peer.PublicKey != approved || !peer.Active {
		return PathUnknown
	}
	// A relay region is configured even when direct traffic succeeds. A live
	// peer-relay path takes precedence; CurAddr identifies ordinary direct UDP.
	if peer.PeerRelay != "" {
		return PathRelayed
	}
	if peer.CurAddr != "" {
		return PathDirect
	}
	if peer.Relay != "" {
		return PathRelayed
	}
	return PathUnknown
}

func (c *ongoingServerConn) Path() Path {
	if c == nil || c.owner == nil || c.owner.state == nil {
		return PathUnknown
	}
	state := c.owner.state
	state.mu.Lock()
	if state.closed || state.active != c {
		state.mu.Unlock()
		return PathUnknown
	}
	server, peer := state.server, state.peer
	state.mu.Unlock()
	if server == nil || peer.IsZero() {
		return PathUnknown
	}
	result := peerPath(server.Status(), peer)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.active != c {
		return PathUnknown
	}
	return result
}

func (c *ongoingClientConn) Path() Path {
	if c == nil {
		return PathUnknown
	}
	c.mu.Lock()
	if c.closed || c.client == nil {
		c.mu.Unlock()
		return PathUnknown
	}
	client, peer := c.client, c.peer
	c.mu.Unlock()
	result := peerPath(client.Status(), peer)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return PathUnknown
	}
	return result
}
