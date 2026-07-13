//go:build !linux

package cpupin

// PinSelf is Linux-only.
func PinSelf(cores ...int) (Unpin, error) { return nil, ErrUnsupported }

// SetProcessMask is Linux-only.
func SetProcessMask(cores ...int) error { return ErrUnsupported }
