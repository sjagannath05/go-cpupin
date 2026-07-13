package cpupin

import "runtime"

// SetGOMAXPROCS aligns GOMAXPROCS to floor(min(quota, Available().Size())),
// minimum 1, and returns the value set.
//
// Portable — the one deliberate exception to the ErrUnsupported rule (DESIGN
// §4.1/§5): off-Linux it is a documented no-op returning
// (runtime.GOMAXPROCS(0), nil), because setting GOMAXPROCS is meaningful
// everywhere. On Go ≥ 1.25 the runtime already handles the quota half; this
// remains the cpuset-width shim and is harmless there.
func SetGOMAXPROCS() (int, error) {
	if !Supported() {
		return runtime.GOMAXPROCS(0), nil
	}
	n, err := gomaxprocsTarget()
	if err != nil {
		return runtime.GOMAXPROCS(0), err
	}
	runtime.GOMAXPROCS(n)
	return n, nil
}
