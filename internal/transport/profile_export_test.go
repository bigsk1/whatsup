package transport

import "testing"

// ExportLoopbackRelayProfileForTest is available only in Go's augmented test
// package. No production caller can create a private-address relay profile.
func ExportLoopbackRelayProfileForTest(t *testing.T) *RelayProfile {
	t.Helper()
	return loopbackRelayProfile(t)
}
