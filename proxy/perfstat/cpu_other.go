// proxy/perfstat/cpu_other.go
//go:build !linux && !darwin

package perfstat

import (
	"errors"
	"time"
)

var ErrProcessCPUNotSupported = errors.New("process CPU measurement via RUSAGE is only supported on Linux and macOS in this build")

func currentProcessCPU() (time.Duration, error) {
	return 0, ErrProcessCPUNotSupported
}
