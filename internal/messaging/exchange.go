package messaging

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"io"
	"time"

	"whatsup/internal/chat"
	"whatsup/internal/profile"
	"whatsup/internal/sessionauth"
	"whatsup/internal/transfer"
)

const (
	dataPacket        byte = 1
	pingPacket        byte = 2
	pongPacket        byte = 3
	profilePacket     byte = 4
	capacityPacket    byte = 5
	transferPacket    byte = 6
	heartbeatInterval      = 4 * time.Second
	peerTimeout            = 12 * time.Second
	writeTimeout           = 8 * time.Second
)

type packet struct {
	kind       byte
	frame      chat.Frame
	nonce      [16]byte
	name       string
	attachment transfer.Frame
	control    controlFrame
}

var errProtocol = errors.New("authenticated messaging protocol failed")

func readPacket(reader io.Reader) (packet, error) {
	return readPacketFor(reader, sessionauth.Capabilities{WireVersion: 4, Attachments: true, Control: true, Withdrawals: true})
}

func readPacketFor(reader io.Reader, capabilities sessionauth.Capabilities) (packet, error) {
	var kind [1]byte
	if _, err := io.ReadFull(reader, kind[:]); err != nil {
		return packet{}, err
	}
	p := packet{kind: kind[0]}
	switch p.kind {
	case controlPacket:
		if !capabilities.Control {
			return packet{}, errProtocol
		}
		frame, err := readControl(reader)
		p.control = frame
		return p, err
	case transferPacket:
		if !capabilities.Attachments {
			return packet{}, errProtocol
		}
		frame, err := transfer.ReadFrame(reader)
		if err == nil && !capabilities.Withdrawals && (frame.Kind == transfer.WithdrawFrame || frame.Kind == transfer.WithdrawAckFrame) {
			return packet{}, errProtocol
		}
		p.attachment = frame
		return p, err
	case dataPacket, capacityPacket:
		frame, err := chat.ReadFrame(reader)
		// A capacity refusal carries only the message ID, encoded using the
		// canonical ID-only frame shape. Its packet kind is deliberately distinct
		// from data/ACK: it can never mark a message delivered.
		if err == nil && p.kind == capacityPacket && frame.Type != chat.AckFrame {
			return packet{}, errProtocol
		}
		p.frame = frame
		return p, err
	case pingPacket, pongPacket:
		_, err := io.ReadFull(reader, p.nonce[:])
		return p, err
	case profilePacket:
		name, err := readProfile(reader)
		p.name = name
		return p, err
	default:
		return packet{}, errProtocol
	}
}

func writePacket(session *sessionauth.Session, p packet) error {
	caps := session.Capabilities()
	if p.kind == transferPacket && !caps.Attachments || p.kind == controlPacket && !caps.Control {
		return errProtocol
	}
	if p.kind == transferPacket && !caps.Withdrawals && (p.attachment.Kind == transfer.WithdrawFrame || p.attachment.Kind == transfer.WithdrawAckFrame) {
		return errProtocol
	}
	if p.kind == controlPacket {
		wire, err := encodeControl(p.control)
		if err != nil || session.SetWriteDeadline(time.Now().Add(writeTimeout)) != nil {
			return errProtocol
		}
		n, err := session.Write(wire)
		if err != nil || n != len(wire) {
			return errProtocol
		}
		return nil
	}
	if p.kind == profilePacket {
		wire, err := encodeProfile(p.name)
		if err != nil || session.SetWriteDeadline(time.Now().Add(writeTimeout)) != nil {
			return errProtocol
		}
		n, err := session.Write(wire)
		if err != nil || n != len(wire) {
			return errProtocol
		}
		return nil
	}
	if session.SetWriteDeadline(time.Now().Add(writeTimeout)) != nil {
		return errProtocol
	}
	if _, err := session.Write([]byte{p.kind}); err != nil {
		return err
	}
	if p.kind == transferPacket {
		return transfer.WriteFrame(session, p.attachment)
	}
	if p.kind == dataPacket || p.kind == capacityPacket {
		if p.kind == capacityPacket && p.frame.Type != chat.AckFrame {
			return errProtocol
		}
		return chat.WriteFrame(session, p.frame)
	}
	if p.kind != pingPacket && p.kind != pongPacket {
		return errProtocol
	}
	n, err := session.Write(p.nonce[:])
	if err != nil || n != len(p.nonce) {
		return errProtocol
	}
	return nil
}

