//go:build windows

package notify

import (
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const noticeReceiptRegistryValue = "WhatsUpIssuedNoticesV1"
const maxProtectedNoticeReceipts = 128 << 10

var errReceiptCorrupt = errors.New("notification receipt data is invalid")

func classifyReceiptDPAPIError(err error) error {
	// Do not mistake resource/access/key-state faults for permission to erase
	// existing evidence. Ambiguous failures remain retryable and untouched.
	// All API parameters other than the stored blob are fixed/validated here.
	// Windows documents both codes for corrupted DPAPI input.
	if errors.Is(err, windows.ERROR_INVALID_DATA) || errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return errReceiptCorrupt
	}
	return ErrUnavailable
}

func receiptDPAPI(data []byte, appName string, protect bool) ([]byte, error) {
	if len(data) == 0 || len(data) > maxProtectedNoticeReceipts {
		return nil, ErrUnavailable
	}
	entropy := []byte("WhatsUp notification receipts v1\x00" + appName)
	input := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	binding := windows.DataBlob{Size: uint32(len(entropy)), Data: &entropy[0]}
	var output windows.DataBlob
	var err error
	if protect {
		err = windows.CryptProtectData(&input, nil, &binding, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	} else {
		err = windows.CryptUnprotectData(&input, nil, &binding, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	}
	runtime.KeepAlive(data)
	runtime.KeepAlive(entropy)
	if err != nil {
		if !protect {
			return nil, classifyReceiptDPAPIError(err)
		}
		return nil, ErrUnavailable
	}
	defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(output.Data))))
	if output.Size == 0 || output.Size > maxProtectedNoticeReceipts {
		if !protect {
			return nil, errReceiptCorrupt
		}
		return nil, ErrUnavailable
	}
	view := unsafe.Slice(output.Data, int(output.Size))
	defer clear(view)
	return append([]byte(nil), view...), nil
}

func openWindowsReceipts(appName string) (*noticeReceipts, error) {
	if !validToastAppName(appName) {
		return nil, ErrUnavailable
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\AppUserModelId\`+appName, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer key.Close()
	save := func(data []byte) error {
		defer clear(data)
		ciphertext, err := receiptDPAPI(data, appName, true)
		if err != nil {
			return ErrUnavailable
		}
		key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\AppUserModelId\`+appName, registry.SET_VALUE)
		if err != nil {
			return ErrUnavailable
		}
		defer key.Close()
		if key.SetBinaryValue(noticeReceiptRegistryValue, ciphertext) != nil {
			return ErrUnavailable
		}
		return nil
	}
	return loadWindowsReceipts(func(buf []byte) (int, uint32, error) { return key.GetValue(noticeReceiptRegistryValue, buf) }, func(ciphertext []byte) ([]byte, error) { return receiptDPAPI(ciphertext, appName, false) }, save)
}

func loadWindowsReceipts(read func([]byte) (int, uint32, error), decrypt func([]byte) ([]byte, error), save func([]byte) error) (*noticeReceipts, error) {
	size, kind, err := read(nil)
	var plain []byte
	defer func() { clear(plain) }()
	reset := false
	if err == nil {
		if kind != registry.BINARY || size <= 0 || size > maxProtectedNoticeReceipts {
			reset = true
		} else {
			ciphertext := make([]byte, size)
			n, kind, readErr := read(ciphertext)
			if readErr != nil || kind != registry.BINARY || n != size {
				return nil, ErrUnavailable
			}
			plain, err = decrypt(ciphertext)
			if err != nil && !errors.Is(err, errReceiptCorrupt) {
				return nil, ErrUnavailable
			}
			reset = err != nil
		}
	} else if err != registry.ErrNotExist {
		return nil, ErrUnavailable
	}
	if !reset {
		receipts, err := newNoticeReceipts(plain, save, nil)
		if err == nil {
			return receipts, nil
		}
	}
	// Receipts are disposable routing evidence, not identity or history. Retire
	// every old proof and recover notification delivery from unusable state.
	if save(encodeNoticeReceipts(nil)) != nil {
		return nil, ErrUnavailable
	}
	return newNoticeReceipts(nil, save, nil)
}
