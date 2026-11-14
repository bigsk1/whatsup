package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"

	"whatsup/internal/transport/address"
)

func testRelayModel() *tailcfg.DERPMap {
	return &tailcfg.DERPMap{Regions: map[tailcfg.DERPRegionID]*tailcfg.DERPRegion{900: {
		RegionID: 900, RegionCode: "synthetic", RegionName: "Synthetic relay",
		Nodes: []*tailcfg.DERPNode{{Name: "synthetic-node", RegionID: 900, HostName: "relay.example", CanPort80: true}},
	}}}
}

func syntheticResolver(_ context.Context, host string) ([]netip.Addr, error) {
	if host != "relay.example" {
		return nil, ErrRelayProfile
	}
	return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
}

func reviewedTestProfile(t *testing.T) *RelayProfile {
	t.Helper()
	profile, err := reviewedRelayProfile(context.Background(), "https://operator.example/derp.json", testRelayModel(), syntheticResolver)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func TestRelayProfileReuseRequiresExactServiceAndOriginalUnexpiredLifetime(t *testing.T) {
	var absent *RelayProfile
	if absent.ReadyFor("https://operator.example/derp.json") || (&RelayProfile{}).ReadyFor("https://operator.example/derp.json") {
		t.Fatal("missing or unreviewed relay configuration became reusable")
	}
	profile := reviewedTestProfile(t)
	deadline := profile.state.expires
	for range 4 {
		if !profile.ReadyFor(profile.state.source) || profile.state.expires != deadline {
			t.Fatal("approved profile was not reusable or reuse extended its deadline")
		}
	}
	for _, changed := range []string{"https://another.example/derp.json", "https://operator.example/other.json", ""} {
		if profile.ReadyFor(changed) {
			t.Fatal("cached profile permitted a service substitution")
		}
	}
	profile.state.expires = time.Now().Add(-time.Second)
	if profile.ReadyFor(profile.state.source) {
		t.Fatal("expired profile was reused")
	}
}

func syntheticInvitationEndpoint(t *testing.T, region *tailcfg.DERPRegion, id tailcfg.DERPRegionID) address.Endpoint {
	t.Helper()
	ci := tailcat.ConnInfo{
		ServerPublic:      tailcat.NodePublic{NodePublic: key.NewNode().Public()},
		ServerDiscoPublic: tailcat.DiscoPublic{DiscoPublic: key.NewDisco().Public()},
		PresharedKey:      tailcat.NewPresharedKey(), RegionID: id,
	}
	if region != nil {
		ci.Region = []*tailcfg.DERPRegion{region}
		ci.RegionID = 0
	}
	endpoint, err := address.Parse(string(ci.Addr()))
	if err != nil {
		t.Fatal("invalid synthetic endpoint")
	}
	return endpoint
}

func TestRelaySourcePolicyRejectsLocalDestinationsBeforeHTTP(t *testing.T) {
	for _, input := range []string{
		"", "http://relay.example/map", "https://user:password@relay.example/map",
		"https://relay.example/map?token=secret", "https://relay.example/map#fragment",
		"https://relay.example:0/map", "https://relay.example:65536/map",
		"https://relay.example:0443/map", "https://relay.example./map",
		"https://localhost/map", "https://127.0.0.1/map", "https://10.0.0.1/map",
		"https://192.168.0.1/map", "https://169.254.169.254/map", "https://100.64.0.1/map",
		"https://[::1]/map", "https://[fd00::1]/map", "https://[::ffff:8.8.8.8]/map",
	} {
		if _, err := relayMapURL(input); err == nil {
			t.Fatal("unsafe relay source accepted")
		}
	}
	for _, input := range []string{"https://operator.example/derp.json", "https://operator.example:8443/derp.json", "https://8.8.8.8/map"} {
		if _, err := relayMapURL(input); err != nil {
			t.Fatal("valid operator URL rejected")
		}
	}
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	if _, err := FetchRelayProfile(context.Background(), server.URL); err == nil || requests.Load() != 0 {
		t.Fatal("map fetch contacted a local address")
	}
	if _, err := FetchRelayProfile(nil, "https://operator.example/map"); err == nil {
		t.Fatal("nil context accepted")
	}
}

func TestRelayAddressPolicyIncludesNonPrivateReservedNetworks(t *testing.T) {
	for _, value := range []string{"0.0.0.0", "127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.0.1", "169.254.1.2", "100.100.100.100", "192.0.0.1", "192.0.2.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "255.255.255.255", "::", "::1", "fd00::1", "fe80::1", "fe80::1%eth0", "::ffff:8.8.8.8", "64:ff9b::808:808", "2001:db8::1", "2001::1", "2002:0808:0808::1"} {
		if publicRelayIP(netip.MustParseAddr(value)) {
			t.Fatal("nonpublic relay address accepted")
		}
	}
	for _, value := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !publicRelayIP(netip.MustParseAddr(value)) {
			t.Fatal("public relay address rejected")
		}
	}
}

func TestReviewedProfilePinsAddressesAndCannotBeMutatedThroughInput(t *testing.T) {
	model := testRelayModel()
	profile, err := reviewedRelayProfile(context.Background(), "https://operator.example/map", model, syntheticResolver)
	if err != nil || !profile.Ready() {
		t.Fatal("synthetic public profile failed")
	}
	model.Regions[900].Nodes[0].HostName = "mutated.example"
	node := profile.state.regions[900].Nodes[0]
	if node.HostName != "relay.example" || node.IPv4 != "8.8.8.8" || node.IPv6 != "none" || node.CanPort80 {
		t.Fatal("profile failed to pin and isolate its network destinations")
	}
	for _, item := range []any{profile, *profile} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if fmt.Sprintf(format, item) != "[private WhatsUp relay configuration]" {
				t.Fatal("profile formatting exposed operator configuration")
			}
		}
		if _, err := json.Marshal(item); err == nil {
			t.Fatal("profile serialized into a DTO")
		}
	}
	profile.state.expires = time.Now().Add(-time.Second)
	if profile.Ready() {
		t.Fatal("expired operator profile remained usable")
	}
	if (&RelayProfile{}).Ready() || (*RelayProfile)(nil).Ready() {
		t.Fatal("missing operator profile selected a default")
	}
}