func (m *Manager) exchange(ctx context.Context, session *sessionauth.Session, conversation *chat.Conversation) error {
	caps := session.Capabilities()
	m.mu.Lock()
	engine := m.transfers
	control := m.controls
	if control == nil || control.session != session {
		control = &controlSession{requests: make(chan *controlRequest, 1), closed: make(chan struct{}), session: session}
	}
	m.controls = control
	m.mu.Unlock()
	defer close(control.closed)
	relays := &relayExchange{manager: m, control: control, sent: map[[32]byte]bool{}, replies: map[[32]byte]bool{}}
	var removal *controlRequest
	defer func() {
		if removal != nil {
			removal.finish(false)
		}
		select {
		case request := <-control.requests:
			request.finish(false)
		default:
		}
	}()
	var epoch uint64
	var failures <-chan transfer.Failure
	var output <-chan transfer.Outbound
	if caps.Attachments && engine == nil {
		var err error
		engine, err = m.attachmentEngine(m.record.ID)
		if err != nil {
			return err
		}
	}
	if caps.Attachments {
		var err error
		epoch, err = engine.BeginWithWithdrawals(ctx, func() error { return session.WithLivePeer(m.record.Peer, func() error { return nil }) }, caps.Withdrawals)
		if err != nil {
			return err
		}
		defer engine.End(epoch)
		failures, output = engine.Failures(), engine.Output()
	}
	// A cancellation must interrupt the initial profile write as well as the
	// main select loop; otherwise a peer that stops reading could delay joining.
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()
	type received struct {
		packet packet
		err    error
	}
	incoming := make(chan received, 16)
	finished := make(chan struct{})
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		defer close(finished)
		for {
			if session.SetReadDeadline(time.Now().Add(peerTimeout)) != nil {
				select {
				case incoming <- received{err: errProtocol}:
				case <-work.Done():
				}
				_ = session.Close()
				return
			}
			p, err := readPacketFor(session, caps)
			select {
			case incoming <- received{p, err}:
			case <-work.Done():
				return
			}
			if err != nil {
				_ = session.Close()
				return
			}
		}
	}()
	defer func() { cancel(); _ = session.Close(); <-finished }()
	retry := time.NewTicker(250 * time.Millisecond)
	defer retry.Stop()
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	pathSamples := time.NewTicker(PathSampleInterval)
	defer pathSamples.Stop()
	m.samplePath()
	var pendingNonce [16]byte
	var pingAt time.Time
	var sentRevision uint64
	sendProfile := func() error {
		local, err := m.application.Profile()
		if err != nil {
			return err
		}
		if local.Revision == sentRevision {
			return nil
		}
		name := ""
		if local.ShareDisplayName {
			name = local.DisplayName
		}
		if profile.ValidateName(name) != nil || writePacket(session, packet{kind: profilePacket, name: name}) != nil {
			return errProtocol
		}
		sentRevision = local.Revision
		return nil
	}
	var profileLimit profileBudget
	sent := make(map[string]bool)
	sendDue := func() (bool, error) {
		m.mu.Lock()
		blocked := m.removing || m.peerRemoved
		m.mu.Unlock()
		if blocked {
			return false, nil
		}
		frame, due, err := conversation.NextAttempt(m.record.Peer)
		if err != nil {
			return false, err
		}
		if due {
			if err := writePacket(session, packet{kind: dataPacket, frame: frame}); err != nil {
				return false, err
			}
			sent[frame.ID] = true
		}
		return due, nil
	}
	if err := sendProfile(); err != nil {
		return err
	}
	if err := relays.sync(); err != nil {
		return err
	}
	if _, err := sendDue(); err != nil {
		return err
	}
	handleReceived := func(got received) error {
		if got.err != nil {
			return errProtocol
		}
		switch got.packet.kind {
		case controlPacket:
			if !caps.Control {
				return errProtocol
			}
			frame := got.packet.control
			if frame.kind >= routeReadyControl {
				return relays.receive(frame)
			}
			if frame.kind == removedReceiptControl {
				// This receipt came from this session's authenticated reader and
				// may be queued just before the peer closes. It only concludes the
				// exact outstanding notice; it grants no new application authority.
				if removal == nil || frame.id != removal.frame.id {
					return errProtocol
				}
				removal.finish(true)
				return errRemovalSent
			}
			if session.WithLivePeer(m.record.Peer, func() error { return nil }) != nil {
				return errProtocol
			}
			if frame.kind != removedControl {
				return errProtocol
			}
			if err := m.application.MarkPeerRemoved(m.record, frame.id); err != nil {
				return err
			}
			m.mu.Lock()
			m.peerRemoved = true
			m.state = "peer-removed"
			m.bumpLocked()
			m.mu.Unlock()
			// Durable marker precedes receipt; a lost receipt cannot revive retries.
			_ = writePacket(session, packet{kind: controlPacket, control: controlFrame{kind: removedReceiptControl, id: frame.id}})
			return errPeerRemoved
		case capacityPacket:
			if !sent[got.packet.frame.ID] {
				return errProtocol
			}
			return errPeerCapacity
		case profilePacket:
			unchanged := profileLimit.seen && profileLimit.last == got.packet.name
			if !profileLimit.accept(got.packet.name, time.Now()) || session.WithLivePeer(m.record.Peer, func() error { return nil }) != nil {
				return errProtocol
			}
			if unchanged {
				return nil
			}
			if err := m.application.SetPeerDisplayName(m.record.ID, m.record.Peer, got.packet.name); err != nil {
				return err
			}
			m.changed("connected")
		case pingPacket:
			if writePacket(session, packet{kind: pongPacket, nonce: got.packet.nonce}) != nil {
				return errProtocol
			}
		case pongPacket:
			if pingAt.IsZero() || subtle.ConstantTimeCompare(pendingNonce[:], got.packet.nonce[:]) != 1 {
				return errProtocol
			}
			pingAt = time.Time{}
			clear(pendingNonce[:])
		case transferPacket:
			// The session guard is bounded and performs no file IO. The
			// attachment actor rechecks it before each repository operation.
			if session.WithLivePeer(m.record.Peer, func() error { return nil }) != nil || engine.Submit(epoch, got.packet.attachment) != nil {
				return errProtocol
			}
		case dataPacket:
			frame := got.packet.frame
			switch frame.Type {
			case chat.MessageFrame:
				ack, inserted, err := conversation.ReceiveCommitted(m.record.Peer, frame)
				if errors.Is(err, chat.ErrCapacity) {
					// Best-effort refusal over this exact authenticated session.
					// Neither side acknowledges, evicts or loses the queued text.
					_ = writePacket(session, packet{kind: capacityPacket, frame: chat.Frame{Version: chat.ProtocolVersion, Type: chat.AckFrame, ID: frame.ID}})
					return err
				}
				if err != nil {
					return err
				}
				m.changed("connected")
				if inserted {
					m.notifyIncoming(frame.ID)
				}
				if writePacket(session, packet{kind: dataPacket, frame: ack}) != nil {
					return errProtocol
				}
			case chat.AckFrame:
				if err := conversation.Acknowledge(m.record.Peer, frame); err != nil {
					return err
				}
				delete(sent, frame.ID)
				m.changed("connected")
			default:
				return errProtocol
			}
		default:
			return errProtocol
		}
		return nil
	}
	var pendingTransfer *transfer.Outbound
	startRemoval := func(request *controlRequest) error {
		if removal != nil || !caps.Control || request.ctx.Err() != nil {
			request.finish(false)
			return nil
		}
		removal = request
		if session.WithLivePeer(m.record.Peer, func() error { return nil }) != nil {
			return errProtocol
		}
		return writePacket(session, packet{kind: controlPacket, control: request.frame})
	}
	for {
		// Controls have priority over text and file chunks. All bytes still have
		// one writer, so Forget can never interleave with another frame body.
		select {
		case request := <-control.requests:
			if err := startRemoval(request); err != nil {
				return err
			}
			continue
		default:
		}
		if removal != nil && removal.ctx.Err() != nil {
			return errRemovalSent
		}
		if pendingTransfer != nil {
			// Drain control and chat packets between writes, then exhaust due
			// chat frames before writing one bounded attachment chunk. Slow file
			// staging/hash verification never runs in this TLS writer.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case request := <-control.requests:
				if err := startRemoval(request); err != nil {
					return err
				}
				continue
			case failed := <-failures:
				if failed.Epoch == epoch {
					return failed.Err
				}
			case got := <-incoming:
				if err := handleReceived(got); err != nil {
					return err
				}
				continue
			default:
			}
			if due, err := sendDue(); err != nil {
				return err
			} else if due {
				continue
			}
			if removal == nil && pendingTransfer.Epoch == epoch {
				if err := writePacket(session, packet{kind: transferPacket, attachment: pendingTransfer.Frame}); err != nil {
					return err
				}
				if err := engine.Written(epoch, pendingTransfer.Frame); err != nil {
					return err
				}
			}
			pendingTransfer = nil
			continue
		}
		select {
		case request := <-control.requests:
			if err := startRemoval(request); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		case got := <-incoming:
			if err := handleReceived(got); err != nil {
				return err
			}
		case failed := <-failures:
			if failed.Epoch == epoch {
				return failed.Err
			}
		case outbound := <-output:
			if outbound.Epoch == epoch {
				pendingTransfer = &outbound
			}
		case <-m.wake:
			if err := relays.sync(); err != nil {
				return err
			}
			if err := sendProfile(); err != nil {
				return err
			}
			if engine != nil && caps.Attachments && removal == nil {
				engine.Wake()
			}
			if _, err := sendDue(); err != nil {
				return err
			}
		case <-pathSamples.C:
			m.samplePath()
			if engine != nil && caps.Attachments && removal == nil {
				engine.Wake()
			}
		case <-retry.C:
			if !pingAt.IsZero() && time.Since(pingAt) > peerTimeout {
				return errProtocol
			}
			if err := sendProfile(); err != nil {
				return err
			}
			if err := relays.sync(); err != nil {
				return err
			}
			if _, err := sendDue(); err != nil {
				return err
			}
		case <-heartbeat.C:
			if pingAt.IsZero() {
				if _, err := rand.Read(pendingNonce[:]); err != nil {
					return errProtocol
				}
				pingAt = time.Now()
				if writePacket(session, packet{kind: pingPacket, nonce: pendingNonce}) != nil {
					return errProtocol
				}
			}
		}
	}
}
