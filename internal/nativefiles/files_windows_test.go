//go:build windows

package nativefiles

import (
	"bytes"
	"context"
	"image/png"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestClipboardCopiesAndReleasesNativeMemoryBeforeDIBConversion(t *testing.T) {
	body := testDIB(1, 1, 32)
	copy(body[40:], []byte{1, 2, 3, 0})
	handle, _, _ := kernel32.NewProc("GlobalAlloc").Call(2, uintptr(len(body)))
	if handle == 0 {
		t.Fatal("synthetic clipboard allocation failed")
	}
	pointer, _, _ := globalLock.Call(handle)
	if pointer == 0 {
		kernel32.NewProc("GlobalFree").Call(handle)
		t.Fatal("synthetic clipboard lock failed")
	}
	moveMemory.Call(pointer, uintptr(unsafe.Pointer(&body[0])), uintptr(len(body)))
	runtime.KeepAlive(body)
	globalUnlock.Call(handle)
	opened, closes := false, 0
	var thread uint32
	api := clipboardAPI{
		open: func() bool { opened = true; thread = windows.GetCurrentThreadId(); return true },
		close: func() {
			if !opened || windows.GetCurrentThreadId() != thread {
				t.Fatal("clipboard closed on wrong thread")
			}
			opened = false
			closes++
			if result, _, _ := kernel32.NewProc("GlobalFree").Call(handle); result != 0 {
				t.Fatal("synthetic clipboard free failed")
			}
		},
		png: func() uintptr { return 0 },
		copy: func(format uintptr) ([]byte, error) {
			if !opened || windows.GetCurrentThreadId() != thread || format != 17 {
				t.Fatal("clipboard read outside native lease")
			}
			return copyClipboardHandle(handle)
		},
	}
	var copied []byte
	encoded, err := clipboardImageUsing(context.Background(), api, func(data []byte) ([]byte, error) {
		if opened || closes != 1 || !bytes.Equal(data, body) {
			t.Fatal("conversion retained clipboard or borrowed provider memory")
		}
		if size, _, _ := globalSize.Call(handle); size != 0 {
			t.Fatal("provider memory still live during conversion")
		}
		copied = data
		return clipboardDIBPNG(data)
	})
	if err != nil || closes != 1 {
		t.Fatalf("clipboard conversion failed: %v", err)
	}
	if _, err := png.Decode(bytes.NewReader(encoded)); err != nil {
		t.Fatal("owned image was invalidated after native release")
	}
	if !bytes.Equal(copied, make([]byte, len(copied))) {
		t.Fatal("raw DIB copy was retained after conversion")
	}
}

func TestClipboardCancellationAndFailureReleaseBeforeReturning(t *testing.T) {
	for _, cancelDuringCopy := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		closes, decoded := 0, false
		data := []byte{1, 2, 3}
		api := clipboardAPI{open: func() bool { return true }, close: func() { closes++ }, png: func() uintptr { return 0 }, copy: func(uintptr) ([]byte, error) {
			if cancelDuringCopy {
				cancel()
				return data, nil
			}
			return nil, ErrUnavailable
		}}
		result, err := clipboardImageUsing(ctx, api, func([]byte) ([]byte, error) { decoded = true; return []byte("must not be returned"), nil })
		cancel()
		if err == nil || len(result) != 0 || closes != 1 || decoded {
			t.Fatal("cancelled/failed copy retained native clipboard or decoded data")
		}
		if cancelDuringCopy && !bytes.Equal(data, []byte{0, 0, 0}) {
			t.Fatal("cancelled clipboard copy was retained")
		}
	}
}
