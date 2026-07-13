//go:build !linux

package cpupin

// Everything that would need Linux returns ErrUnsupported — nothing silently
// pretends to pin (DESIGN §5).

func Available() (CPUSet, error) { return CPUSet{}, ErrUnsupported }

func QuotaCPUs() (float64, bool, error) { return 0, false, ErrUnsupported }

func readSiblings(CPUSet) map[int][]int { return nil }

func gomaxprocsTarget() (int, error) { return 0, ErrUnsupported }
