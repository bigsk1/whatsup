package desktop

import (
	"errors"
	"net/url"

	"whatsup/internal/pairing"
)

type InvitationInspection struct {
	ServiceURL string `json:"serviceURL"`
	Host       string `json:"host"`
}

// InspectInvitation is a local-only parse, independent of storage unlock. No
// resolver, HTTP client, transport factory or pairing controller is consulted.
func (*Service) InspectInvitation(link string) (InvitationInspection, error) {
	invite, err := pairing.ParseInvite(link)
	if err != nil || invite.ServiceURL() == "" {
		return InvitationInspection{}, errors.New("This invitation does not include a supported HTTPS connection service")
	}
	service, err := url.Parse(invite.ServiceURL())
	if err != nil {
		return InvitationInspection{}, errors.New("This invitation does not include a supported HTTPS connection service")
	}
	return InvitationInspection{ServiceURL: invite.ServiceURL(), Host: service.Host}, nil
}
