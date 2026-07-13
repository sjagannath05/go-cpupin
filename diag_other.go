//go:build !linux

package cpupin

// CheckAlignment is Linux-only.
func CheckAlignment(iface string, readerCores CPUSet) (*AlignmentReport, error) {
	return nil, ErrUnsupported
}
