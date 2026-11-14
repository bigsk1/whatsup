package profile

import (
	"strings"
	"testing"
)

func TestDisplayNamesBoundLengthAndRejectDisplaySpoofing(t *testing.T) {
	for _, name := range []string{"", "Alex", "Alex's laptop", "مرحبا", "שלום", "日本語", "cafe\u0301", "Alex 👋", strings.Repeat("x", MaxNameRunes), strings.Repeat("\U0001f600", MaxNameRunes)} {
		if err := ValidateName(name); err != nil {
			t.Fatalf("ordinary display name rejected: %v", err)
		}
	}
	for _, name := range []string{" Alex", "Alex ", "\u00a0Alex", strings.Repeat("x", MaxNameRunes+1), strings.Repeat("\U0001f600", MaxNameRunes+1), string([]byte{'a', 0xff})} {
		if ValidateName(name) == nil {
			t.Fatal("noncanonical, overlong or invalid UTF-8 name accepted")
		}
	}
	for _, hidden := range []rune{0, '\t', '\n', '\r', 0x7f, 0x85, 0x00ad, 0x034f, 0x061c, 0x115f, 0x1160, 0x17b4, 0x17b5, 0x200b, 0x200c, 0x200d, 0x2028, 0x2029, 0x202e, 0x2066, 0x2069, 0x2800, 0x3164, 0xfeff, 0xffa0} {
		if ValidateName("Alex"+string(hidden)+"device") == nil {
			t.Errorf("hidden/control rune U+%04X accepted", hidden)
		}
	}
}
