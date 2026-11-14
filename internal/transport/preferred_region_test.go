package transport

import (
	"testing"
	"time"

	"tailscale.com/tailcfg"
)

func TestPreferredRegionRequiresLiveValidatedSelection(t *testing.T) {
	var nilProfile *RelayProfile
	for _, p := range []*RelayProfile{nilProfile, {}, {state: &relayProfileState{}}} {
		if id, err := p.PreferredRegion(); err == nil || id != 0 {
			t.Fatal("unavailable map returned region")
		}
	}
	state := &relayProfileState{source: "https://relay.example/map.json", expires: time.Now().Add(time.Minute), defaultRegion: 900, regions: map[tailcfg.DERPRegionID]*tailcfg.DERPRegion{900: {RegionID: 900}}}
	p := &RelayProfile{state: state}
	if id, err := p.PreferredRegion(); err != nil || id != 900 {
		t.Fatal("valid selection changed", err)
	}
	state.expires = time.Now().Add(-time.Second)
	if _, err := p.PreferredRegion(); err == nil {
		t.Fatal("expired map returned region")
	}
	state.expires = time.Now().Add(time.Minute)
	state.defaultRegion = 70000
	state.regions[70000] = &tailcfg.DERPRegion{RegionID: 70000}
	if _, err := p.PreferredRegion(); err == nil {
		t.Fatal("unrepresentable region was truncated")
	}
}
