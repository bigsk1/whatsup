// Package address validates private Tailcat endpoints without resolving names or
// starting networking. Parsing is syntax validation, not identity authentication
// or permission to use a peer's advertised relay.
package address

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/netip"
	"strings"

	"github.com/fxamacker/cbor/v2"
	"github.com/tailscale/tailcat"
)

const MaxBytes = 512

var ErrInvalid = errors.New("invalid private transport endpoint")

// Endpoint contains a WireGuard PSK. Its fields are private, ordinary formatting
// is redacted, and serialization is forbidden. A zero Endpoint cannot be used.
type Endpoint struct{ encoded tailcat.Addr }

func Parse(raw string) (Endpoint, error) {
	if len(raw) <= 2 || len(raw) > MaxBytes || !strings.HasPrefix(raw, "tc") {
		return Endpoint{}, ErrInvalid
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(raw[2:])
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != raw[2:] {
		return Endpoint{}, ErrInvalid
	}
	// Declared CBOR collection lengths can be much larger than the short input.
	// Preflight with tight allocation/depth limits before upstream's more general
	// decoder. No Tailcat address needs indefinite items, tags or duplicate keys.
	decoder, err := (cbor.DecOptions{
		MaxNestedLevels: 8, MaxArrayElements: 16, MaxMapPairs: 16,
		DupMapKey: cbor.DupMapKeyEnforcedAPF, IndefLength: cbor.IndefLengthForbidden, TagsMd: cbor.TagsForbidden,
	}).DecMode()
	var wire any
	if err != nil || decoder.Unmarshal(decoded, &wire) != nil {
		return Endpoint{}, ErrInvalid
	}
	ci, err := tailcat.ParseAddr(tailcat.Addr(raw))
	if err != nil || ci.ServerPublic.IsZero() || ci.ServerDiscoPublic.IsZero() || ci.PresharedKey.IsZero() {
		return Endpoint{}, ErrInvalid
	}
	// A saved keypair's -1 region marker is not a dialable endpoint. A short
	// region reference still requires an explicitly configured relay map when
	// networking is eventually implemented; parsing never fetches a default map.
	if (len(ci.Region) == 0 && ci.RegionID <= 0) || (len(ci.Region) > 0 && ci.RegionID != 0) || len(ci.Region) > 4 {
		return Endpoint{}, ErrInvalid
	}
	nodes := 0
	for _, region := range ci.Region {
		if region == nil || region.RegionID <= 0 || len(region.Nodes) == 0 {
			return Endpoint{}, ErrInvalid
		}
		for _, node := range region.Nodes {
			nodes++
			if node == nil || nodes > 8 || node.InsecureForTests || !validHost(node.HostName) ||
				(node.CertName != "" && !validHost(node.CertName)) ||
				node.DERPPort < 0 || node.DERPPort > 65535 || node.STUNPort < -1 || node.STUNPort > 65535 ||
				!validIP(node.IPv4, true) || !validIP(node.IPv6, false) {
				return Endpoint{}, ErrInvalid
			}
		}
	}
	// This rejects noncanonical CBOR, unknown/duplicate fields, legacy addresses
	// and alternate encodings instead of handing permissively parsed data to a dialer.
	if string(ci.Addr()) != raw {
		return Endpoint{}, ErrInvalid
	}
	return Endpoint{encoded: tailcat.Addr(raw)}, nil
}

func validHost(host string) bool {
	if len(host) == 0 || len(host) > 253 || strings.HasSuffix(host, ".") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

func validIP(raw string, v4 bool) bool {
	if raw == "" {
		return true
	}
	ip, err := netip.ParseAddr(raw)
	return err == nil && ip.Zone() == "" && ip.Is4() == v4 && !ip.Is4In6()
}

func (e Endpoint) Valid() bool { return e.encoded != "" }

func (e Endpoint) Equal(other Endpoint) bool {
	return subtle.ConstantTimeCompare([]byte(e.encoded), []byte(other.encoded)) == 1
}

// EncodeForTransport is a backend-only explicit secret export. It grants no
// authentication or relay consent and must never be exposed on the Wails bridge.
func (e Endpoint) EncodeForTransport() tailcat.Addr { return e.encoded }
func (Endpoint) String() string                     { return "[private transport endpoint]" }
func (Endpoint) GoString() string                   { return "[private transport endpoint]" }
func (Endpoint) MarshalJSON() ([]byte, error) {
	return nil, errors.New("private transport endpoints cannot be serialized")
}
