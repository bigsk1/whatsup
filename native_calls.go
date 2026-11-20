package main

import (
	"encoding/binary"
	"errors"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"whatsup/internal/callmedia"
	"whatsup/internal/desktop"
)

// callMediaStream carries one answered call's encoded audio and video between
// the interface and Go. It rides the app's own origin through Wails Streams, so
// no local port is opened. The stream is useless without an answered call:
// its first frame must name one, and Go checks that before anything else.
const callMediaStream = "whatsup-call-media"

const (
	callStreamHello    = 0 // interface to Go: 22-character call ID
	callStreamAudio    = 1 // both ways: encoded audio
	callStreamVideo    = 2 // both ways: one encoded video frame
	callStreamKeyframe = 3 // Go to interface: encode a keyframe now

	callHelloSize   = 1 + 22
	callAudioHeader = 1 + 4     // kind, timestamp
	callVideoHeader = 1 + 1 + 4 // kind, flags, timestamp
	callOutHeader   = 1 + 1 + 4

	callFlagRecovered = 1 // audio, Go to interface
	callFlagKey       = 1 // video, both ways
)

// callStreamFrame checks one frame from the interface and reports what it is.
// Anything else ends the stream: the interface is bundled code, so a malformed
// frame is a fault, not something to skip past.
func callStreamFrame(frame []byte) (kind byte, ok bool) {
	if len(frame) == 0 {
		return 0, false
	}
	switch frame[0] {
	case callStreamAudio:
		return callStreamAudio, len(frame) > callAudioHeader && len(frame) <= callAudioHeader+callmedia.MaxAudioFrame
	case callStreamVideo:
		return callStreamVideo, len(frame) > callVideoHeader && len(frame) <= callVideoHeader+callmedia.MaxVideoFrame && frame[1]&^callFlagKey == 0
	}
	return 0, false
}

// callStreamOut lays out one received frame for the interface.
func callStreamOut(frame desktop.CallMediaFrame) []byte {
	wire := make([]byte, callOutHeader, callOutHeader+len(frame.Data))
	wire[0] = callStreamAudio
	if frame.Video {
		wire[0] = callStreamVideo
		if frame.Key {
			wire[1] = callFlagKey
		}
	} else if frame.Recovered {
		wire[1] = callFlagRecovered
	}
	binary.BigEndian.PutUint32(wire[2:6], frame.Timestamp)
	return append(wire, frame.Data...)
}

func registerCallMedia(app *application.App, service *desktop.Service) {
	app.HandleStream(callMediaStream, func(conn *application.StreamConn) {
		defer conn.Close()
		hello, err := conn.Receive()
		if err != nil || len(hello) != callHelloSize || hello[0] != callStreamHello {
			return
		}
		session, err := desktop.AttachCallMedia(service, string(hello[1:]))
		if err != nil {
			return
		}
		defer session.Close()
		finished := make(chan struct{})
		var writer sync.WaitGroup
		writer.Add(1)
		go func() {
			defer writer.Done()
			for {
				select {
				case <-finished:
					return
				case <-conn.Context().Done():
					return
				case <-session.Done():
					// The call ended or another stream replaced this one.
					_ = conn.Close()
					return
				case <-session.KeyframeNeeded():
					if err := conn.TrySend([]byte{callStreamKeyframe}); err != nil && !errors.Is(err, application.ErrStreamFull) {
						return
					}
				case frame := <-session.Frames():
					// A full queue means the interface is not keeping up;
					// late media is worthless, so the frame is dropped. A
					// dropped video frame is recovered by the next keyframe.
					if err := conn.TrySend(callStreamOut(frame)); err != nil && !errors.Is(err, application.ErrStreamFull) {
						return
					}
				}
			}
		}()
		defer func() { close(finished); writer.Wait() }()
		for {
			frame, err := conn.Receive()
			if err != nil {
				return
			}
			switch kind, ok := callStreamFrame(frame); {
			case !ok:
				return
			case kind == callStreamAudio:
				if session.SendAudio(binary.BigEndian.Uint32(frame[1:5]), frame[callAudioHeader:]) != nil {
					return
				}
			case kind == callStreamVideo:
				if session.SendVideo(binary.BigEndian.Uint32(frame[2:6]), frame[1]&callFlagKey != 0, frame[callVideoHeader:]) != nil {
					return
				}
			}
		}
	})
}

// setCallCapture opens the microphone gate for an answered call, and the
// camera gate while this device's camera is switched on in an answered video
// call, and closes them afterwards.
//
// The window's permission table says deny except in those windows. Each request is answered from that table at the moment it
// is made: by the pinned Wails for WebKitGTK, which has no prompt of its own,
// and by the reviewed Wails patch for WebView2, which would otherwise copy the
// table once and save an Allow in the profile for good. The change is made on
// the main thread, where the table is read.
//
// The argument is only a signal that the answer may have changed. What is
// written is whatever desktop.CallCaptureAllowed and desktop.CallCameraAllowed
// say when the update runs on the main thread, so an update that was queued for an earlier call state and
// runs late, or after this function gave up waiting for it, cannot reopen the
// gate: it applies the present truth like any other.
func (h *nativeHost) setCallCapture(bool) {
	if h == nil || h.permissions == nil || h.life.Err() != nil {
		return
	}
	update := captureGateUpdate(h.permissions,
		func() bool { return desktop.CallCaptureAllowed(h.backend) },
		func() bool { return desktop.CallCameraAllowed(h.backend) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		application.InvokeSync(update)
	}()
	select {
	case <-done:
	case <-h.life.Done():
	case <-time.After(2 * time.Second):
	}
}

// incomingCall brings the window forward once for a new ringing call, so a
// call is not missed while the app sits in the tray.
func (h *nativeHost) incomingCall() {
	if h == nil || h.life.Err() != nil || h.foreground() {
		return
	}
	// Called from the messaging notification goroutine: never block it.
	go h.focus()
}
