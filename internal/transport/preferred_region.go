package transport

// PreferredRegion returns only the selected ID from a live, validated operator
// map. It never fetches a map, exposes an address, or extends the map lifetime.
func (p *RelayProfile) PreferredRegion() (uint16, error) {
	if !p.Ready() || uint64(p.state.defaultRegion) > 65535 {
		return 0, ErrRelayProfile
	}
	return uint16(p.state.defaultRegion), nil
}
