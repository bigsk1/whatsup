package main

import "testing"

func TestNativeFatalExitCannotMasqueradeAsDisplayFailure(t *testing.T) {
	// Go's fatal CGO signal path can itself exit 2. Only an explicitly encoded
	// normal return from our native probe may mean that no display was present.
	for _, status := range []int{-1, 0, 1, 2, 127, 128 + 5} {
		if decodeChildExit(status) != probeNativeFailed {
			t.Fatal("native process failure accepted as a normal probe result")
		}
	}
	if decodeChildExit(probeChildBase+probeNoDisplay) != probeNoDisplay {
		t.Fatal("genuine missing-display result lost")
	}
	if decodeChildExit(probeChildBase+probePassed) != probePassed {
		t.Fatal("successful native return lost")
	}
}
