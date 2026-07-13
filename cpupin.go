// Package cpupin provides CPU pinning, cgroup-aware core discovery, role-based
// core planning, and SO_REUSEPORT packet-path steering for Linux.
//
// All real functionality is Linux-only; on other platforms every entry point
// returns ErrUnsupported (exception: SetGOMAXPROCS, a documented no-op). The
// pure components (CPUSet, the planner) work everywhere.
package cpupin

import (
	"errors"
	"runtime"
)

// ErrUnsupported is returned by every Linux-only entry point on other platforms.
var ErrUnsupported = errors.New("cpupin: not supported on this platform")

// Unpin restores the previous thread affinity mask and unlocks the goroutine
// from its OS thread. Restore failures are deliberately swallowed — best-effort
// restore, unconditional unlock (DESIGN §4.2).
type Unpin func()

// Supported reports whether real pinning/steering is available on this platform.
func Supported() bool { return runtime.GOOS == "linux" }
