package transport

import (
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
)

func TestOngoingListenerRelationRequiresApprovedExactPhysicalRegion(t *testing.T) {
	if !networkPrivacyReady {
		t.Skip("requires the reviewed network privacy omission tags")
	}
	profile := reviewedTestProfile(t)
	record, _ := ongoingRecords(t, profile)
	first := OngoingOptions{Record: record, Profile: profile, Policy: RelayFallback, AllowRelayFallback: true}
	makeSecond := func() OngoingOptions {
		state := *profile.state
		state.source = "https://second-operator.example/map"
		state.regions = map[tailcfg.DERPRegionID]*tailcfg.DERPRegion{}
		for id, region := range profile.state.regions {
			state.regions[id] = cloneRelayRegion(region)
		}
		second := first
		second.Profile = &RelayProfile{state: &state}
		second.Record.RelayServiceURL = state.source
		return second
	}
	for _, test := range []struct {
		name                string
		change              func(*OngoingOptions)
		equivalent, overlap bool
		invalid             bool
	}{
		{name: "exact alias", equivalent: true, overlap: true},
		{name: "presentation names", change: func(o *OngoingOptions) {
			r := o.Profile.state.regions[900]
			r.RegionName, r.RegionCode, r.Latitude, r.Longitude = "renamed", "other", 4, 7
			r.Nodes[0].Name = "renamed node"
		}, equivalent: true, overlap: true},
		{name: "certificate differs", change: func(o *OngoingOptions) { o.Profile.state.regions[900].Nodes[0].CertName = "other.example" }, overlap: true},
		{name: "extra fallback node", change: func(o *OngoingOptions) {
			n := *o.Profile.state.regions[900].Nodes[0]
			n.Name, n.HostName, n.IPv4 = "second-node", "other.example", "9.9.9.9"
			o.Profile.state.regions[900].Nodes = append(o.Profile.state.regions[900].Nodes, &n)
		}, overlap: true},
		{name: "STUN differs", change: func(o *OngoingOptions) { o.Profile.state.regions[900].Nodes[0].STUNPort = -1 }, overlap: true},
		{name: "region label differs", change: func(o *OngoingOptions) {
			region := o.Profile.state.regions[900]
			region.RegionID, region.Nodes[0].RegionID = 901, 901
			delete(o.Profile.state.regions, 900)
			o.Profile.state.regions[901], o.Profile.state.defaultRegion = region, 901
			ci, err := tailcat.ParseAddr(tailcat.Addr(o.Record.ServerEndpoint))
			if err != nil {
				t.Fatal(err)
			}
			ci.RegionID = 901
			o.Record.ServerEndpoint = string(ci.Addr())
		}, overlap: true},
		{name: "disjoint node", change: func(o *OngoingOptions) {
			n := o.Profile.state.regions[900].Nodes[0]
			n.HostName, n.IPv4 = "other.example", "9.9.9.9"
		}},
		{name: "disjoint port", change: func(o *OngoingOptions) { o.Profile.state.regions[900].Nodes[0].DERPPort = 8443 }},
		{name: "missing local consent", change: func(o *OngoingOptions) { o.Record.RelayFallbackConsent = false }, invalid: true},
		{name: "substituted URL", change: func(o *OngoingOptions) { o.Record.RelayServiceURL = "https://unapproved.example/map" }, invalid: true},
		{name: "expired profile", change: func(o *OngoingOptions) { o.Profile.state.expires = time.Now().Add(-time.Second) }, invalid: true},
		{name: "nil profile", change: func(o *OngoingOptions) { o.Profile = nil }, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			second := makeSecond()
			if test.change != nil {
				test.change(&second)
			}
			equivalent, overlap, err := OngoingListenerRelation(first, second)
			if (err != nil) != test.invalid || equivalent != test.equivalent || overlap != test.overlap {
				t.Fatalf("unexpected relation: equivalent=%t overlap=%t invalid=%t", equivalent, overlap, err != nil)
			}
		})
	}
}