func TestReviewedProfileRefusesMixedPrivateDNSAndUnsafeMetadata(t *testing.T) {
	badDNS := func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}, nil
	}
	if _, err := reviewedRelayProfile(context.Background(), "https://operator.example/map", testRelayModel(), badDNS); err == nil {
		t.Fatal("mixed public/private DNS was accepted")
	}
	changes := []func(*tailcfg.DERPMap){
		func(m *tailcfg.DERPMap) { m.Regions[900] = nil },
		func(m *tailcfg.DERPMap) { m.Regions[900].RegionID = 901 },
		func(m *tailcfg.DERPMap) { m.Regions[900].Nodes = nil },
		func(m *tailcfg.DERPMap) { m.Regions[900].Nodes[0] = nil },
		func(m *tailcfg.DERPMap) { m.Regions[900].Nodes[0].RegionID = 901 },
		func(m *tailcfg.DERPMap) { m.Regions[900].Nodes[0].InsecureForTests = true },
		func(m *tailcfg.DERPMap) { m.Regions[900].Nodes[0].STUNTestIP = "127.0.0.1" },
		func(m *tailcfg.DERPMap) { m.Regions[900].Nodes[0].CertName = "sha256-raw:forged" },
		func(m *tailcfg.DERPMap) { m.Regions[900].Nodes[0].HostName = "https://relay.example" },
		func(m *tailcfg.DERPMap) { m.Regions[900].Nodes[0].DERPPort = 65536 },
		func(m *tailcfg.DERPMap) { m.Regions[900].Nodes[0].STUNPort = -2 },
		func(m *tailcfg.DERPMap) { m.Regions[900].Nodes[0].IPv4 = "127.0.0.1" },
		func(m *tailcfg.DERPMap) { m.Regions[900].Nodes[0].IPv4 = "2606:4700:4700::1111" },
		func(m *tailcfg.DERPMap) { m.Regions[900].Nodes[0].IPv6 = "8.8.8.8" },
		func(m *tailcfg.DERPMap) { m.Regions[900].Nodes[0].IPv4, m.Regions[900].Nodes[0].IPv6 = "none", "none" },
		func(m *tailcfg.DERPMap) { m.Regions[900].Avoid = true },
		func(m *tailcfg.DERPMap) { m.Regions[900].Nodes = append(m.Regions[900].Nodes, m.Regions[900].Nodes[0]) },
	}
	for _, change := range changes {
		model := testRelayModel()
		change(model)
		if _, err := reviewedRelayProfile(context.Background(), "https://operator.example/map", model, syntheticResolver); err == nil {
			t.Fatal("unsafe relay metadata accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reviewedRelayProfile(ctx, "https://operator.example/map", testRelayModel(), syntheticResolver); err == nil {
		t.Fatal("canceled profile review succeeded")
	}
}

func TestPeerCannotReplaceSelectedRelayOrTriggerDefaultLookup(t *testing.T) {
	profile := reviewedTestProfile(t)
	short := syntheticInvitationEndpoint(t, nil, 900)
	trusted, err := profile.trustedEndpoint(short)
	if err != nil {
		t.Fatal(err)
	}
	ci, err := tailcat.ParseAddr(trusted)
	if err != nil || len(ci.Region) != 1 || ci.RegionID != 0 || ci.Region[0].Nodes[0].IPv4 != "8.8.8.8" || ci.Region[0].Nodes[0].IPv6 != "none" {
		t.Fatal("short region reference did not use operator-pinned metadata")
	}
	if _, err := profile.trustedEndpoint(syntheticInvitationEndpoint(t, nil, 901)); err == nil {
		t.Fatal("unknown region could request a public default lookup")
	}
	if _, err := profile.trustedEndpoint(syntheticInvitationEndpoint(t, testRelayModel().Regions[900], 0)); err != nil {
		t.Fatal("matching embedded operator metadata rejected")
	}
	for _, change := range []func(*tailcfg.DERPNode){
		func(n *tailcfg.DERPNode) { n.HostName = "attacker.example" },
		func(n *tailcfg.DERPNode) { n.CertName = "attacker.example" },
		func(n *tailcfg.DERPNode) { n.DERPPort = 8443 },
		func(n *tailcfg.DERPNode) { n.STUNPort = 9999 },
		func(n *tailcfg.DERPNode) { n.IPv4 = "127.0.0.1" },
	} {
		region := testRelayModel().Regions[900]
		change(region.Nodes[0])
		if _, err := profile.trustedEndpoint(syntheticInvitationEndpoint(t, region, 0)); err == nil {
			t.Fatal("peer-controlled relay metadata replaced the operator selection")
		}
	}
	if _, err := profile.trustedEndpoint(address.Endpoint{}); err == nil {
		t.Fatal("zero endpoint accepted")
	}
	// Diagnostic errors are finite, not the private endpoint or source URL.
	if strings.Contains(ErrRelayProfile.Error(), "operator.example") {
		t.Fatal("relay error retained an operator endpoint")
	}
}
