//go:build windows

package nativefiles

import (
	"context"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                   = windows.NewLazySystemDLL("user32.dll")
	openClipboard            = user32.NewProc("OpenClipboard")
	closeClipboard           = user32.NewProc("CloseClipboard")
	getClipboardData         = user32.NewProc("GetClipboardData")
	registerClipboardFormat  = user32.NewProc("RegisterClipboardFormatW")
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	globalSize               = kernel32.NewProc("GlobalSize")
	globalLock               = kernel32.NewProc("GlobalLock")
	globalUnlock             = kernel32.NewProc("GlobalUnlock")
	moveMemory               = windows.NewLazySystemDLL("ntdll.dll").NewProc("RtlMoveMemory")
	shell32                  = windows.NewLazySystemDLL("shell32.dll")
	parseDisplayName         = shell32.NewProc("SHParseDisplayName")
	openFolderAndSelectItems = shell32.NewProc("SHOpenFolderAndSelectItems")
	ole32                    = windows.NewLazySystemDLL("ole32.dll")
	coInitializeEx           = ole32.NewProc("CoInitializeEx")
	coUninitialize           = ole32.NewProc("CoUninitialize")
	coTaskMemFree            = ole32.NewProc("CoTaskMemFree")
)

func clipboardImage(ctx context.Context) ([]byte, error) {
	return clipboardImageUsing(ctx, clipboardAPI{
		open:  func() bool { ok, _, _ := openClipboard.Call(0); return ok != 0 },
		close: func() { closeClipboard.Call() },
		png: func() uintptr {
			name, _ := windows.UTF16PtrFromString("PNG")
			format, _, _ := registerClipboardFormat.Call(uintptr(unsafe.Pointer(name)))
			return format
		},
		copy: func(format uintptr) ([]byte, error) {
			handle, _, _ := getClipboardData.Call(format)
			if handle == 0 {
				return nil, nil
			}
			return copyClipboardHandle(handle)
		},
	}, clipboardDIBPNG)
}

// The native clipboard is held only while copying owned bytes. Image decoding
// and encoding happen after CloseClipboard and after releasing the OS thread.
type clipboardAPI struct {
	open  func() bool
	close func()
	png   func() uintptr
	copy  func(uintptr) ([]byte, error)
}

func clipboardImageUsing(ctx context.Context, api clipboardAPI, decode func([]byte) ([]byte, error)) ([]byte, error) {
	data, dib, err := copyClipboard(ctx, api)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		clear(data)
		return nil, ErrUnavailable
	}
	if !dib {
		return data, nil
	}
	defer clear(data)
	return decode(data)
}

func copyClipboard(ctx context.Context, api clipboardAPI) ([]byte, bool, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, false, ErrUnavailable
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if !api.open() {
		return nil, false, ErrUnavailable
	}
	defer api.close()
	pngFormat := api.png()
	formats := []uintptr{pngFormat, 17, 8} // PNG, CF_DIBV5, CF_DIB.
	for _, format := range formats {
		if format == 0 || ctx.Err() != nil {
			continue
		}
		data, err := api.copy(format)
		if err != nil {
			return nil, false, err
		}
		if len(data) == 0 {
			continue
		}
		if len(data) > MaxClipboardBytes {
			clear(data)
			return nil, false, ErrUnavailable
		}
		return data, format != pngFormat, nil
	}
	return nil, false, ErrUnavailable
}

func copyClipboardHandle(handle uintptr) ([]byte, error) {
	size, _, _ := globalSize.Call(handle)
	if size == 0 || size > MaxClipboardBytes {
		return nil, ErrUnavailable
	}
	pointer, _, _ := globalLock.Call(handle)
	if pointer == 0 {
		return nil, ErrUnavailable
	}
	defer globalUnlock.Call(handle)
	data := make([]byte, int(size))
	moveMemory.Call(uintptr(unsafe.Pointer(&data[0])), pointer, size)
	runtime.KeepAlive(data)
	return data, nil
}

func showInFolder(ctx context.Context, filename string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// A dedicated COM apartment avoids changing a Wails thread's apartment.
	hresult, _, _ := coInitializeEx.Call(0, 2)
	if int32(hresult) < 0 {
		return ErrUnavailable
	}
	defer coUninitialize.Call()
	wide, err := windows.UTF16PtrFromString(filename)
	if err != nil || ctx.Err() != nil {
		return ErrUnavailable
	}
	var idList uintptr
	hresult, _, _ = parseDisplayName.Call(uintptr(unsafe.Pointer(wide)), 0, uintptr(unsafe.Pointer(&idList)), 0, 0)
	if int32(hresult) < 0 || idList == 0 {
		return ErrUnavailable
	}
	defer coTaskMemFree.Call(idList)
	if ctx.Err() != nil {
		return ErrUnavailable
	}
	// A full absolute PIDL with zero children selects that item in its parent.
	hresult, _, _ = openFolderAndSelectItems.Call(idList, 0, 0, 0)
	if int32(hresult) < 0 {
		return ErrUnavailable
	}
	return nil
}
