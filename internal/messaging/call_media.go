package messaging

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"whatsup/internal/call"
	"whatsup/internal/callmedia"
	"whatsup/internal/sessionauth"
)

const (
	// Until the first authentic datagram arrives, liveness datagrams are sent
	// this often to bring the flow up on both sides.
	callMediaProbeInterval = 100 * time.Millisecond
	// Once media flows, a liveness datagram is sent only after this much
	// silence, for example while the microphone is muted on a voice call.
	callMediaIdleInterval = time.Second
	callMediaReadSlice    = 250 * time.Millisecond
	callMediaReportEvery  = time.Second
)

type callMediaOpener interface {
	OpenCallMedia(context.Context) (net.Conn, error)
}

type callMediaEvent struct {
	call     call.ID
	ready    bool
	failed   bool
	keyframe bool
	report   *call.Report
}

// callMediaRun owns one call's datagram flow, keys and loops. Nothing about
// it outlives stop: the flow is closed, both goroutines have returned and the
// key bytes held here are cleared.
type callMediaRun struct {
	plan   call.Plan
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	sender   *callmedia.Sender
	conn     net.Conn
	lastSent time.Time
}

func startCallMedia(parent context.Context, session *sessionauth.Session, raw net.Conn, plan call.Plan, inviter bool, contactID string, sink CallSink, events chan<- callMediaEvent, stall time.Duration) *callMediaRun {
	ctx, cancel := context.WithCancel(parent)
	run := &callMediaRun{plan: plan, cancel: cancel, done: make(chan struct{})}
	go run.loop(ctx, session, raw, inviter, contactID, sink, events, stall)
	return run
}

func (r *callMediaRun) stop() {
	r.cancel()
	<-r.done
}

func (r *callMediaRun) write(seal func(*callmedia.Sender) ([][]byte, error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sender == nil || r.conn == nil {
		return ErrUnavailable
	}
	datagrams, err := seal(r.sender)
	if err != nil {
		return ErrUnavailable
	}
	for _, datagram := range datagrams {
		// A dropped datagram is ordinary loss; the receiver's stall timer, not
		// a write error, decides when a call has failed.
		_, _ = r.conn.Write(datagram)
	}
	r.lastSent = time.Now()
	return nil
}

func (r *callMediaRun) sendAudio(frame []byte, timestamp uint32) error {
	return r.write(func(sender *callmedia.Sender) ([][]byte, error) {
		datagram, err := sender.Audio(frame, timestamp)
		return [][]byte{datagram}, err
	})
}

func (r *callMediaRun) sendVideo(frame []byte, timestamp uint32, key bool) error {
	return r.write(func(sender *callmedia.Sender) ([][]byte, error) { return sender.Video(frame, timestamp, key) })
}

func (r *callMediaRun) keepalive(force bool) {
	r.mu.Lock()
	idle := time.Since(r.lastSent) >= callMediaIdleInterval
	r.mu.Unlock()
	if !force && !idle {
		return
	}
	_ = r.write(func(sender *callmedia.Sender) ([][]byte, error) {
		datagram, err := sender.Keepalive()
		return [][]byte{datagram}, err
	})
}

func (r *callMediaRun) loop(ctx context.Context, session *sessionauth.Session, raw net.Conn, inviter bool, contactID string, sink CallSink, events chan<- callMediaEvent, stall time.Duration) {
	defer close(r.done)
	emit := func(event callMediaEvent) {
		event.call = r.plan.Call
		select {
		case events <- event:
		case <-ctx.Done():
		}
	}
	opener, ok := raw.(callMediaOpener)
	if !ok {
		emit(callMediaEvent{failed: true})
		return
	}
	// Keys come from this exact authenticated session and this call's key
	// context, half of which is this device's own fresh randomness. The
	// session exports a context only once, so these keys are new. They are
	// derived before the port opens, so no datagram is handled without them.
	keys, err := callmedia.Derive(session, r.plan.KeyContext(), inviter)
	if err != nil {
		emit(callMediaEvent{failed: true})
		return
	}
	defer keys.Clear()
	sender, err := callmedia.NewSender(keys)
	if err != nil {
		emit(callMediaEvent{failed: true})
		return
	}
	receiver, err := callmedia.NewReceiver(keys)
	if err != nil {
		emit(callMediaEvent{failed: true})
		return
	}
	conn, err := opener.OpenCallMedia(ctx)
	if err != nil {
		emit(callMediaEvent{failed: true})
		return
	}
	r.mu.Lock()
	r.sender, r.conn = sender, conn
	r.mu.Unlock()
	var ready atomic.Bool
	writing, stopWriting := context.WithCancel(ctx)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		tick := time.NewTicker(callMediaProbeInterval)
		defer tick.Stop()
		for {
			r.keepalive(!ready.Load())
			select {
			case <-writing.Done():
				return
			case <-tick.C:
			}
		}
	}()
	defer func() {
		stopWriting()
		<-writerDone
		r.mu.Lock()
		r.sender, r.conn = nil, nil
		r.mu.Unlock()
		_ = conn.Close()
	}()
	// Closing the flow is what interrupts a blocked Read when the call ends.
	stopRead := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopRead()

	buffer := make([]byte, callmedia.MaxDatagram+1)
	lastAuthentic, lastReport := time.Now(), time.Now()
	for ctx.Err() == nil {
		_ = conn.SetReadDeadline(time.Now().Add(callMediaReadSlice))
		n, err := conn.Read(buffer)
		now := time.Now()
		if err != nil {
			var timeout net.Error
			if !errors.Is(err, os.ErrDeadlineExceeded) && !(errors.As(err, &timeout) && timeout.Timeout()) {
				if ctx.Err() == nil {
					emit(callMediaEvent{failed: true})
				}
				return
			}
		} else if got, err := receiver.Open(buffer[:n], now); err == nil {
			lastAuthentic = now
			if !ready.Swap(true) {
				emit(callMediaEvent{ready: true})
			}
			if sink != nil {
				for _, frame := range got.Frames {
					if frame.Kind == callmedia.FrameVideo && !r.plan.Video {
						continue
					}
					sink.CallFrame(contactID, r.plan.Call, frame)
				}
			}
			if got.NeedKeyframe && r.plan.Video {
				emit(callMediaEvent{keyframe: true})
			}
		}
		// Only authentic datagrams count as life. Garbage cannot keep a dead call open.
		if now.Sub(lastAuthentic) > stall {
			emit(callMediaEvent{failed: true})
			return
		}
		if ready.Load() && now.Sub(lastReport) >= callMediaReportEvery {
			lastReport = now
			stats := receiver.Stats()
			emit(callMediaEvent{report: &call.Report{Loss: stats.Loss, JitterMS: stats.JitterMS}})
		}
	}
}
