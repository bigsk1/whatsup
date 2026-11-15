// Package profile validates optional display labels. Names never establish
// identity, local conversation authority, or permission to start networking.
package profile

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxNameBytes = 256
	MaxNameRunes = 64
)

var ErrName = errors.New("display name is invalid")

// Local is encrypted local profile state. Revision changes only when the owner
// saves different profile metadata; both sharing choices are off for a new
// profile. AvatarRevision is a local change trigger, never a peer-supplied floor
// or a counter shared across independent authenticated TLS sessions.
type Local struct {
	DisplayName       string `json:"displayName"`
	ShareDisplayName  bool   `json:"shareDisplayName"`
	Revision          uint64 `json:"revision"`
	Avatar            string `json:"avatar,omitempty"`
	ShareAvatar       bool   `json:"shareAvatar,omitempty"`
	HideContactPhotos bool   `json:"hideContactPhotos,omitempty"`
	AvatarRevision    uint64 `json:"avatarRevision,omitempty"`
}

// Contact labels are scoped to an exact locally approved contact association.
// LocalName is never sent to the peer. PeerDisplayName is only a received label.
type Contact struct {
	LocalName       string `json:"localName"`
	PeerDisplayName string `json:"peerDisplayName"`
}

func (Local) String() string     { return "[private WhatsUp profile]" }
func (Local) GoString() string   { return "[private WhatsUp profile]" }
func (Contact) String() string   { return "[private WhatsUp contact names]" }
func (Contact) GoString() string { return "[private WhatsUp contact names]" }

// ValidateName accepts an empty cleared label, ordinary right-to-left letters,
// combining accents and visible Unicode. It rejects noncanonical edge spaces,
// invalid UTF-8, multiline/format controls and invisible fillers before either
// storage or transport can accept them. It does not alter a peer-provided name.
func ValidateName(name string) error {
	if len(name) > MaxNameBytes || !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxNameRunes || strings.TrimSpace(name) != name {
		return ErrName
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) || unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) || r == '\u2800' {
			return ErrName
		}
	}
	return nil
}
