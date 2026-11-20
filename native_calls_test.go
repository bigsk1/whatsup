package main

import (
	"bytes"
	"testing"

	"whatsup/internal/callmedia"
	"whatsup/internal/desktop"
)

func TestCallStreamFramesFromTheInterfaceAreBoundedAndTyped(t *testing.T) {
	audio := func(n int) []byte {
		return append([]byte{callStreamAudio, 0, 0, 3, 192}, bytes.Repeat([]byte{7}, n)...)
	}
	video := func(flags byte, n int) []byte {
		return append([]byte{callStreamVideo, flags, 0, 0, 3, 192}, bytes.Repeat([]byte{7}, n)...)
	}
	accepted := map[string]struct {
		frame []byte
		kind  byte
	}{
		"smallest audio":  {audio(1), callStreamAudio},
		"largest audio":   {audio(callmedia.MaxAudioFrame), callStreamAudio},
		"video delta":     {video(0, 900), callStreamVideo},
		"video keyframe":  {video(callFlagKey, 40000), callStreamVideo},
		"largest video":   {video(callFlagKey, callmedia.MaxVideoFrame), callStreamVideo},
		"smallest video":  {video(0, 1), callStreamVideo},
		"audio, any data": {append([]byte{callStreamAudio, 255, 255, 255, 255}, 2), callStreamAudio},
	}
	for name, item := range accepted {
		if kind, ok := callStreamFrame(item.frame); !ok || kind != item.kind {
			t.Errorf("%s: kind %d ok %v, want kind %d", name, kind, ok, item.kind)
		}
	}
	refused := map[string][]byte{
		"empty":                    {},
		"hello after the first":    append([]byte{callStreamHello}, bytes.Repeat([]byte{65}, 22)...),
		"keyframe signal upstream": {callStreamKeyframe},
		"unknown kind":             {9, 0, 0, 0, 0, 1},
		"audio with no data":       audio(0),
		"audio too large":          audio(callmedia.MaxAudioFrame + 1),
		"video with no data":       video(0, 0),
		"video too large":          video(0, callmedia.MaxVideoFrame+1),
		"video with unknown flags": video(2, 10),
		"video header only":        {callStreamVideo, 0, 0},
	}
	for name, frame := range refused {
		if _, ok := callStreamFrame(frame); ok {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestCallStreamFramesToTheInterfaceCarryKindFlagsAndTimestamp(t *testing.T) {
	cases := []struct {
		name  string
		frame desktop.CallMediaFrame
		want  []byte
	}{
		{"audio", desktop.CallMediaFrame{Timestamp: 960, Data: []byte{5, 6}}, []byte{callStreamAudio, 0, 0, 0, 3, 192, 5, 6}},
		{"recovered audio", desktop.CallMediaFrame{Recovered: true, Timestamp: 960, Data: []byte{5}}, []byte{callStreamAudio, callFlagRecovered, 0, 0, 3, 192, 5}},
		{"video delta", desktop.CallMediaFrame{Video: true, Timestamp: 90000, Data: []byte{1, 2, 3}}, []byte{callStreamVideo, 0, 0, 1, 95, 144, 1, 2, 3}},
		{"video keyframe", desktop.CallMediaFrame{Video: true, Key: true, Timestamp: 1, Data: []byte{9}}, []byte{callStreamVideo, callFlagKey, 0, 0, 0, 1, 9}},
		// Flags of one kind never leak into the other.
		{"video marked recovered", desktop.CallMediaFrame{Video: true, Recovered: true, Timestamp: 1, Data: []byte{9}}, []byte{callStreamVideo, 0, 0, 0, 0, 1, 9}},
		{"audio marked key", desktop.CallMediaFrame{Key: true, Timestamp: 1, Data: []byte{9}}, []byte{callStreamAudio, 0, 0, 0, 0, 1, 9}},
	}
	for _, item := range cases {
		if got := callStreamOut(item.frame); !bytes.Equal(got, item.want) {
			t.Errorf("%s: wire %v, want %v", item.name, got, item.want)
		}
	}
}
