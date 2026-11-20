package sessionauth

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
)

// callContext builds a key context from a call ID fill and an answer fill.
func callContext(call, answer byte) []byte {
	return append(bytes.Repeat([]byte{call}, CallContextSize/2), bytes.Repeat([]byte{answer}, CallContextSize/2)...)
}

func TestCallKeyMaterialMatchesAcrossTheSessionAndIsScopedToContextAndSession(t *testing.T) {
	a, b := optionsPair(t)
	left, right := connect(t, a, b)
	if left.err != nil || right.err != nil {
		t.Fatal("saved identity authentication failed")
	}
	if !left.session.Capabilities().Calls || !right.session.Capabilities().Calls {
		t.Fatal("current peers did not negotiate the call capability")
	}
	mine, err := left.session.ExportCallKeyMaterial(callContext(7, 1), 88)
	if err != nil || len(mine) != 88 {
		t.Fatal("call key export failed on a live call-capable session")
	}
	theirs, err := right.session.ExportCallKeyMaterial(callContext(7, 1), 88)
	if err != nil || !bytes.Equal(mine, theirs) {
		t.Fatal("both ends of one session did not derive identical call key material")
	}
	if bytes.Equal(mine, make([]byte, 88)) {
		t.Fatal("exported call key material is all zero")
	}
	// The same call ID with a different answer value is a different call.
	reanswered, err := left.session.ExportCallKeyMaterial(callContext(7, 2), 88)
	if err != nil || bytes.Equal(mine, reanswered) {
		t.Fatal("a different answer value did not change the key material")
	}
	other, err := left.session.ExportCallKeyMaterial(callContext(8, 1), 88)
	if err != nil || bytes.Equal(mine, other) || bytes.Equal(reanswered, other) {
		t.Fatal("a different call ID did not change the key material")
	}
	again, _ := connect(t, a, b)
	if again.err != nil {
		t.Fatal("second authentication failed")
	}
	fresh, err := again.session.ExportCallKeyMaterial(callContext(7, 1), 88)
	if err != nil || bytes.Equal(mine, fresh) {
		t.Fatal("a different authenticated session reused call key material")
	}
}

// Media keys are used with packet counters that restart for every call. Handing
// out one context's keys twice would repeat AES-GCM nonces, so the session
// refuses, on every copy of itself and whatever length is asked for.
func TestCallKeyContextIsExportedOnlyOncePerSession(t *testing.T) {
	a, b := optionsPair(t)
	left, right := connect(t, a, b)
	if left.err != nil || right.err != nil {
		t.Fatal("saved identity authentication failed")
	}
	if _, err := left.session.ExportCallKeyMaterial(callContext(3, 4), 88); err != nil {
		t.Fatal(err)
	}
	copyOfSession := *left.session
	for name, attempt := range map[string]func() ([]byte, error){
		"same request":     func() ([]byte, error) { return left.session.ExportCallKeyMaterial(callContext(3, 4), 88) },
		"different length": func() ([]byte, error) { return left.session.ExportCallKeyMaterial(callContext(3, 4), 32) },
		"copied session":   func() ([]byte, error) { return copyOfSession.ExportCallKeyMaterial(callContext(3, 4), 88) },
	} {
		if material, err := attempt(); material != nil || !errors.Is(err, ErrAuthentication) {
			t.Errorf("%s: a key context was exported a second time", name)
		}
	}
	// The other end has its own single use of the same context.
	if _, err := right.session.ExportCallKeyMaterial(callContext(3, 4), 88); err != nil {
		t.Fatal("the peer's one export of the shared context was refused")
	}
	if material, err := right.session.ExportCallKeyMaterial(callContext(3, 4), 88); material != nil || !errors.Is(err, ErrAuthentication) {
		t.Fatal("the peer exported a key context a second time")
	}
	// A refused request must not consume the context it named.
	if material, err := left.session.ExportCallKeyMaterial(callContext(9, 9), 0); material != nil || err == nil {
		t.Fatal("a zero-length export succeeded")
	}
	if _, err := left.session.ExportCallKeyMaterial(callContext(9, 9), 88); err != nil {
		t.Fatal("an invalid request spent a key context")
	}
}

func TestCallKeyExportsAreBoundedPerSession(t *testing.T) {
	a, b := optionsPair(t)
	left, _ := connect(t, a, b)
	if left.err != nil {
		t.Fatal("saved identity authentication failed")
	}
	context := callContext(1, 1)
	for i := 0; i < MaxCallExports; i++ {
		context[0], context[1], context[2] = byte(i), byte(i>>8), 1
		if _, err := left.session.ExportCallKeyMaterial(context, 88); err != nil {
			t.Fatalf("export %d of %d was refused: %v", i+1, MaxCallExports, err)
		}
	}
	context[5] ^= 0xFF
	if material, err := left.session.ExportCallKeyMaterial(context, 88); material != nil || !errors.Is(err, ErrAuthentication) {
		t.Fatal("a session exported more call keys than its limit")
	}
}

func TestCallKeyExportRejectsInvalidRequestsAndClosedSessions(t *testing.T) {
	a, b := optionsPair(t)
	left, _ := connect(t, a, b)
	if left.err != nil {
		t.Fatal("saved identity authentication failed")
	}
	for name, attempt := range map[string]func() ([]byte, error){
		"short context": func() ([]byte, error) { return left.session.ExportCallKeyMaterial(make([]byte, CallContextSize-1), 32) },
		"long context":  func() ([]byte, error) { return left.session.ExportCallKeyMaterial(make([]byte, CallContextSize+1), 32) },
		"call ID only": func() ([]byte, error) {
			return left.session.ExportCallKeyMaterial(bytes.Repeat([]byte{1}, CallContextSize/2), 32)
		},
		"zero context": func() ([]byte, error) { return left.session.ExportCallKeyMaterial(make([]byte, CallContextSize), 32) },
		"zero call ID": func() ([]byte, error) { return left.session.ExportCallKeyMaterial(callContext(0, 1), 32) },
		"zero answer":  func() ([]byte, error) { return left.session.ExportCallKeyMaterial(callContext(1, 0), 32) },
		"zero length":  func() ([]byte, error) { return left.session.ExportCallKeyMaterial(callContext(1, 1), 0) },
		"excessive length": func() ([]byte, error) {
			return left.session.ExportCallKeyMaterial(callContext(1, 1), MaxCallKeyMaterial+1)
		},
		"nil session": func() ([]byte, error) { return (*Session)(nil).ExportCallKeyMaterial(callContext(1, 1), 32) },
	} {
		if material, err := attempt(); material != nil || !errors.Is(err, ErrAuthentication) {
			t.Errorf("%s: export was not refused", name)
		}
	}
	_ = left.session.Close()
	if material, err := left.session.ExportCallKeyMaterial(callContext(1, 1), 32); material != nil || !errors.Is(err, ErrClosed) {
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
	if material, err := got.session.ExportCallKeyMaterial(callContext(1, 1), 32); material != nil || !errors.Is(err, ErrAuthentication) {
		t.Fatal("call key material was exported without the negotiated call capability")
	}
}
