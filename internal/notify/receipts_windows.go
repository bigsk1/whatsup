//go:build windows

package notify

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const noticeReceiptRegistryValue = "WhatsUpIssuedNoticesV1"
const maxProtectedNoticeReceipts = 128 << 10

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
		return nil, ErrUnavailable
	}
	defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(output.Data))))
	if output.Size == 0 || output.Size > maxProtectedNoticeReceipts {
		return nil, ErrUnavailable
	}
	view := unsafe.Slice(output.Data, int(output.Size))
	defer clear(view)
	return append([]byte(nil), view...), nil
}

func openWindowsReceipts(appName string) (*noticeReceipts, error) {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\AppUserModelId\`+appName, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer key.Close()
	size, kind, err := key.GetValue(noticeReceiptRegistryValue, nil)
	var plain []byte
	if err == nil {
		if kind != registry.BINARY || size <= 0 || size > maxProtectedNoticeReceipts {
			return nil, ErrUnavailable
		}
		ciphertext := make([]byte, size)
		n, kind, err := key.GetValue(noticeReceiptRegistryValue, ciphertext)
		if err != nil || kind != registry.BINARY || n != size {
			return nil, ErrUnavailable
		}
		plain, err = receiptDPAPI(ciphertext, appName, false)
		if err != nil {
			return nil, ErrUnavailable
		}
	} else if err != registry.ErrNotExist {
		return nil, ErrUnavailable
	}
	defer clear(plain)
	return newNoticeReceipts(plain, func(data []byte) error {
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
	}, nil)
}
