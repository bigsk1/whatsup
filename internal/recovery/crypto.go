package recovery

import (
	"context"

	"github.com/tink-crypto/tink-go/v2/insecuresecretdataaccess"
	"github.com/tink-crypto/tink-go/v2/keyset"
	"github.com/tink-crypto/tink-go/v2/secretdata"
	"github.com/tink-crypto/tink-go/v2/streamingaead"
	"github.com/tink-crypto/tink-go/v2/streamingaead/aesgcmhkdf"
	"github.com/tink-crypto/tink-go/v2/tink"
	"whatsup/internal/keymaterial"
)

// Argon2 cannot be interrupted internally. Do not abandon derivation goroutines
// on cancellation or permit concurrent recovery calls to allocate unbounded KDF
// memory. The slot covers derivation only, not the subsequent archive stream.
var derivation = make(chan struct{}, 1)

func derive(ctx context.Context, passphrase, salt []byte) ([]byte, error) {
	select {
	case derivation <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-derivation }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := keymaterial.PassphraseKey(passphrase, salt)
	if err != nil {
		return nil, ErrSecret
	}
	defer clear(root)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := keymaterial.Derive(root, "recovery-stream/v1")
	if err != nil {
		return nil, ErrIO
	}
	return key, nil
}

func primitive(key []byte) (tink.StreamingAEAD, error) {
	params, err := aesgcmhkdf.NewParameters(aesgcmhkdf.ParametersOpts{KeySizeInBytes: 32, DerivedKeySizeInBytes: 32, HKDFHashType: aesgcmhkdf.SHA256, SegmentSizeInBytes: 1 << 20})
	if err != nil {
		return nil, ErrIO
	}
	material, err := aesgcmhkdf.NewKey(params, secretdata.NewBytesFromData(key, insecuresecretdataaccess.Token{}))
	if err != nil {
		return nil, ErrIO
	}
	manager := keyset.NewManager()
	id, err := manager.AddKey(material)
	if err != nil {
		return nil, ErrIO
	}
	if manager.SetPrimary(id) != nil {
		return nil, ErrIO
	}
	handle, err := manager.Handle()
	if err != nil {
		return nil, ErrIO
	}
	aead, err := streamingaead.New(handle)
	if err != nil {
		return nil, ErrIO
	}
	return aead, nil
}
