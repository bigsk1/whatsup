package transfer

// Unchanged helper from 3e483ae internal/transfer/engine.go, needed by the
// historical Record validator. Only the historical parser is imported by tests.
func checkpoint(offer Offer, next uint32) bool {
	return next <= offer.Chunks() && (next%UnitChunks == 0 || next == offer.Chunks())
}
