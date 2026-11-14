package address

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

func syntheticInfo() tailcat.ConnInfo {
	return tailcat.ConnInfo{
		ServerPublic:      tailcat.NodePublic{NodePublic: key.NewNode().Public()},
		ServerDiscoPublic: tailcat.DiscoPublic{DiscoPublic: key.NewDisco().Public()},
		PresharedKey:      tailcat.NewPresharedKey(), RegionID: 1,
	}
}

func TestEndpointRequiresCanonicalTypedTailcatAddress(t *testing.T) {
	ci := syntheticInfo()
	raw := string(ci.Addr())
	endpoint, err := Parse(raw)
	if err != nil || !endpoint.Valid() || string(endpoint.EncodeForTransport()) != raw || !endpoint.Equal(endpoint) {
		t.Fatal("canonical private endpoint did not round trip")
	}
	other := syntheticInfo()
	different, err := Parse(string(other.Addr()))
	if err != nil || endpoint.Equal(different) {
		t.Fatal("different endpoint was conflated")
	}
	if (Endpoint{}).Valid() || endpoint.Equal(Endpoint{}) {
		t.Fatal("zero endpoint was usable")
	}
	for _, invalid := range []string{
		"", "synthetic-endpoint", "https://relay.example.test/map", "relay.example.test:443/path",
		"tc", raw + "=", raw + "\n", " " + raw, strings.Repeat("x", MaxBytes+1),
		"tc" + base64.RawURLEncoding.EncodeToString([]byte(`{"ServerPublic":"legacy"}`)),
	} {
		if _, err := Parse(invalid); !errors.Is(err, ErrInvalid) {
			t.Fatal("untyped or noncanonical endpoint was admitted")
		}
	}
	for _, invalidCBOR := range [][]byte{
		{0x9a, 0xff, 0xff, 0xff, 0xff},           // huge declared array before its contents
		{0xba, 0xff, 0xff, 0xff, 0xff},           // huge declared map before its contents
		{0xa2, 0x61, 'i', 0x01, 0x61, 'i', 0x01}, // duplicate relay key
		{0xbf, 0xff},                             // indefinite map
		{0xc1, 0x01},                             // tagged value
		{0x81, 0x81, 0x81, 0x81, 0x81, 0x81, 0x81, 0x81, 0x81, 0x80}, // excess depth
	} {
		if _, err := Parse("tc" + base64.RawURLEncoding.EncodeToString(invalidCBOR)); !errors.Is(err, ErrInvalid) {
			t.Fatal("unbounded or ambiguous CBOR passed endpoint preflight")
		}
	}
	for _, mutate := range []func(*tailcat.ConnInfo){
		func(ci *tailcat.ConnInfo) { ci.ServerPublic = tailcat.NodePublic{} },
		func(ci *tailcat.ConnInfo) { ci.ServerDiscoPublic = tailcat.DiscoPublic{} },
		func(ci *tailcat.ConnInfo) { ci.PresharedKey = tailcat.PresharedKey{} },
		func(ci *tailcat.ConnInfo) { ci.RegionID = 0 },
		func(ci *tailcat.ConnInfo) { ci.RegionID = -1 },
	} {
		invalid := syntheticInfo()
		mutate(&invalid)
		if _, err := Parse(string(invalid.Addr())); !errors.Is(err, ErrInvalid) {
			t.Fatal("endpoint omitted a required key, PSK or usable relay reference")
		}
	}
	// Upstream's permissive decoder ignores unknown map fields. Our canonical
	// round trip must reject them before any future dialer receives the address.
	decoded, _ := base64.RawURLEncoding.DecodeString(raw[2:])
	var fields map[string]cbor.RawMessage
	if err := cbor.Unmarshal(decoded, &fields); err != nil {
		t.Fatal(err)
	}
	fields["unexpected"] = cbor.RawMessage{0xf5}
	altered, err := cbor.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse("tc" + base64.RawURLEncoding.EncodeToString(altered)); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown endpoint fields were silently ignored")
	}
}

func embeddedInfo() tailcat.ConnInfo {
	ci := syntheticInfo()
	ci.RegionID = 0
	ci.Region = []*tailcfg.DERPRegion{{RegionID: 1, Nodes: []*tailcfg.DERPNode{{HostName: "relay.example.test", DERPPort: 443, STUNPort: 3478}}}}
	return ci
}

func TestEmbeddedRelaysCannotDisableTLSOrSmuggleURLs(t *testing.T) {
	ci := embeddedInfo()
	if _, err := Parse(string(ci.Addr())); err != nil {
		t.Fatal("bounded TLS relay metadata was rejected")
	}
	for _, mutate := range []func(*tailcat.ConnInfo){
		func(ci *tailcat.ConnInfo) { ci.Region[0].Nodes[0].InsecureForTests = true },
		func(ci *tailcat.ConnInfo) { ci.Region[0].Nodes[0].HostName = "https://relay.example.test/path" },
		func(ci *tailcat.ConnInfo) { ci.Region[0].Nodes[0].HostName = "user@relay.example.test" },
		func(ci *tailcat.ConnInfo) { ci.Region[0].Nodes[0].HostName = "relay.example.test\n" },
		func(ci *tailcat.ConnInfo) { ci.Region[0].Nodes[0].HostName = "../relay.example.test" },
		func(ci *tailcat.ConnInfo) { ci.Region[0].Nodes[0].DERPPort = 65536 },
		func(ci *tailcat.ConnInfo) { ci.Region[0].Nodes[0].STUNPort = -2 },
		func(ci *tailcat.ConnInfo) { ci.Region[0].Nodes[0].IPv4 = "2001:db8::1" },
		func(ci *tailcat.ConnInfo) { ci.Region[0].Nodes[0].IPv6 = "::ffff:192.0.2.1" },
		func(ci *tailcat.ConnInfo) { ci.Region[0].Nodes = nil },
		func(ci *tailcat.ConnInfo) { ci.RegionID = 1 },
	} {
		invalid := embeddedInfo()
		mutate(&invalid)
		if _, err := Parse(string(invalid.Addr())); !errors.Is(err, ErrInvalid) {
			t.Fatal("unsafe embedded relay metadata was admitted")
		}
	}
}

func TestEndpointFormattingAndErrorsDoNotRevealBearerPSK(t *testing.T) {
	ci := syntheticInfo()
	raw := string(ci.Addr())
	endpoint, _ := Parse(raw)
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if fmt.Sprintf(format, endpoint) != "[private transport endpoint]" {
			t.Fatal("ordinary formatting exposed an endpoint")
		}
	}
	if _, err := json.Marshal(endpoint); err == nil || strings.Contains(err.Error(), raw) {
		t.Fatal("endpoint JSON exposed bearer material")
	}
	if _, err := Parse(raw + "/secret"); err == nil || err.Error() != ErrInvalid.Error() {
		t.Fatal("parser error included attacker-controlled bearer material")
	}
}

func FuzzParseEndpoint(f *testing.F) {
	ci := syntheticInfo()
	f.Add(string(ci.Addr()))
	f.Add("")
	f.Add("tcAA")
	f.Fuzz(func(t *testing.T, raw string) {
		endpoint, err := Parse(raw)
		if err == nil && (!endpoint.Valid() || string(endpoint.EncodeForTransport()) != raw) {
			t.Fatal("accepted endpoint did not round trip")
		}
	})
}
