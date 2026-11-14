//go:build linux

package transport_test

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func resourceProcessStats() (uint64, time.Duration, error) {
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		return 0, 0, err
	}
	wire, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(string(wire))
	if len(fields) < 2 {
		return 0, 0, fmt.Errorf("resident memory unavailable")
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	cpu := (time.Duration(usage.Utime.Sec)+time.Duration(usage.Stime.Sec))*time.Second + (time.Duration(usage.Utime.Usec)+time.Duration(usage.Stime.Usec))*time.Microsecond
	return pages * uint64(os.Getpagesize()), cpu, nil
}
