package transport

import (
	"testing"
	"time"

	"tailscale.com/tailcfg"
)

// Only the augmented test package can relabel a certificate-pinned disposable
// loopback service. Production FetchRelayProfile and its address policy stay
// unchanged. Node/region material is copied exactly, including the TLS pin.
func ExportRelayMigrationProfileForTest(t *testing.T, original *RelayProfile, source string) *RelayProfile {
	t.Helper()
	if !original.Ready() || original.state.source != "https://operator.example/test-only" {
		t.Fatal("invalid disposable migration profile")
	}
	if _, err := relayMapURL(source); err != nil {
		t.Fatal("invalid synthetic migration source")
	}
	state := &relayProfileState{source: source, expires: time.Now().Add(3 * time.Minute), defaultRegion: original.state.defaultRegion, regions: map[tailcfg.DERPRegionID]*tailcfg.DERPRegion{}, wire: map[tailcfg.DERPRegionID]*tailcfg.DERPRegion{}}
	for id, region := range original.state.regions {
		state.regions[id] = cloneRelayRegion(region)
	}
	for id, region := range original.state.wire {
		state.wire[id] = cloneRelayRegion(region)
	}
	return &RelayProfile{state: state}
}
