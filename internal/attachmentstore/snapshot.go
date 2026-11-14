package attachmentstore

import (
	"context"
	"crypto/sha256"
	"io"
)

// VerifyObjects authenticates every persisted unit without accumulating files
// or exposing incomplete content. The caller quiesces runtime writers first.
func (s *Scope) VerifyObjects(ctx context.Context) error {
	if s == nil || s.scopeState == nil || ctx == nil {
		return ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return err
	}
	var cursor uint64
	for {
		entries, next, more, err := s.meta.Page(cursor, 32)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e.ObjectID == "" || e.CipherBytes == 0 {
				continue
			}
			file, err := s.openObject(e, false)
			if err != nil {
				return err
			}
			crypto, err := primitive(e.Key)
			if err != nil {
				file.Close()
				return err
			}
			hash := sha256.New()
			for index, unit := range e.Units {
				if ctx.Err() != nil {
					file.Close()
					return ctx.Err()
				}
				reader, err := crypto.NewDecryptingReader(io.NewSectionReader(file, int64(unit.Offset), int64(unit.CipherBytes)), s.aad(e, uint32(index), unit.PlainBytes))
				if err != nil {
					file.Close()
					return ErrIntegrity
				}
				n, err := io.Copy(hash, io.LimitReader(reader, int64(unit.PlainBytes)+1))
				if err != nil || n != int64(unit.PlainBytes) {
					file.Close()
					return ErrIntegrity
				}
			}
			file.Close()
			if e.Ready {
				var actual [32]byte
				copy(actual[:], hash.Sum(nil))
				if actual != e.Record.Offer.SHA256 {
					return ErrIntegrity
				}
			}
		}
		if !more {
			return nil
		}
		cursor = next
	}
}
