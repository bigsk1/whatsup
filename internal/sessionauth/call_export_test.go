package sessionauth

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
)

func callID(fill byte) []byte { return bytes.Repeat([]byte{fill}, CallIDSize) }

func TestCallKeyMaterialMatchesAcrossTheSessionAndIsScopedToCallAndSession(t *testing.T) {
	a, b := optionsPair(t)
	left, right := connect(t, a, b)
	if left.err != nil || right.err != nil {
		t.Fatal("saved identity authentication failed")
	}
	if !left.session.Capabilities().Calls || !right.session.Capabilities().Calls {
		t.Fatal("current peers did not negotiate the call capability")
	}
	mine, err := left.session.ExportCallKeyMaterial(callID(7), 88)
	if err != nil || len(mine) != 88 {
		t.Fatal("call key export failed on a live call-capable session")
	}
	theirs, err := right.session.ExportCallKeyMaterial(callID(7), 88)
	if err != nil || !bytes.Equal(mine, theirs) {
		t.Fatal("both ends of one session did not derive identical call key material")
	}
	if bytes.Equal(mine, make([]byte, 88)) {
		t.Fatal("exported call key material is all zero")
	}
	other, err := left.session.ExportCallKeyMaterial(callID(8), 88)
	if err != nil || bytes.Equal(mine, other) {
		t.Fatal("a different call ID did not change the key material")
	}
	again, _ := connect(t, a, b)
	if again.err != nil {
		t.Fatal("second authentication failed")
	}
	fresh, err := again.session.ExportCallKeyMaterial(callID(7), 88)
	if err != nil || bytes.Equal(mine, fresh) {
		t.Fatal("a different authenticated session reused call key material")
	}
}

func TestCallKeyExportRejectsInvalidRequestsAndClosedSessions(t *testing.T) {
	a, b := optionsPair(t)
	left, _ := connect(t, a, b)
	if left.err != nil {
		t.Fatal("saved identity authentication failed")
	}
	for name, attempt := range map[string]func() ([]byte, error){
		"short call ID":    func() ([]byte, error) { return left.session.ExportCallKeyMaterial(make([]byte, CallIDSize-1), 32) },
		"long call ID":     func() ([]byte, error) { return left.session.ExportCallKeyMaterial(make([]byte, CallIDSize+1), 32) },
		"zero call ID":     func() ([]byte, error) { return left.session.ExportCallKeyMaterial(make([]byte, CallIDSize), 32) },
		"zero length":      func() ([]byte, error) { return left.session.ExportCallKeyMaterial(callID(1), 0) },
		"excessive length": func() ([]byte, error) { return left.session.ExportCallKeyMaterial(callID(1), MaxCallKeyMaterial+1) },
		"nil session":      func() ([]byte, error) { return (*Session)(nil).ExportCallKeyMaterial(callID(1), 32) },
	} {
		if material, err := attempt(); material != nil || !errors.Is(err, ErrAuthentication) {
			t.Errorf("%s: export was not refused", name)
		}
	}
	_ = left.session.Close()
	if material, err := left.session.ExportCallKeyMaterial(callID(1), 32); material != nil || !errors.Is(err, ErrClosed) {
		t.Fatal("a closed session still exported call key material")
	}
}

// An authenticated peer on the previous protocol must not be able to obtain
// call keys: the capability is fixed by the TLS transcript, not by a claim.
func TestCallKeyExportRequiresTheNegotiatedCallCapability(t *testing.T) {
	current, old := optionsPair(t)
	suffix := "." + old.ContactID + "." + base64.RawURLEncoding.EncodeToString(old.Association[:])
	got, _, err := rawProtocolPeer(t, current, nil, protocolPeerConfig(t, old, []string{avatarProtocol + suffix}))
	if err != nil || got.err != nil || got.session == nil {
		t.Fatalf("previous-protocol peer did not authenticate: %v / %v", got.err, err)
	}
	if got.session.Capabilities().Calls {
		t.Fatal("a peer that did not offer the call protocol was granted the call capability")
	}
	if material, err := got.session.ExportCallKeyMaterial(callID(1), 32); material != nil || !errors.Is(err, ErrAuthentication) {
		t.Fatal("call key material was exported without the negotiated call capability")
	}
}
