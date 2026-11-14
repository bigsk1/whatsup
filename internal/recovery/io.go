package recovery

import (
	"context"
	"errors"
	"io"
)

var errCipherLimit = errors.New("recovery ciphertext limit")

type guardedReader struct {
	ctx   context.Context
	r     io.Reader
	empty int
}

func (r *guardedReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	if n < 0 || n > len(p) {
		return 0, ErrIO
	}
	if n == 0 && err == nil && len(p) > 0 {
		r.empty++
		if r.empty >= 100 {
			return 0, io.ErrNoProgress
		}
	} else if n > 0 {
		r.empty = 0
	}
	return n, err
}

type guardedWriter struct {
	ctx context.Context
	w   io.Writer
}

func (w guardedWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.w.Write(p)
	if n < 0 || n > len(p) {
		return 0, ErrIO
	}
	if n != len(p) && err == nil {
		return n, io.ErrShortWrite
	}
	return n, err
}

type ciphertextReader struct {
	r         io.Reader
	max, used uint64
}

func (r *ciphertextReader) Read(p []byte) (int, error) {
	if r.used > r.max {
		return 0, errCipherLimit
	}
	remaining := r.max + 1 - r.used
	if uint64(len(p)) > remaining {
		p = p[:int(remaining)]
	}
	n, err := r.r.Read(p)
	r.used += uint64(n)
	return n, err
}

func writeBytes(w io.Writer, p []byte) error {
	n, err := w.Write(p)
	if err == nil && n != len(p) {
		return io.ErrShortWrite
	}
	return err
}
func resultError(ctx context.Context, fallback error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fallback
}
func readError(ctx context.Context, err error) error {
	if errors.Is(err, errCipherLimit) {
		return ErrLimit
	}
	return resultError(ctx, ErrAuthentication)
}
