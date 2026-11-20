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

// callMediaStream carries one answered call's encoded audio between the
// interface and Go. It rides the app's own origin through Wails Streams, so
// no local port is opened. The stream is useless without an answered call:
// its first frame must name one, and Go checks that before anything else.
const callMediaStream = "whatsup-call-media"

const (
	callStreamHello = 0 // interface to Go: 22-character call ID
	callStreamAudio = 1 // both ways: encoded audio

	callHelloSize   = 1 + 22
	callAudioHeader = 1 + 4 // kind, timestamp
	callOutHeader   = 1 + 1 + 4

	callFlagRecovered = 1
)

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
				case frame := <-session.Frames():
					wire := make([]byte, callOutHeader, callOutHeader+len(frame.Data))
					wire[0] = callStreamAudio
					if frame.Recovered {
						wire[1] = callFlagRecovered
					}
					binary.BigEndian.PutUint32(wire[2:6], frame.Timestamp)
					wire = append(wire, frame.Data...)
					// A full queue means the interface is not keeping up;
					// late audio is worthless, so the frame is dropped.
					if err := conn.TrySend(wire); err != nil && !errors.Is(err, application.ErrStreamFull) {
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
			if len(frame) <= callAudioHeader || len(frame) > callAudioHeader+callmedia.MaxAudioFrame || frame[0] != callStreamAudio {
				return
			}
			if session.SendAudio(binary.BigEndian.Uint32(frame[1:5]), frame[callAudioHeader:]) != nil {
				return
			}
		}
	})
}

// setCallCapture opens the microphone gate for an answered call and closes it
// afterwards.
//
// The window's permission table says deny except between an answer and the
// end of that call. Each request is answered from that table at the moment it
// is made: by the pinned Wails for WebKitGTK, which has no prompt of its own,
// and by the reviewed Wails patch for WebView2, which would otherwise copy the
// table once and save an Allow in the profile for good. The change is made on
// the main thread, where the table is read.
//
// The argument is only a signal that the answer may have changed. What is
// written is whatever desktop.CallCaptureAllowed says when the update runs on
// the main thread, so an update that was queued for an earlier call state and
// runs late, or after this function gave up waiting for it, cannot reopen the
// gate: it applies the present truth like any other.
func (h *nativeHost) setCallCapture(bool) {
	if h == nil || h.permissions == nil || h.life.Err() != nil {
		return
	}
	update := captureGateUpdate(h.permissions, func() bool { return desktop.CallCaptureAllowed(h.backend) })
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
