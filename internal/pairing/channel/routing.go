package channel

import (
	"crypto/sha256"
	"encoding/binary"
	"io"
	"net/url"
	"strings"
)

const maxRelayServiceURL = 2048
const maxServerEndpoint = 512

func validRelaySelection(raw string) bool {
	if len(raw) == 0 || len(raw) > maxRelayServiceURL || strings.ContainsAny(raw, " \r\n\t") {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.Opaque == "" && u.String() == raw
}

// RoutingCommitment hashes the exact explicitly accepted service and ongoing
// endpoint using a versioned, length-delimited layout. Empty legacy agreements
// have no routing commitment and cannot restore ongoing transport credentials.
func RoutingCommitment(relayServiceURL, serverEndpoint string) ([32]byte, error) {
	if relayServiceURL == "" && serverEndpoint == "" {
		return [32]byte{}, nil
	}
	if !validRelaySelection(relayServiceURL) || len(serverEndpoint) == 0 || len(serverEndpoint) > maxServerEndpoint || strings.ContainsAny(serverEndpoint, " \r\n\t") {
		return [32]byte{}, ErrInvalid
	}
	wire := []byte("WhatsUp contact routing v1\x00")
	wire = binary.BigEndian.AppendUint16(wire, uint16(len(relayServiceURL)))
	wire = append(wire, relayServiceURL...)
	wire = binary.BigEndian.AppendUint16(wire, uint16(len(serverEndpoint)))
	wire = append(wire, serverEndpoint...)
	wire = append(wire, 1) // explicit encrypted-relay fallback is the only live policy
	digest := sha256.Sum256(wire)
	clear(wire)
	return digest, nil
}

func provisioningMagic(service string) string {
	if service != "" {
		return "WUPRE002"
	}
	return "WUPRE001"
}

func writeProvision(writer io.Writer, kind byte, role Role, binding [32]byte, payload []byte, service, endpoint string) error {
	packet := makePacket(provisioningMagic(service), kind, role, binding, len(payload))
	copy(packet[frameSize:], payload)
	if service != "" {
		packet = binary.BigEndian.AppendUint16(packet, uint16(len(service)))
		packet = append(packet, service...)
		packet = binary.BigEndian.AppendUint16(packet, uint16(len(endpoint)))
		packet = append(packet, endpoint...)
	}
	defer clear(packet)
	return writePacket(writer, packet)
}

func readProvision(reader io.Reader, kind byte, role Role, binding [32]byte, payload int, service string, server bool) ([]byte, string, error) {
	packet, err := readPacket(reader, provisioningMagic(service), kind, role, binding, payload)
	if err != nil {
		return nil, "", err
	}
	if service == "" {
		return packet, "", nil
	}
	read := func(maximum int) (string, error) {
		var size [2]byte
		if _, err := io.ReadFull(reader, size[:]); err != nil {
			return "", ErrProtocol
		}
		n := int(binary.BigEndian.Uint16(size[:]))
		if n > maximum {
			return "", ErrProtocol
		}
		value := make([]byte, n)
		defer clear(value)
		if _, err := io.ReadFull(reader, value); err != nil {
			return "", ErrProtocol
		}
		return string(value), nil
	}
	remoteService, err := read(maxRelayServiceURL)
	if err != nil || remoteService != service {
		clear(packet)
		return nil, "", ErrProtocol
	}
	endpoint, err := read(maxServerEndpoint)
	if err != nil || (server && endpoint == "") || (!server && endpoint != "") {
		clear(packet)
		return nil, "", ErrProtocol
	}
	if server {
		if _, err := RoutingCommitment(service, endpoint); err != nil {
			clear(packet)
			return nil, "", ErrProtocol
		}
	}
	return packet, endpoint, nil
}
