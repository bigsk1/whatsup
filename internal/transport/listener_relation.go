package transport

import (
	"reflect"
	"strings"

	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
)

// OngoingListenerRelation compares only two already approved and validated
// transports. It performs no network IO and grants no session authority. Exact
// equivalent regions may share a WireGuard listener; overlapping regions must
// not register the same node key in two engines at the same DERP destination.
func OngoingListenerRelation(first, second OngoingOptions) (equivalent, overlap bool, err error) {
	a, err := ongoingOptions(first)
	if err != nil {
		return false, false, err
	}
	b, err := ongoingOptions(second)
	if err != nil {
		return false, false, err
	}
	if first.Record.Role != "inviter" || second.Record.Role != "inviter" ||
		first.Record.LocalNodePrivate != second.Record.LocalNodePrivate ||
		first.Record.PeerNode != second.Record.PeerNode || a.psk != b.psk {
		return false, false, ErrOngoingTransport
	}
	ai, err := tailcat.ParseAddr(a.endpoint.EncodeForTransport())
	if err != nil || first.Profile.state.regions[ai.RegionID] == nil {
		return false, false, ErrOngoingTransport
	}
	bi, err := tailcat.ParseAddr(b.endpoint.EncodeForTransport())
	if err != nil || second.Profile.state.regions[bi.RegionID] == nil {
		return false, false, ErrOngoingTransport
	}
	ar, br := listenerRegion(first.Profile.state.regions[ai.RegionID]), listenerRegion(second.Profile.state.regions[bi.RegionID])
	equivalent = reflect.DeepEqual(ar, br)
	for _, an := range ar.Nodes {
		for _, bn := range br.Nodes {
			if an == nil || bn == nil || an.STUNOnly || bn.STUNOnly || an.DERPPort != bn.DERPPort {
				continue
			}
			if an.HostName == bn.HostName ||
				an.IPv4 != "" && an.IPv4 != "none" && an.IPv4 == bn.IPv4 ||
				an.IPv6 != "" && an.IPv6 != "none" && an.IPv6 == bn.IPv6 {
				overlap = true
			}
		}
	}
	return equivalent, overlap, nil
}

func listenerRegion(region *tailcfg.DERPRegion) *tailcfg.DERPRegion {
	copy := cloneRelayRegion(region)
	// Presentation names and coordinates do not identify a relay connection.
	// Region labels, endpoints, certificates, ordering and policy remain exact.
	copy.RegionCode, copy.RegionName = "", ""
	copy.Latitude, copy.Longitude = 0, 0
	for _, node := range copy.Nodes {
		if node == nil {
			continue
		}
		node.Name = ""
		node.HostName = strings.ToLower(node.HostName)
		if node.CertName == "" {
			node.CertName = node.HostName
		}
		if node.DERPPort == 0 {
			node.DERPPort = 443
		}
		if node.STUNPort == 0 {
			node.STUNPort = 3478
		}
	}
	return copy
}
