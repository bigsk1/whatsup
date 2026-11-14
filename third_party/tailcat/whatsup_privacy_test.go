package tailcat

import (
	"bytes"
	"log"
	"testing"

	"tailscale.com/types/key"
)

// This deliberately exercises the exact helper used by createEngine without
// creating sockets, probing a relay or starting an engine.
func TestWhatsUpDERPApplicationStatisticsOptOut(t *testing.T) {
	for _, server := range []bool{false, true} {
		for _, disabled := range []bool{false, true} {
			backend := &locoBackend{isServer: server, disableDERPApplicationStats: disabled}
			want := "tailcat-client"
			if server {
				want = "tailcat-server"
			}
			if disabled {
				want = ""
			}
			if backend.derpApplicationName() != want {
				t.Fatal("DERP application-name opt-out or upstream default changed")
			}
		}
	}
	if WhatsUpPrivacyPatchVersion != 2 {
		t.Fatal("privacy patch version changed without reviewing the guard")
	}
}

func TestWhatsUpClientStatusDoesNotInitializeOrStart(t *testing.T) {
	client := &Client{Server: "invalid status-only sentinel"}
	for range 3 {
		if client.Status() != nil || client.lb != nil || client.started || !client.key.IsZero() {
			t.Fatal("read-only client status performed lazy initialization")
		}
	}
	client.started = true
	if client.Status() != nil {
		t.Fatal("missing client backend produced status")
	}
}

func TestWhatsUpInheritedAddressDebugCannotLogBearer(t *testing.T) {
	t.Setenv("TS_DEBUG_ADDR", "true")
	var captured bytes.Buffer
	original := log.Writer()
	log.SetOutput(&captured)
	defer log.SetOutput(original)
	info := ConnInfo{
		ServerPublic:      NodePublic{key.NewNode().Public()},
		ServerDiscoPublic: DiscoPublic{key.NewDisco().Public()},
		PresharedKey:      NewPresharedKey(), RegionID: 1,
	}
	if info.Addr() == "" || debugAddr || captured.Len() != 0 {
		t.Fatal("address construction enabled debug logging of a bearer credential")
	}
}
