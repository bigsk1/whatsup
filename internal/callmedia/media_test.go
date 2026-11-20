package callmedia

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/srtp/v3"
)

// fakeSession stands in for an authenticated TLS session: both ends holding the
// same secret export identical bytes for a call ID, and unrelated bytes for
// any other call or session. The real exporter is tested in sessionauth.
type fakeSession struct {
	secret string
	fail   bool
	short  bool
}

func (f fakeSession) ExportCallKeyMaterial(callID []byte, length int) ([]byte, error) {
	if f.fail {
		return nil, errors.New("session closed")
	}
	var out []byte
	for block := uint32(0); len(out) < length; block++ {
		hash := sha256.New()
		hash.Write([]byte(f.secret))
		hash.Write(callID)
		_ = binary.Write(hash, binary.BigEndian, block)
		out = hash.Sum(out)
	}
	if f.short {
		return out[:length-1], nil
	}
	return out[:length], nil
}

var (
	callA = [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	callB = [16]byte{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	start = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
)

type link struct {
	sender   *Sender
	receiver *Receiver
	keys     *Keys // the sending side's keys, for crafting hostile packets
}

// newLink returns media from the inviter to the joiner for one call.
func newLink(t testing.TB, session Exporter, call [16]byte) link {
	t.Helper()
	inviter, err := Derive(session, call, true)
	if err != nil {
		t.Fatal(err)
	}
	joiner, err := Derive(session, call, false)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := NewSender(inviter)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := NewReceiver(joiner)
	if err != nil {
		t.Fatal(err)
	}
	return link{sender: sender, receiver: receiver, keys: inviter}
}

func audioFrame(i int) []byte {
	frame := bytes.Repeat([]byte{byte(i)}, 80)
	binary.BigEndian.PutUint32(frame, uint32(i))
	return frame
}

func mustOpen(t testing.TB, r *Receiver, datagram []byte, now time.Time) Received {
	t.Helper()
	got, err := r.Open(datagram, now)
	if err != nil {
		t.Fatalf("authentic datagram rejected: %v", err)
	}
	return got
}

func TestKeysAreDirectionalCallScopedAndRedacted(t *testing.T) {
	session := fakeSession{secret: "session-one"}
	forward := newLink(t, session, callA)
	datagram, err := forward.sender.Audio(audioFrame(1), 960)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(datagram, audioFrame(1)[4:]) {
		t.Fatal("audio payload is visible in the sealed datagram")
	}
	if got := mustOpen(t, forward.receiver, datagram, start); len(got.Frames) != 1 || !bytes.Equal(got.Frames[0].Data, audioFrame(1)) {
		t.Fatal("the peer could not open the datagram")
	}

	for name, keys := range map[string]func() (*Keys, error){
		"sender's own receive key": func() (*Keys, error) { return Derive(session, callA, true) },
		"another call":             func() (*Keys, error) { return Derive(session, callB, false) },
		"another session":          func() (*Keys, error) { return Derive(fakeSession{secret: "session-two"}, callA, false) },
	} {
		wrong, err := keys()
		if err != nil {
			t.Fatal(err)
		}
		receiver, _ := NewReceiver(wrong)
		fresh, _ := forward.sender.Audio(audioFrame(2), 1920)
		if got, err := receiver.Open(fresh, start); !errors.Is(err, ErrDatagram) || len(got.Frames) != 0 {
			t.Errorf("%s opened the datagram", name)
		}
	}

	for name, attempt := range map[string]func() (*Keys, error){
		"nil session":    func() (*Keys, error) { return Derive(nil, callA, true) },
		"zero call ID":   func() (*Keys, error) { return Derive(session, [16]byte{}, true) },
		"closed session": func() (*Keys, error) { return Derive(fakeSession{fail: true}, callA, true) },
		"short export":   func() (*Keys, error) { return Derive(fakeSession{secret: "x", short: true}, callA, true) },
	} {
		if keys, err := attempt(); keys != nil || !errors.Is(err, ErrKeys) {
			t.Errorf("%s: keys were derived", name)
		}
	}

	keys, _ := Derive(session, callA, true)
	if text := fmt.Sprintf("%v %+v %#v %s", keys, *keys, *keys, keys); strings.Contains(text, "key:") || !strings.Contains(text, "private") {
		t.Fatalf("formatting key material was not redacted: %s", text)
	}
	if encoded, err := json.Marshal(keys); err == nil {
		t.Fatalf("key material serialized: %s", encoded)
	}
	keys.Clear()
	if _, err := NewSender(keys); !errors.Is(err, ErrKeys) {
		t.Fatal("cleared keys were still usable")
	}
	if _, err := NewReceiver(&Keys{}); !errors.Is(err, ErrKeys) {
		t.Fatal("zero-value keys were usable")
	}
}

func TestAudioRedundancyFillsOneLostPacketWithoutDuplicates(t *testing.T) {
	l := newLink(t, fakeSession{secret: "s"}, callA)
	var datagrams [][]byte
	for i := 1; i <= 12; i++ {
		datagram, err := l.sender.Audio(audioFrame(i), uint32(i)*AudioFrameSamples)
		if err != nil {
			t.Fatal(err)
		}
		if len(datagram) > 220 {
			t.Fatalf("voice datagram is %d bytes", len(datagram))
		}
		datagrams = append(datagrams, datagram)
	}
	lost := map[int]bool{5: true, 8: true, 9: true} // one single loss, one double loss
	var frames []Frame
	for i, datagram := range datagrams {
		if lost[i+1] {
			continue
		}
		frames = append(frames, mustOpen(t, l.receiver, datagram, start.Add(time.Duration(i)*20*time.Millisecond)).Frames...)
	}
	var got []string
	for _, frame := range frames {
		n := int(binary.BigEndian.Uint32(frame.Data))
		if frame.Kind != FrameAudio || !bytes.Equal(frame.Data, audioFrame(n)) || frame.Timestamp != uint32(n)*AudioFrameSamples {
			t.Fatalf("frame %d arrived damaged or with the wrong timestamp", n)
		}
		label := fmt.Sprint(n)
		if frame.Recovered {
			label += "r"
		}
		got = append(got, label)
	}
	// 5 is rebuilt from 6. Of the double loss only 9 can be rebuilt (from 10).
	if want := "1 2 3 4 5r 6 7 9r 10 11 12"; strings.Join(got, " ") != want {
		t.Fatalf("delivered frames %q, want %q", strings.Join(got, " "), want)
	}
	// The real packet 5 turns up late: it was already delivered as a recovered copy.
	if late := mustOpen(t, l.receiver, datagrams[4], start); len(late.Frames) != 0 {
		t.Fatal("a late packet was delivered after its recovered copy")
	}
	// Packet 8 was never recovered, so a late copy is still useful.
	if late := mustOpen(t, l.receiver, datagrams[7], start); len(late.Frames) != 1 || late.Frames[0].Recovered {
		t.Fatal("a late, never-recovered packet was not delivered")
	}
	if _, err := l.receiver.Open(datagrams[7], start); !errors.Is(err, ErrDatagram) {
		t.Fatal("a replayed datagram was accepted")
	}
}

func videoFrame(seed int64, size int) []byte {
	frame := make([]byte, size)
	rand.New(rand.NewSource(seed)).Read(frame)
	return frame
}

func TestVideoIsFragmentedWithinTheDatagramLimitAndReassembled(t *testing.T) {
	l := newLink(t, fakeSession{secret: "s"}, callA)
	for i, size := range []int{1, videoFragment - 1, videoFragment, videoFragment + 1, 48000, MaxVideoFrame} {
		frame := videoFrame(int64(i), size)
		key := i%2 == 0
		datagrams, err := l.sender.Video(frame, uint32(i)*3750, key)
		if err != nil {
			t.Fatalf("%d-byte frame refused: %v", size, err)
		}
		if want := (size + videoFragment - 1) / videoFragment; len(datagrams) != want {
			t.Fatalf("%d-byte frame became %d datagrams, want %d", size, len(datagrams), want)
		}
		var frames []Frame
		for _, datagram := range datagrams {
			if len(datagram) > datagramBudget || len(datagram) > MaxDatagram {
				t.Fatalf("video datagram is %d bytes", len(datagram))
			}
			got := mustOpen(t, l.receiver, datagram, start)
			if got.NeedKeyframe {
				t.Fatal("keyframe requested with nothing lost")
			}
			frames = append(frames, got.Frames...)
		}
		if i == 0 && !key {
			t.Fatal("test must begin with a keyframe")
		}
		if len(frames) != 1 || frames[0].Kind != FrameVideo || frames[0].Key != key || frames[0].Timestamp != uint32(i)*3750 || !bytes.Equal(frames[0].Data, frame) {
			t.Fatalf("%d-byte frame was not reassembled exactly", size)
		}
	}
}

func TestVideoLossHoldsBackDeltaFramesUntilAKeyframe(t *testing.T) {
	l := newLink(t, fakeSession{secret: "s"}, callA)
	now := start
	send := func(n int, key bool, drop func(part, parts int) bool) (delivered bool, requested bool) {
		datagrams, err := l.sender.Video(videoFrame(int64(n), 3*videoFragment), uint32(n)*3750, key)
		if err != nil {
			t.Fatal(err)
		}
		for part, datagram := range datagrams {
			if drop != nil && drop(part, len(datagrams)) {
				continue
			}
			got := mustOpen(t, l.receiver, datagram, now)
			requested = requested || got.NeedKeyframe
			for _, frame := range got.Frames {
				if !bytes.Equal(frame.Data, videoFrame(int64(n), 3*videoFragment)) || frame.Key != key {
					t.Fatalf("frame %d was delivered damaged", n)
				}
				delivered = true
			}
		}
		now = now.Add(time.Second)
		return delivered, requested
	}
	none := func(int, int) bool { return false }
	all := func(int, int) bool { return true }
	middle := func(part, _ int) bool { return part == 1 }
	first := func(part, _ int) bool { return part == 0 }
	last := func(part, parts int) bool { return part == parts-1 }

	steps := []struct {
		name               string
		key                bool
		drop               func(int, int) bool
		deliver, wantReply bool
	}{
		{"keyframe", true, none, true, false},
		{"delta", false, none, true, false},
		{"delta missing its middle", false, middle, false, true},
		{"delta after the damage", false, none, false, true},
		{"keyframe repairs", true, none, true, false},
		{"delta", false, none, true, false},
		{"delta lost entirely", false, all, false, false},
		{"delta after a whole lost frame", false, none, false, true},
		{"keyframe repairs", true, none, true, false},
		{"delta missing its start", false, first, false, true},
		{"keyframe repairs", true, none, true, false},
		{"delta missing its end", false, last, false, false},
		{"delta after a lost end", false, none, false, true},
		{"keyframe missing its middle", true, middle, false, true},
		{"delta after a broken keyframe", false, none, false, true},
		{"keyframe repairs", true, none, true, false},
		{"delta", false, none, true, false},
	}
	for i, step := range steps {
		delivered, requested := send(i, step.key, step.drop)
		if delivered != step.deliver {
			t.Fatalf("step %d (%s): delivered=%v, want %v", i, step.name, delivered, step.deliver)
		}
		if requested != step.wantReply {
			t.Fatalf("step %d (%s): keyframe requested=%v, want %v", i, step.name, requested, step.wantReply)
		}
	}
}

func TestKeyframeRequestsAreRateLimited(t *testing.T) {
	l := newLink(t, fakeSession{secret: "s"}, callA)
	key, _ := l.sender.Video(videoFrame(1, 100), 0, true)
	mustOpen(t, l.receiver, key[0], start)
	_, _ = l.sender.Video(videoFrame(2, 100), 1, false) // lost entirely
	requests := 0
	for i := 0; i < 200; i++ {
		delta, _ := l.sender.Video(videoFrame(int64(3+i), 100), uint32(2+i), false)
		if mustOpen(t, l.receiver, delta[0], start.Add(time.Duration(i)*10*time.Millisecond)).NeedKeyframe {
			requests++
		}
	}
	if requests < 3 || requests > 5 {
		t.Fatalf("2 seconds of undecodable video produced %d keyframe requests, want about 4", requests)
	}
}

// craft seals an RTP packet under the sender's real key, bypassing the Sender's
// own profile, to prove the receiver enforces the profile on authentic input.
func craft(t *testing.T, keys *Keys, header rtp.Header, payload []byte) []byte {
	t.Helper()
	return craftPacket(t, keys, &rtp.Packet{Header: header, Payload: payload})
}

func craftPacket(t *testing.T, keys *Keys, packet *rtp.Packet) []byte {
	t.Helper()
	context, err := srtp.CreateContext(keys.send.key[:], keys.send.salt[:], profile)
	if err != nil {
		t.Fatal(err)
	}
	packet.Header.Version = 2
	plain, err := packet.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := context.EncryptRTP(nil, plain, nil)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func TestReceiverRejectsEverythingOutsideTheFixedProfile(t *testing.T) {
	session := fakeSession{secret: "s"}
	for name, build := range map[string]func(l link) []byte{
		"empty":     func(link) []byte { return nil },
		"too short": func(link) []byte { return make([]byte, minDatagram-1) },
		"too long":  func(link) []byte { return make([]byte, MaxDatagram+1) },
		"random":    func(link) []byte { return videoFrame(9, 200) },
		"tampered payload": func(l link) []byte {
			d, _ := l.sender.Audio(audioFrame(1), 960)
			d[20] ^= 1
			return d
		},
		"tampered header": func(l link) []byte {
			d, _ := l.sender.Audio(audioFrame(1), 960)
			d[5] ^= 1
			return d
		},
		"tampered tag": func(l link) []byte {
			d, _ := l.sender.Audio(audioFrame(1), 960)
			d[len(d)-1] ^= 1
			return d
		},
		"truncated tag": func(l link) []byte { d, _ := l.sender.Audio(audioFrame(1), 960); return d[:len(d)-1] },
		"unknown stream": func(l link) []byte {
			return craft(t, l.keys, rtp.Header{SSRC: 77, PayloadType: typeAudio, SequenceNumber: 1}, []byte{0, 1})
		},
		"audio stream with video type": func(l link) []byte {
			return craft(t, l.keys, rtp.Header{SSRC: ssrcAudio, PayloadType: typeVideo, SequenceNumber: 1}, []byte{0, 1})
		},
		"audio with marker": func(l link) []byte {
			return craft(t, l.keys, rtp.Header{SSRC: ssrcAudio, PayloadType: typeAudio, SequenceNumber: 1, Marker: true}, []byte{0, 1})
		},
		"header extension": func(l link) []byte {
			h := rtp.Header{SSRC: ssrcAudio, PayloadType: typeAudio, SequenceNumber: 1}
			_ = h.SetExtension(1, []byte{1})
			return craft(t, l.keys, h, []byte{0, 1})
		},
		"padding": func(l link) []byte {
			return craftPacket(t, l.keys, &rtp.Packet{Header: rtp.Header{SSRC: ssrcAudio, PayloadType: typeAudio, SequenceNumber: 1, Padding: true},
				Payload: []byte{0, 1}, PaddingSize: 4})
		},
		"contributing sources": func(l link) []byte {
			return craft(t, l.keys, rtp.Header{SSRC: ssrcAudio, PayloadType: typeAudio, SequenceNumber: 1, CSRC: []uint32{5}}, []byte{0, 1})
		},
	} {
		l := newLink(t, session, callA)
		if got, err := l.receiver.Open(build(l), start); !errors.Is(err, ErrDatagram) || len(got.Frames) != 0 || got.NeedKeyframe {
			t.Errorf("%s: datagram was not rejected", name)
		}
	}

	// Authentic packets whose payload breaks the format are dropped without frames.
	for name, packet := range map[string]struct {
		header  rtp.Header
		payload []byte
	}{
		"audio redundancy longer than payload": {rtp.Header{SSRC: ssrcAudio, PayloadType: typeAudio, SequenceNumber: 1}, []byte{9, 1, 2}},
		"audio with no primary frame":          {rtp.Header{SSRC: ssrcAudio, PayloadType: typeAudio, SequenceNumber: 1}, []byte{2, 1, 2}},
		"audio frame over the limit":           {rtp.Header{SSRC: ssrcAudio, PayloadType: typeAudio, SequenceNumber: 1}, append([]byte{0}, make([]byte, MaxAudioFrame+1)...)},
		"video unknown flag":                   {rtp.Header{SSRC: ssrcVideo, PayloadType: typeVideo, SequenceNumber: 1, Marker: true}, []byte{0x80 | flagStart | flagKey, 1}},
		"video empty fragment":                 {rtp.Header{SSRC: ssrcVideo, PayloadType: typeVideo, SequenceNumber: 1, Marker: true}, []byte{flagStart | flagKey}},
	} {
		l := newLink(t, session, callA)
		if got, err := l.receiver.Open(craft(t, l.keys, packet.header, packet.payload), start); err != nil || len(got.Frames) != 0 {
			t.Errorf("%s: produced frames or a hard error (%v)", name, err)
		}
	}
}

func TestRejectedDatagramsDoNotDisturbAFrameInProgress(t *testing.T) {
	l := newLink(t, fakeSession{secret: "s"}, callA)
	frame := videoFrame(4, 3*videoFragment)
	datagrams, _ := l.sender.Video(frame, 3750, true)
	mustOpen(t, l.receiver, datagrams[0], start)
	for _, garbage := range [][]byte{videoFrame(1, 300), datagrams[0], append(bytes.Clone(datagrams[1][:40]), 1, 2, 3)} {
		if _, err := l.receiver.Open(garbage, start); !errors.Is(err, ErrDatagram) {
			t.Fatal("garbage or a replay was accepted")
		}
	}
	mustOpen(t, l.receiver, datagrams[1], start)
	if got := mustOpen(t, l.receiver, datagrams[2], start); len(got.Frames) != 1 || !bytes.Equal(got.Frames[0].Data, frame) {
		t.Fatal("garbage between fragments broke the frame")
	}
}

func TestSenderRefusesEmptyAndOversizedFrames(t *testing.T) {
	l := newLink(t, fakeSession{secret: "s"}, callA)
	if _, err := l.sender.Audio(nil, 0); !errors.Is(err, ErrFrame) {
		t.Error("empty audio frame was sealed")
	}
	if _, err := l.sender.Audio(make([]byte, MaxAudioFrame+1), 0); !errors.Is(err, ErrFrame) {
		t.Error("oversized audio frame was sealed")
	}
	if _, err := l.sender.Video(nil, 0, true); !errors.Is(err, ErrFrame) {
		t.Error("empty video frame was sealed")
	}
	if _, err := l.sender.Video(make([]byte, MaxVideoFrame+1), 0, true); !errors.Is(err, ErrFrame) {
		t.Error("oversized video frame was sealed")
	}
	// A large audio frame is sent without a redundant copy, never over the limit.
	for i := 0; i < 3; i++ {
		datagram, err := l.sender.Audio(make([]byte, MaxAudioFrame), uint32(i))
		if err != nil || len(datagram) > datagramBudget {
			t.Fatalf("maximum audio frame produced a %d-byte datagram (%v)", len(datagram), err)
		}
		if got := mustOpen(t, l.receiver, datagram, start); len(got.Frames) != 1 || len(got.Frames[0].Data) != MaxAudioFrame {
			t.Fatal("maximum audio frame did not round trip")
		}
	}
	keepalive, err := l.sender.Keepalive()
	if err != nil {
		t.Fatal(err)
	}
	if got := mustOpen(t, l.receiver, keepalive, start); len(got.Frames) != 0 {
		t.Fatal("keepalive produced a media frame")
	}
}

func TestLongCallCrossesSequenceWrapAndReportsLoss(t *testing.T) {
	l := newLink(t, fakeSession{secret: "s"}, callA)
	now := start
	delivered := 0
	const total = 140000 // two wraps of the 16-bit sequence number, about 47 minutes of audio
	for i := 1; i <= total; i++ {
		datagram, err := l.sender.Audio(audioFrame(i), uint32(i)*AudioFrameSamples)
		if err != nil {
			t.Fatal(err)
		}
		now = now.Add(20 * time.Millisecond)
		if i%10 == 0 {
			continue // 10% loss, every one recoverable
		}
		got, err := l.receiver.Open(datagram, now)
		if err != nil {
			t.Fatalf("packet %d rejected after sequence wrap: %v", i, err)
		}
		for _, frame := range got.Frames {
			if n := int(binary.BigEndian.Uint32(frame.Data)); !bytes.Equal(frame.Data, audioFrame(n)) || frame.Timestamp != uint32(n)*AudioFrameSamples {
				t.Fatalf("frame %d damaged near packet %d", n, i)
			}
			delivered++
		}
		if i == 70000 {
			if stats := l.receiver.Stats(); stats.Loss != 10 || stats.JitterMS != 0 || stats.Packets != 63000 {
				t.Fatalf("first interval stats %+v, want 10%% loss and no jitter", stats)
			}
		}
	}
	if delivered != total-1 { // the very last packet's loss has no successor to carry it
		t.Fatalf("delivered %d of %d frames", delivered, total)
	}
	if stats := l.receiver.Stats(); stats.Loss < 9 || stats.Loss > 10 {
		t.Fatalf("second interval loss %d%%, want about 10", stats.Loss)
	}
	if stats := l.receiver.Stats(); stats.Loss != 0 || stats.Packets != 0 {
		t.Fatalf("empty interval reported %+v", stats)
	}
}

func FuzzReceiverOpen(f *testing.F) {
	seed := newLink(f, fakeSession{secret: "s"}, callA)
	audio, _ := seed.sender.Audio(audioFrame(1), 960)
	video, _ := seed.sender.Video(videoFrame(1, 2000), 3750, true)
	f.Add(audio)
	f.Add(video[0])
	f.Add(video[1])
	f.Add([]byte{0x80, typeAudio, 0, 1, 0, 0, 0, 0, 0, 0, 0, ssrcAudio})
	f.Add([]byte{0x90, 0xE0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 2, 0xBE, 0xDE, 0xFF, 0xFF})
	f.Fuzz(func(t *testing.T, data []byte) {
		l := newLink(t, fakeSession{secret: "s"}, callA)
		// One genuine packet first, so reassembly state exists to be corrupted.
		genuine, _ := l.sender.Video(videoFrame(1, 2000), 3750, true)
		_, _ = l.receiver.Open(genuine[0], start)
		got, err := l.receiver.Open(data, start)
		if err != nil && (len(got.Frames) != 0 || got.NeedKeyframe) {
			t.Fatal("a rejected datagram produced output")
		}
		if err == nil && !bytes.Equal(data, genuine[1]) && !bytes.Equal(data, audio) && !bytes.Equal(data, video[1]) && len(got.Frames) != 0 {
			t.Fatal("a datagram this call's sender never produced yielded a frame")
		}
	})
}
