//go:build linux

package cpupin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// File roots, package vars so tests can point them at fixture trees.
var (
	cgroupRoot = "/sys/fs/cgroup"
	procRoot   = "/proc"
	sysRoot    = "/sys"
)

// Boot-time affinity mask, captured at package init — before this library (or
// anything else in-process going through it) can narrow thread masks. Available()
// intersects the LIVE cgroup cpuset with this, never with current thread
// affinity, so Plan.Apply()/SetProcessMask can't feed back into discovery.
var (
	bootMask    CPUSet
	bootMaskErr error
)

func init() {
	var set unix.CPUSet
	if err := unix.SchedGetaffinity(0, &set); err != nil {
		bootMaskErr = fmt.Errorf("cpupin: sched_getaffinity at init: %w", err)
		return
	}
	bootMask = cpuSetFromUnix(&set)
}

func cpuSetFromUnix(set *unix.CPUSet) CPUSet {
	var cores []int
	for c := 0; c < len(set)*64; c++ {
		if set.IsSet(c) {
			cores = append(cores, c)
		}
	}
	return NewCPUSet(cores...)
}

func unixFromCPUSet(s CPUSet) unix.CPUSet {
	var set unix.CPUSet
	for _, c := range s.List() {
		set.Set(c)
	}
	return set
}

// Available returns the cores this process may use: live cgroup effective
// cpuset ∩ boot-time affinity mask. If cgroupfs is masked or unmounted, it
// falls back to the boot mask alone — best-effort, never an error.
func Available() (CPUSet, error) {
	if bootMaskErr != nil {
		return CPUSet{}, bootMaskErr
	}
	if cg, ok := effectiveCgroupCpuset(); ok {
		return cg.Intersect(bootMask), nil
	}
	return bootMask, nil
}

// effectiveCgroupCpuset reads the cgroup cpuset for this process, preferring
// v2 cpuset.cpus.effective, then v1 cpuset.effective_cpus, then v1 cpuset.cpus.
// Files may be absent at the leaf (docker scopes) — walk up to the mount root.
func effectiveCgroupCpuset() (CPUSet, bool) {
	data, err := os.ReadFile(filepath.Join(procRoot, "self", "cgroup"))
	if err != nil {
		return CPUSet{}, false
	}
	v2, v1 := cgroupPaths(string(data))
	if v2 != "" {
		if s, ok := readCpusetUpward(filepath.Join(cgroupRoot, v2), cgroupRoot, "cpuset.cpus.effective"); ok {
			return s, true
		}
	}
	if p, ok := v1["cpuset"]; ok {
		base := filepath.Join(cgroupRoot, "cpuset")
		if s, ok := readCpusetUpward(filepath.Join(base, p), base, "cpuset.effective_cpus"); ok {
			return s, true
		}
		if s, ok := readCpusetUpward(filepath.Join(base, p), base, "cpuset.cpus"); ok {
			return s, true
		}
	}
	return CPUSet{}, false
}

// readCpusetUpward reads file from dir, walking up parent directories until
// stop (inclusive). Returns the first parseable, non-empty cpuset.
func readCpusetUpward(dir, stop, file string) (CPUSet, bool) {
	for {
		if data, err := os.ReadFile(filepath.Join(dir, file)); err == nil {
			if s, perr := ParseCPUSet(string(data)); perr == nil && !s.IsEmpty() {
				return s, true
			}
		}
		if dir == stop || !strings.HasPrefix(dir, stop) {
			return CPUSet{}, false
		}
		dir = filepath.Dir(dir)
	}
}

// QuotaCPUs returns the cgroup CPU quota in fractional CPUs.
// ok=false when unlimited or when no cgroup information is readable.
func QuotaCPUs() (float64, bool, error) {
	data, err := os.ReadFile(filepath.Join(procRoot, "self", "cgroup"))
	if err != nil {
		return 0, false, nil
	}
	v2, v1 := cgroupPaths(string(data))
	if v2 != "" {
		dir := filepath.Join(cgroupRoot, v2)
		for {
			if b, err := os.ReadFile(filepath.Join(dir, "cpu.max")); err == nil {
				return parseCPUMax(string(b))
			}
			if dir == cgroupRoot || !strings.HasPrefix(dir, cgroupRoot) {
				break
			}
			dir = filepath.Dir(dir)
		}
	}
	if p, ok := v1["cpu"]; ok {
		base := filepath.Join(cgroupRoot, "cpu")
		dir := filepath.Join(base, p)
		for {
			q, errQ := os.ReadFile(filepath.Join(dir, "cpu.cfs_quota_us"))
			per, errP := os.ReadFile(filepath.Join(dir, "cpu.cfs_period_us"))
			if errQ == nil && errP == nil {
				return parseCFSQuota(string(q), string(per))
			}
			if dir == base || !strings.HasPrefix(dir, base) {
				break
			}
			dir = filepath.Dir(dir)
		}
	}
	return 0, false, nil
}

// readSiblings returns core → SMT siblings (excluding the core itself) for the
// given set, from sysfs topology. Best-effort: unreadable entries are skipped.
func readSiblings(avail CPUSet) map[int][]int {
	out := map[int][]int{}
	for _, c := range avail.List() {
		p := filepath.Join(sysRoot, "devices", "system", "cpu",
			fmt.Sprintf("cpu%d", c), "topology", "thread_siblings_list")
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		sibs, perr := ParseCPUSet(string(data))
		if perr != nil {
			continue
		}
		out[c] = sibs.Difference(NewCPUSet(c)).List()
	}
	return out
}

// gomaxprocsTarget computes floor(min(quota, Available().Size())), min 1.
func gomaxprocsTarget() (int, error) {
	avail, err := Available()
	if err != nil {
		return 0, err
	}
	n := avail.Size()
	if q, ok, qerr := QuotaCPUs(); qerr == nil && ok && int(q) < n {
		n = int(q)
	}
	if n < 1 {
		n = 1
	}
	return n, nil
}
