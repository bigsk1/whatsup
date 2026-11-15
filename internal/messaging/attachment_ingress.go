package messaging

import (
	"context"
	"sync"

	"whatsup/internal/transfer"
)

const (
	// Older control2 peers can replay128 requests before waiting for receipts.
	// Allow both request/receipt directions plus bounded offer/write notices.
	maxAttachmentIngressControls = 2*transfer.MaxOffers + 2*transfer.MaxActive + 8
	maxAttachmentIngressChunks   = transfer.UnitChunks
)

type attachmentIngressEvent struct {
	frame   transfer.Frame
	written bool
	chunk   bool
}

// attachmentIngress separates local attachment IO backpressure from the TLS
// scheduler. It retains at most one credit window (1MiB) of chunk bytes and a
// fixed number of small controls. Excess is a bounded peer-protocol failure,
// never an unbounded goroutine, byte queue, or a wait on the chat writer.
type attachmentIngress struct {
	ctx              context.Context
	cancel           context.CancelFunc
	events           chan attachmentIngressEvent
	failures         chan error
	done             chan struct{}
	mu               sync.Mutex
	controls, chunks int
}

func newAttachmentIngress(ctx context.Context, engine *transfer.Engine, epoch uint64) *attachmentIngress {
	work, cancel := context.WithCancel(ctx)
	q := &attachmentIngress{ctx: work, cancel: cancel, events: make(chan attachmentIngressEvent, maxAttachmentIngressControls+maxAttachmentIngressChunks), failures: make(chan error, 1), done: make(chan struct{})}
	go func() {
		defer close(q.done)
		for {
			select {
			case <-work.Done():
				return
			case event := <-q.events:
				var err error
				if event.written {
					err = engine.WrittenContext(work, epoch, event.frame)
				} else {
					err = engine.SubmitContext(work, epoch, event.frame)
				}
				q.mu.Lock()
				if event.chunk {
					q.chunks--
				} else {
					q.controls--
				}
				q.mu.Unlock()
				if err != nil {
					if work.Err() == nil {
						q.failures <- err
					}
					return
				}
			}
		}
	}()
	return q
}

func (q *attachmentIngress) submit(frame transfer.Frame) error {
	if frame.Validate() != nil {
		return errProtocol
	}
	return q.enqueue(attachmentIngressEvent{frame: frame, chunk: frame.Kind == transfer.ChunkFrame})
}

func (q *attachmentIngress) written(frame transfer.Frame) error {
	// Written notifications only need the immutable frame header. Retaining
	// their already-sent chunk buffers would double the byte budget needlessly.
	frame.Data = nil
	frame.Offer = transfer.Offer{}
	return q.enqueue(attachmentIngressEvent{frame: frame, written: true})
}

func (q *attachmentIngress) enqueue(event attachmentIngressEvent) error {
	if q == nil || q.ctx.Err() != nil {
		return errProtocol
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if event.chunk {
		if q.chunks >= maxAttachmentIngressChunks {
			return errProtocol
		}
		q.chunks++
	} else {
		if q.controls >= maxAttachmentIngressControls {
			return errProtocol
		}
		q.controls++
	}
	select {
	case q.events <- event:
		return nil
	default:
		if event.chunk {
			q.chunks--
		} else {
			q.controls--
		}
		return errProtocol
	}
}

func (q *attachmentIngress) close() {
	if q == nil {
		return
	}
	q.cancel()
	<-q.done
}
