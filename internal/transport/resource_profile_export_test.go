package transport

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"tailscale.com/envknob"
	"tailscale.com/tailcfg"
)

// These serialization helpers exist only in the augmented test package. They
// share one disposable loopback relay and exact certificate pin with isolated
// subprocesses; no production profile constructor or validation is changed.
func ExportResourceProfileForTest(t *testing.T, profile *RelayProfile) []byte {
	t.Helper()
	if !profile.Ready() || profile.state.source != "https://operator.example/test-only" {
		t.Fatal("resource fixture profile unavailable")
	}
	wire, err := json.Marshal(profile.state.regions[900])
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func ExportImportResourceProfileForTest(t *testing.T, wire []byte) (*RelayProfile, error) {
	t.Helper()
	if !networkPrivacyReady {
		return nil, errors.New("resource fixture requires reviewed privacy build tags")
	}
	var region tailcfg.DERPRegion
	if len(wire) > MaxRelayMapBytes || json.Unmarshal(wire, &region) != nil || region.RegionID != 900 || len(region.Nodes) != 1 {
		return nil, ErrRelayProfile
	}
	node := region.Nodes[0]
	if node == nil || node.RegionID != 900 || node.STUNPort != -1 || node.HostName != "127.0.0.1" || node.IPv4 != "127.0.0.1" || node.IPv6 != "none" || node.DERPPort < 1 || node.DERPPort > 65535 || len(node.CertName) != len("sha256-raw:")+64 || !strings.HasPrefix(node.CertName, "sha256-raw:") {
		return nil, ErrRelayProfile
	}
	if pin, err := hex.DecodeString(strings.TrimPrefix(node.CertName, "sha256-raw:")); err != nil || len(pin) != 32 {
		return nil, ErrRelayProfile
	}
	envknob.SetenvForTest(t, "IN_TS_TEST", "true")
	return &RelayProfile{state: &relayProfileState{source: "https://operator.example/test-only", expires: time.Now().Add(10 * time.Minute), defaultRegion: 900, regions: map[tailcfg.DERPRegionID]*tailcfg.DERPRegion{900: &region}, wire: map[tailcfg.DERPRegionID]*tailcfg.DERPRegion{900: cloneRelayRegion(&region)}}}, nil
}
