package cpupin

import (
	"fmt"
	"strconv"
	"strings"
)

// cgroupPaths parses /proc/self/cgroup content.
// v2 line shape:  "0::/some/path"
// v1 line shape:  "N:controller[,controller]:/path"
// v2 is "" when the process is not on the unified hierarchy; v1 maps each
// controller name to its path (empty map when pure v2).
func cgroupPaths(selfCgroup string) (v2 string, v1 map[string]string) {
	v1 = map[string]string{}
	for _, line := range strings.Split(selfCgroup, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[0] == "0" && parts[1] == "" {
			v2 = parts[2]
			continue
		}
		for _, ctrl := range strings.Split(parts[1], ",") {
			if ctrl != "" {
				v1[ctrl] = parts[2]
			}
		}
	}
	return v2, v1
}

// parseCPUMax parses cgroup v2 cpu.max content: "max 100000" (unlimited,
// ok=false) or "<quota_us> <period_us>" → quota/period CPUs.
func parseCPUMax(s string) (cpus float64, ok bool, err error) {
	fields := strings.Fields(s)
	if len(fields) != 2 {
		return 0, false, fmt.Errorf("cpupin: cpu.max %q: want 2 fields", strings.TrimSpace(s))
	}
	if fields[0] == "max" {
		return 0, false, nil
	}
	quota, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false, fmt.Errorf("cpupin: cpu.max %q: bad quota: %w", strings.TrimSpace(s), err)
	}
	period, err := strconv.ParseFloat(fields[1], 64)
	if err != nil || period <= 0 {
		return 0, false, fmt.Errorf("cpupin: cpu.max %q: bad period", strings.TrimSpace(s))
	}
	return quota / period, true, nil
}

// parseCFSQuota parses cgroup v1 cpu.cfs_quota_us / cpu.cfs_period_us file
// contents. quota -1 means unlimited (ok=false).
func parseCFSQuota(quotaStr, periodStr string) (cpus float64, ok bool, err error) {
	quota, err := strconv.ParseFloat(strings.TrimSpace(quotaStr), 64)
	if err != nil {
		return 0, false, fmt.Errorf("cpupin: cfs_quota_us %q: %w", strings.TrimSpace(quotaStr), err)
	}
	if quota < 0 {
		return 0, false, nil
	}
	period, err := strconv.ParseFloat(strings.TrimSpace(periodStr), 64)
	if err != nil || period <= 0 {
		return 0, false, fmt.Errorf("cpupin: cfs_period_us %q: bad period", strings.TrimSpace(periodStr))
	}
	return quota / period, true, nil
}
