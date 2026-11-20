// Package callmedia protects call audio and video for one call. It turns
// encoded media frames into sealed datagrams and back, using SRTP with
// AES-256-GCM (RFC 7714) keyed from the contact's authenticated TLS session.
//
// It does no network I/O, opens no device and never sees unencoded media. It
// invents no cryptography: key bytes come from the standard TLS 1.3 exporter
// and are used by the reviewed pion/srtp library through its Context API only.
// Its session API is deliberately unused because that would create a logger
// driven by environment variables.
package callmedia

import (
	"errors"

	"github.com/pion/srtp/v3"
)

const (
	profile = srtp.ProtectionProfileAeadAes256Gcm
	keySize = 32
	// AES-GCM SRTP salts are 12 bytes (RFC 7714 section 12).
	saltSize     = 12
	materialSize = 2 * (keySize + saltSize)
	// replayWindow is how far behind the newest packet an authentic datagram
	// may arrive and still be accepted once. The library default is no replay
	// protection at all, so this is always passed explicitly.
	replayWindow = 1024
)

// ErrKeys reports unusable call key material. It never includes key bytes.
var ErrKeys = errors.New("call media keys are unavailable")

// Exporter is the one capability this package needs from an authenticated
// session. sessionauth.Session provides it only when both sides negotiated
// the call capability and the session is live.
type Exporter interface {
	ExportCallKeyMaterial(callID []byte, length int) ([]byte, error)
}

// Keys holds one call's two directional master keys. The zero value is unusable.
type Keys struct {
	send, receive direction
	ready         bool
}

type direction struct {
	key  [keySize]byte
	salt [saltSize]byte
}

func (Keys) String() string               { return "[private WhatsUp call media keys]" }
func (Keys) GoString() string             { return "[private WhatsUp call media keys]" }
func (Keys) MarshalJSON() ([]byte, error) { return nil, ErrKeys }

// Derive exports this call's keys from the live session. Each direction gets
// its own key and salt, so the two ends never encrypt under the same key, and
// each call ID yields unrelated keys. inviter is this device's fixed pairing
// role for the contact; the peer passes the opposite value.
func Derive(session Exporter, callID [16]byte, inviter bool) (*Keys, error) {
	if session == nil || callID == [16]byte{} {
		return nil, ErrKeys
	}
	material, err := session.ExportCallKeyMaterial(callID[:], materialSize)
	if err != nil || len(material) != materialSize {
		clear(material)
		return nil, ErrKeys
	}
	defer clear(material)
	// Layout: inviter key, joiner key, inviter salt, joiner salt.
	var fromInviter, fromJoiner direction
	copy(fromInviter.key[:], material[:keySize])
	copy(fromJoiner.key[:], material[keySize:2*keySize])
	copy(fromInviter.salt[:], material[2*keySize:2*keySize+saltSize])
	copy(fromJoiner.salt[:], material[2*keySize+saltSize:])
	if fromInviter.key == fromJoiner.key || fromInviter.key == [keySize]byte{} || fromJoiner.key == [keySize]byte{} {
		return nil, ErrKeys
	}
	keys := &Keys{ready: true}
	if inviter {
		keys.send, keys.receive = fromInviter, fromJoiner
	} else {
		keys.send, keys.receive = fromJoiner, fromInviter
	}
	return keys, nil
}

// Clear overwrites the key bytes held here. Contexts already built from them
// keep their own derived state until they are dropped; this is not a promise
// of secure memory erasure.
func (k *Keys) Clear() {
	if k == nil {
		return
	}
	clear(k.send.key[:])
	clear(k.send.salt[:])
	clear(k.receive.key[:])
	clear(k.receive.salt[:])
	k.ready = false
}
