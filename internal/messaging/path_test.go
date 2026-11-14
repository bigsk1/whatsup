package messaging

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"whatsup/internal/transport"
)

type observedPathConn struct {
	net.Conn
	path  atomic.Value
	calls atomic.Int32
}

func (c *observedPathConn) Path() transport.Path {
	c.calls.Add(1)
	return c.path.Load().(transport.Path)
}
func (c *observedPathConn) Close() error { return nil }

func pathManager(t *testing.T) (*Manager, *observedPathConn) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	conn := &observedPathConn{}
	conn.path.Store(transport.PathDirect)
	m := &Manager{&managerState{ctx: ctx, cancel: cancel, state: "connected", raw: conn, path: transport.PathUnknown, revision: 1}}
	t.Cleanup(m.Close)
	return m, conn
}

func TestPathSamplingRequiresAuthenticationAndResetsOnDisconnect(t *testing.T) {
	m, source := pathManager(t)
	m.samplePath()
	if source.calls.Load() != 0 {
		t.Fatal("unauthenticated path source was sampled")
	}
	m.mu.Lock()
	m.sessionActive = true
	m.mu.Unlock()
	m.samplePath()
	m.mu.Lock()
	path := m.pathLocked(time.Now())
	revision := m.revision
	m.mu.Unlock()
	if path != transport.PathDirect || source.calls.Load() != 1 {
		t.Fatal("authenticated path did not sample")
	}
	source.path.Store(transport.PathRelayed)
	m.samplePath()
	m.mu.Lock()
	if m.pathLocked(time.Now()) != transport.PathRelayed || m.revision <= revision {
		t.Fatal("live path transition was not published")
	}
	m.pathAt = time.Now().Add(-PathMaxAge)
	if m.pathLocked(time.Now()) != transport.PathUnknown {
		t.Fatal("stale path remained live")
	}
	m.sessionActive = false
	m.clearPathLocked()
	if m.pathLocked(time.Now()) != transport.PathUnknown {
		t.Fatal("disconnection retained path")
	}
	m.mu.Unlock()
	m.samplePath()
	if source.calls.Load() != 2 {
		t.Fatal("offline runtime kept sampling transport")
	}
}

func TestPathExpiryEmitsUnknownWithoutAnotherSample(t *testing.T) {
	t.Parallel()
	m, _ := pathManager(t)
	changed := make(chan Hint, 4)
	m.record.ID = "AQEBAQEBAQEBAQEBAQEBAQ"
	m.onChanged = func(h Hint) { changed <- h }
	m.sessionActive = true
	m.samplePath()
	initial := <-changed
	select {
	case latest := <-changed:
		m.mu.Lock()
		path := m.pathLocked(time.Now())
		m.mu.Unlock()
		if latest.Revision <= initial.Revision || path != transport.PathUnknown {
			t.Fatal("expired sample did not publish unknown")
		}
	case <-time.After(PathMaxAge + time.Second):
		t.Fatal("stale indicator never invalidated")
	}
}
