//go:build windows

package transport_test

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func resourceProcessStats() (uint64, time.Duration, error) {
	var created, exited, kernel, user windows.Filetime
	process := windows.CurrentProcess()
	if err := windows.GetProcessTimes(process, &created, &exited, &kernel, &user); err != nil {
		return 0, 0, err
	}
	var counters struct {
		Size                       uint32
		PageFaultCount             uint32
		PeakWorkingSetSize         uintptr
		WorkingSetSize             uintptr
		QuotaPeakPagedPoolUsage    uintptr
		QuotaPagedPoolUsage        uintptr
		QuotaPeakNonPagedPoolUsage uintptr
		QuotaNonPagedPoolUsage     uintptr
		PagefileUsage              uintptr
		PeakPagefileUsage          uintptr
		PrivateUsage               uintptr
	}
	counters.Size = uint32(unsafe.Sizeof(counters))
	getMemory := windows.NewLazySystemDLL("psapi.dll").NewProc("GetProcessMemoryInfo")
	result, _, err := getMemory.Call(uintptr(process), uintptr(unsafe.Pointer(&counters)), uintptr(counters.Size))
	if result == 0 {
		return 0, 0, err
	}
	kernelTicks := uint64(kernel.HighDateTime)<<32 | uint64(kernel.LowDateTime)
	userTicks := uint64(user.HighDateTime)<<32 | uint64(user.LowDateTime)
	return uint64(counters.WorkingSetSize), time.Duration(kernelTicks+userTicks) * 100 * time.Nanosecond, nil
}
