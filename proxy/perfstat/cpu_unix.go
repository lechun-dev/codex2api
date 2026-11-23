//go:build linux || darwin

package perfstat

import (
	"syscall"
	"time"
)

// 2026-11-23 coder(lq): Both production Linux and local macOS expose getrusage; share measurement so acceptance checks run on either platform.
func currentProcessCPU() (time.Duration, error) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, err
	}
	utime := time.Duration(usage.Utime.Sec)*time.Second + time.Duration(usage.Utime.Usec)*time.Microsecond
	stime := time.Duration(usage.Stime.Sec)*time.Second + time.Duration(usage.Stime.Usec)*time.Microsecond
	return utime + stime, nil
}
