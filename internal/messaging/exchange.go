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
)

const (
	dataPacket        byte = 1
	pingPacket        byte = 2
	pongPacket        byte = 3
	profilePacket     byte = 4
	heartbeatInterval      = 4 * time.Second
	peerTimeout            = 12 * time.Second
	writeTimeout           = 8 * time.Second
)

type packet struct {
	kind  byte
	frame chat.Frame
	nonce [16]byte
	name  string
}

var errProtocol = errors.New("authenticated messaging protocol failed")

func readPacket(reader io.Reader) (packet, error) {
	var kind [1]byte
	if _, err := io.ReadFull(reader, kind[:]); err != nil {
		return packet{}, err
	}
	p := packet{kind: kind[0]}
	switch p.kind {
	case dataPacket:
		frame, err := chat.ReadFrame(reader)
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
	if p.kind == dataPacket {
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
			p, err := readPacket(session)
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
	sendDue := func() error {
		frame, due, err := conversation.NextAttempt(m.record.Peer)
		if err != nil {
			return err
		}
		if due {
			return writePacket(session, packet{kind: dataPacket, frame: frame})
		}
		return nil
	}
	if err := sendProfile(); err != nil {
		return err
	}
	if err := sendDue(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case got := <-incoming:
			if got.err != nil {
				return errProtocol
			}
			switch got.packet.kind {
			case profilePacket:
				unchanged := profileLimit.seen && profileLimit.last == got.packet.name
				if !profileLimit.accept(got.packet.name, time.Now()) || session.WithLivePeer(m.record.Peer, func() error { return nil }) != nil {
					return errProtocol
				}
				if unchanged {
					continue
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
			case dataPacket:
				frame := got.packet.frame
				switch frame.Type {
				case chat.MessageFrame:
					ack, err := conversation.Receive(m.record.Peer, frame)
					if err != nil {
						return err
					}
					m.changed("connected")
					if writePacket(session, packet{kind: dataPacket, frame: ack}) != nil {
						return errProtocol
					}
				case chat.AckFrame:
					if err := conversation.Acknowledge(m.record.Peer, frame); err != nil {
						return err
					}
					m.changed("connected")
				default:
					return errProtocol
				}
			default:
				return errProtocol
			}
		case <-m.wake:
			if err := sendProfile(); err != nil {
				return err
			}
			if err := sendDue(); err != nil {
				return err
			}
		case <-retry.C:
			if !pingAt.IsZero() && time.Since(pingAt) > peerTimeout {
				return errProtocol
			}
			if err := sendProfile(); err != nil {
				return err
			}
			if err := sendDue(); err != nil {
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
