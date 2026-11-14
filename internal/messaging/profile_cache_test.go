package messaging

import (
	"testing"

	"whatsup/internal/transport"
)

// Isolate the bounded retention policy from profile validation: these are
// distinct synthetic profile identities, not usable network configurations.
// Transport tests separately require exact URLs and the original unexpired TTL.
func TestRelayProfileCacheRetainsThirdConsentedIdentityBeforePlanRebuild(t *testing.T) {
	m := &Manager{managerState: &managerState{}}
	a, b, c, d := &transport.RelayProfile{}, &transport.RelayProfile{}, &transport.RelayProfile{}, &transport.RelayProfile{}
	const first = "https://operator.example/first"
	const second = "https://operator.example/second"
	const third = "https://operator.example/third"
	const fourth = "https://operator.example/fourth"
	m.rememberRelayProfileLocked(first, a)
	m.rememberRelayProfileLocked(second, b)
	// Preparation for B→C can occur while A and B are still cached. The third
	// fetched identity must be available for every subsequent connection retry.
	m.rememberRelayProfileLocked(third, c)
	for range 5 {
		if got := m.profiles[third]; got != c {
			t.Fatal("third service was not retained for retry reuse")
		}
	}
	if len(m.profiles) != 2 || m.profiles[first] != nil || m.profiles[second] != b {
		t.Fatal("FIFO did not evict the oldest identity within the two-entry bound")
	}
	// A later plan rebuild can prune another entry. Its stale ordering slot must
	// not make the next insertion evict a still reusable profile unnecessarily.
	delete(m.profiles, second)
	m.rememberRelayProfileLocked(fourth, d)
	if len(m.profiles) != 2 || m.profiles[third] != c || m.profiles[fourth] != d {
		t.Fatal("retired ordering slot displaced a live profile")
	}
	replacement := &transport.RelayProfile{}
	m.rememberRelayProfileLocked(third, replacement)
	if len(m.profiles) != 2 || m.profiles[third] != replacement || m.profiles[fourth] != d {
		t.Fatal("refreshing an existing URL consumed a third cache entry")
	}
}
