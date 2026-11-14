// Package transport isolates the reviewed Tailcat dependency from application policy.
// Nothing in this package is bound directly to the frontend.
package transport

import "errors"

// Policy is the permitted application payload path. The zero value fails closed.
type Policy uint8

const (
	DirectOnly Policy = iota
	RelayFallback
)

var (
	ErrDirectOnlyUnavailable = errors.New("strict direct-only delivery is unavailable")
	ErrRelayConsentRequired  = errors.New("encrypted relay fallback requires explicit approval")
	ErrInvalidPolicy         = errors.New("unknown delivery policy")
)

// ValidatePolicy is called before constructing or starting a transport. Tailcat's
// path status is intentionally not used: a snapshot cannot control retransmission.
// DirectOnly remains unavailable even if discovery reports a direct path.
func ValidatePolicy(policy Policy, relayApproved bool) error {
	switch policy {
	case DirectOnly:
		return ErrDirectOnlyUnavailable
	case RelayFallback:
		if !relayApproved {
			return ErrRelayConsentRequired
		}
		return nil
	default:
		return ErrInvalidPolicy
	}
}
