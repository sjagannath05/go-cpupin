//go:build linux

package cpupin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"golang.org/x/sys/unix"
)

// PinSelf locks the calling goroutine to its OS thread and binds that thread
// to the given cores. Must be called from inside the goroutine being pinned.
// The returned Unpin restores the previous mask and unlocks the thread.
//
// Kernel semantics absorbed here: a partially-overlapping mask
// is silently narrowed by the kernel with no error, so PinSelf re-reads the
// affinity after setting and fails if the effective mask differs from the
// request. Never trust the setter's return code alone.
func PinSelf(cores ...int) (Unpin, error) {
	req := NewCPUSet(cores...)
	if req.IsEmpty() {
		return nil, errors.New("cpupin: PinSelf: no cores given")
	}
	avail, err := Available()
	if err != nil {
		return nil, err
	}
	if bad := req.Difference(avail); !bad.IsEmpty() {
		return nil, fmt.Errorf("cpupin: PinSelf: cores %s outside available set %s", bad, avail)
	}

	runtime.LockOSThread()
	var prev unix.CPUSet
	if err := unix.SchedGetaffinity(0, &prev); err != nil {
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("cpupin: PinSelf: sched_getaffinity: %w", err)
	}
	want := unixFromCPUSet(req)
	if err := unix.SchedSetaffinity(0, &want); err != nil {
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("cpupin: PinSelf: sched_setaffinity(%s): %w", req, err)
	}
	var got unix.CPUSet
	if err := unix.SchedGetaffinity(0, &got); err != nil {
		_ = unix.SchedSetaffinity(0, &prev)
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("cpupin: PinSelf: readback: %w", err)
	}
	if eff := cpuSetFromUnix(&got); !eff.Equal(req) {
		_ = unix.SchedSetaffinity(0, &prev)
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("cpupin: PinSelf: kernel narrowed mask to %s (requested %s) — cgroup cpuset clamp", eff, req)
	}
	return func() {
		_ = unix.SchedSetaffinity(0, &prev) // best-effort restore, deliberate
		runtime.UnlockOSThread()
	}, nil
}

// SetProcessMask applies the mask to EVERY existing thread of the process by
// walking /proc/self/task, then relies on creator-inheritance for all future
// threads. Repeats the walk until a pass finds no unseen tids,
// shrinking the concurrent-thread-creation race window. ESRCH (thread exited
// mid-walk) is ignored.
//
// Ordering rule: call before any PinSelf — the sweep cannot
// distinguish datapath threads and would clobber earlier pins.
func SetProcessMask(cores ...int) error {
	req := NewCPUSet(cores...)
	if req.IsEmpty() {
		return errors.New("cpupin: SetProcessMask: no cores given")
	}
	avail, err := Available()
	if err != nil {
		return err
	}
	if bad := req.Difference(avail); !bad.IsEmpty() {
		return fmt.Errorf("cpupin: SetProcessMask: cores %s outside available set %s", bad, avail)
	}
	mask := unixFromCPUSet(req)
	done := map[int]bool{}
	for pass := 0; pass < 10; pass++ {
		tids, err := listTIDs()
		if err != nil {
			return err
		}
		progress := false
		for _, tid := range tids {
			if done[tid] {
				continue
			}
			progress = true
			if err := unix.SchedSetaffinity(tid, &mask); err != nil && err != unix.ESRCH {
				return fmt.Errorf("cpupin: SetProcessMask: tid %d: %w", tid, err)
			}
			done[tid] = true
		}
		if !progress {
			return nil
		}
	}
	return nil
}

func listTIDs() ([]int, error) {
	ents, err := os.ReadDir(filepath.Join(procRoot, "self", "task"))
	if err != nil {
		return nil, fmt.Errorf("cpupin: reading /proc/self/task: %w", err)
	}
	tids := make([]int, 0, len(ents))
	for _, e := range ents {
		if tid, err := strconv.Atoi(e.Name()); err == nil {
			tids = append(tids, tid)
		}
	}
	return tids, nil
}
