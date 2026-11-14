// Package relaychange signs bounded relay-change contributions. It performs no
// discovery, transport, persistence or identity lookup.
package relaychange

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net/url"
	"strings"
	"whatsup/internal/identity"
)

const Prefix = "whatsup-relay:v1:"
const MaxText = 8192
const MaxWire = 4096
const (
	Proposal uint8 = iota + 1
	Approval
	Decline
	Cancel
	Commit
)

var ErrInvalid = errors.New("relay change is invalid or unavailable")
var domain = []byte("WhatsUp relay change signature v1\x00")

type Statement struct {
	Kind                      uint8
	ContactID                 string
	Base, Old                 [32]byte
	Epoch                     uint64
	Nonce                     string
	Proposer, Signer          uint8
	ServiceURL                string
	RegionID                  uint32
	ProposalHash, Association [32]byte
}
type Signed struct {
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}

func (Statement) String() string            { return "[private relay change statement]" }
func (Statement) GoString() string          { return "[private relay change statement]" }
func (Signed) String() string               { return "[private signed relay change]" }
func (Signed) GoString() string             { return "[private signed relay change]" }
func (Signed) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }
func Clone(s Signed) Signed {
	return Signed{Payload: bytes.Clone(s.Payload), Signature: bytes.Clone(s.Signature)}
}
func rawID(s string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil || len(raw) != 16 || base64.RawURLEncoding.EncodeToString(raw) != s || bytes.Equal(raw, make([]byte, 16)) {
		return nil, ErrInvalid
	}
	return raw, nil
}
func valid(s Statement) bool {
	if _, err := rawID(s.ContactID); err != nil {
		return false
	}
	if _, err := rawID(s.Nonce); err != nil {
		return false
	}
	if s.Base == [32]byte{} || s.Old == [32]byte{} || s.Epoch == 0 || s.Proposer < 1 || s.Proposer > 2 || s.Signer < 1 || s.Signer > 2 {
		return false
	}
	if s.Kind == Proposal {
		u, err := url.Parse(s.ServiceURL)
		return s.Signer == s.Proposer && s.RegionID > 0 && len(s.ServiceURL) > 0 && len(s.ServiceURL) <= 2048 && strings.IndexFunc(s.ServiceURL, func(r rune) bool { return r < 33 || r > 126 }) < 0 && err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.Opaque == "" && u.String() == s.ServiceURL && s.ProposalHash == [32]byte{} && s.Association == [32]byte{}
	}
	if s.Kind < Approval || s.Kind > Commit || s.ServiceURL != "" || s.RegionID != 0 || s.ProposalHash == [32]byte{} {
		return false
	}
	if (s.Kind == Approval || s.Kind == Decline) && s.Signer == s.Proposer {
		return false
	}
	if s.Kind == Commit {
		return s.Association != [32]byte{}
	}
	return s.Association == [32]byte{}
}
func encode(s Statement) ([]byte, error) {
	if !valid(s) {
		return nil, ErrInvalid
	}
	id, _ := rawID(s.ContactID)
	nonce, _ := rawID(s.Nonce)
	wire := []byte{1, s.Kind}
	wire = append(wire, id...)
	wire = append(wire, s.Base[:]...)
	wire = append(wire, s.Old[:]...)
	wire = binary.BigEndian.AppendUint64(wire, s.Epoch)
	wire = append(wire, nonce...)
	wire = append(wire, s.Proposer, s.Signer)
	if s.Kind == Proposal {
		wire = binary.BigEndian.AppendUint16(wire, uint16(len(s.ServiceURL)))
		wire = append(wire, s.ServiceURL...)
		wire = binary.BigEndian.AppendUint32(wire, s.RegionID)
	} else {
		wire = append(wire, s.ProposalHash[:]...)
		if s.Kind == Commit {
			wire = append(wire, s.Association[:]...)
		}
	}
	return wire, nil
}
func Sign(local *identity.Identity, s Statement) (Signed, error) {
	if local == nil {
		return Signed{}, ErrInvalid
	}
	wire, err := encode(s)
	if err != nil {
		return Signed{}, err
	}
	message := append(bytes.Clone(domain), wire...)
	return Signed{Payload: wire, Signature: local.Sign(message)}, nil
}
func (s Signed) Statement() (Statement, error) {
	var out Statement
	if len(s.Signature) != 64 || len(s.Payload)+64 > MaxWire {
		return out, ErrInvalid
	}
	r := bytes.NewReader(s.Payload)
	var head [2]byte
	if _, err := io.ReadFull(r, head[:]); err != nil || head[0] != 1 {
		return out, ErrInvalid
	}
	out.Kind = head[1]
	var id, nonce [16]byte
	if _, err := io.ReadFull(r, id[:]); err != nil {
		return out, ErrInvalid
	}
	out.ContactID = base64.RawURLEncoding.EncodeToString(id[:])
	if _, err := io.ReadFull(r, out.Base[:]); err != nil {
		return out, ErrInvalid
	}
	if _, err := io.ReadFull(r, out.Old[:]); err != nil {
		return out, ErrInvalid
	}
	if binary.Read(r, binary.BigEndian, &out.Epoch) != nil {
		return out, ErrInvalid
	}
	if _, err := io.ReadFull(r, nonce[:]); err != nil {
		return out, ErrInvalid
	}
	out.Nonce = base64.RawURLEncoding.EncodeToString(nonce[:])
	roles := make([]byte, 2)
	if _, err := io.ReadFull(r, roles); err != nil {
		return out, ErrInvalid
	}
	out.Proposer, out.Signer = roles[0], roles[1]
	if out.Kind == Proposal {
		var n uint16
		if binary.Read(r, binary.BigEndian, &n) != nil || n > 2048 {
			return out, ErrInvalid
		}
		text := make([]byte, n)
		if _, err := io.ReadFull(r, text); err != nil {
			return out, ErrInvalid
		}
		out.ServiceURL = string(text)
		if binary.Read(r, binary.BigEndian, &out.RegionID) != nil {
			return out, ErrInvalid
		}
	} else {
		if _, err := io.ReadFull(r, out.ProposalHash[:]); err != nil {
			return out, ErrInvalid
		}
		if out.Kind == Commit {
			if _, err := io.ReadFull(r, out.Association[:]); err != nil {
				return out, ErrInvalid
			}
		}
	}
	canonical, err := encode(out)
	if err != nil || r.Len() != 0 || !bytes.Equal(canonical, s.Payload) {
		return Statement{}, ErrInvalid
	}
	return out, nil
}
func (s Signed) Verify(peer identity.PublicKey) (Statement, error) {
	out, err := s.Statement()
	if err != nil || !peer.Verify(append(bytes.Clone(domain), s.Payload...), s.Signature) {
		return Statement{}, ErrInvalid
	}
	return out, nil
}
func (s Signed) Bytes() []byte    { return append(bytes.Clone(s.Payload), s.Signature...) }
func (s Signed) Digest() [32]byte { return sha256.Sum256(s.Bytes()) }
func ParseBytes(raw []byte) (Signed, error) {
	if len(raw) < 64 || len(raw) > MaxWire {
		return Signed{}, ErrInvalid
	}
	s := Signed{Payload: bytes.Clone(raw[:len(raw)-64]), Signature: bytes.Clone(raw[len(raw)-64:])}
	if _, err := s.Statement(); err != nil {
		return Signed{}, err
	}
	return s, nil
}
func Format(s Signed) (string, error) {
	if _, err := s.Statement(); err != nil {
		return "", err
	}
	text := Prefix + base64.RawURLEncoding.EncodeToString(s.Bytes())
	if len(text) > MaxText {
		return "", ErrInvalid
	}
	return text, nil
}
func Parse(text string) (Signed, error) {
	if len(text) > MaxText || !strings.HasPrefix(text, Prefix) || strings.IndexFunc(text, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return Signed{}, ErrInvalid
	}
	encoded := strings.TrimPrefix(text, Prefix)
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != encoded {
		return Signed{}, ErrInvalid
	}
	return ParseBytes(raw)
}
