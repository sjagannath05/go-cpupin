//go:build !linux

package cpupin

// SteerReuseport is Linux-only.
func SteerReuseport(fd uintptr, readerCores CPUSet) error { return ErrUnsupported }

// SetIncomingCPU is Linux-only.
func SetIncomingCPU(fd uintptr, cpu int) error { return ErrUnsupported }
