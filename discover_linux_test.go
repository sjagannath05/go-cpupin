//go:build linux

package cpupin

import (
	"os"
	"path/filepath"
	"testing"
)

// withRoots redirects the discovery file roots to fixtures for one test.
func withRoots(t *testing.T, cgroup, proc, sys string) {
	t.Helper()
	oldCg, oldProc, oldSys := cgroupRoot, procRoot, sysRoot
	if cgroup != "" {
		cgroupRoot = cgroup
	}
	if proc != "" {
		procRoot = proc
	}
	if sys != "" {
		sysRoot = sys
	}
	t.Cleanup(func() { cgroupRoot, procRoot, sysRoot = oldCg, oldProc, oldSys })
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAvailableV2(t *testing.T) {
	cg, proc := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(proc, "self", "cgroup"), "0::/\n")
	writeFile(t, filepath.Join(cg, "cpuset.cpus.effective"), "0-3\n")
	withRoots(t, cg, proc, "")

	got, err := Available()
	if err != nil {
		t.Fatal(err)
	}
	// Result is fixture-cpuset ∩ boot mask: every returned core must be in
	// both. On any real runner the boot mask covers the low cores, but never
	// assume — assert subset relations, not exact values.
	if got.IsEmpty() {
		t.Fatal("Available() empty")
	}
	fixture, _ := ParseCPUSet("0-3")
	if !got.Difference(fixture).IsEmpty() {
		t.Errorf("Available() = %s, not a subset of fixture cpuset 0-3", got)
	}
	if !got.Difference(bootMask).IsEmpty() {
		t.Errorf("Available() = %s, not a subset of boot mask %s", got, bootMask)
	}
}

func TestAvailableV2WalksUpToParent(t *testing.T) {
	// Leaf dir exists but has no cpuset file (common for docker leaf scopes);
	// the parent's cpuset.cpus.effective must be found.
	cg, proc := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(proc, "self", "cgroup"), "0::/system.slice/app.scope\n")
	if err := os.MkdirAll(filepath.Join(cg, "system.slice", "app.scope"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(cg, "cpuset.cpus.effective"), "0-1\n")
	withRoots(t, cg, proc, "")

	got, err := Available()
	if err != nil {
		t.Fatal(err)
	}
	fixture, _ := ParseCPUSet("0-1")
	if !got.Difference(fixture).IsEmpty() {
		t.Errorf("Available() = %s, want subset of 0-1 via parent walk", got)
	}
}

func TestAvailableV1(t *testing.T) {
	cg, proc := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(proc, "self", "cgroup"), "12:cpuset:/docker/abc\n")
	writeFile(t, filepath.Join(cg, "cpuset", "docker", "abc", "cpuset.effective_cpus"), "0-2\n")
	withRoots(t, cg, proc, "")

	got, err := Available()
	if err != nil {
		t.Fatal(err)
	}
	fixture, _ := ParseCPUSet("0-2")
	if !got.Difference(fixture).IsEmpty() {
		t.Errorf("Available() = %s, want subset of 0-2 (v1)", got)
	}
}

func TestAvailableFallsBackToBootMask(t *testing.T) {
	// cgroupfs unreadable (hardened container): best-effort boot mask, no error.
	withRoots(t, t.TempDir(), t.TempDir(), "") // both empty dirs
	got, err := Available()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(bootMask) {
		t.Errorf("Available() = %s, want boot mask %s", got, bootMask)
	}
}

func TestQuotaCPUs(t *testing.T) {
	cg, proc := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(proc, "self", "cgroup"), "0::/\n")
	writeFile(t, filepath.Join(cg, "cpu.max"), "200000 100000\n")
	withRoots(t, cg, proc, "")

	cpus, ok, err := QuotaCPUs()
	if err != nil || !ok || cpus != 2.0 {
		t.Errorf("QuotaCPUs() = (%v,%v,%v), want (2,true,nil)", cpus, ok, err)
	}
}

func TestQuotaCPUsUnlimited(t *testing.T) {
	cg, proc := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(proc, "self", "cgroup"), "0::/\n")
	writeFile(t, filepath.Join(cg, "cpu.max"), "max 100000\n")
	withRoots(t, cg, proc, "")

	_, ok, err := QuotaCPUs()
	if err != nil || ok {
		t.Errorf("QuotaCPUs() ok=%v err=%v, want unlimited (false, nil)", ok, err)
	}
}

func TestQuotaCPUsV1(t *testing.T) {
	cg, proc := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(proc, "self", "cgroup"), "11:cpu,cpuacct:/docker/abc\n")
	writeFile(t, filepath.Join(cg, "cpu", "docker", "abc", "cpu.cfs_quota_us"), "150000\n")
	writeFile(t, filepath.Join(cg, "cpu", "docker", "abc", "cpu.cfs_period_us"), "100000\n")
	withRoots(t, cg, proc, "")

	cpus, ok, err := QuotaCPUs()
	if err != nil || !ok || cpus != 1.5 {
		t.Errorf("QuotaCPUs() = (%v,%v,%v), want (1.5,true,nil)", cpus, ok, err)
	}
}

func TestReadSiblings(t *testing.T) {
	sys := t.TempDir()
	// cpu0/cpu2 are HT siblings; cpu1 has no topology file (best-effort skip).
	writeFile(t, filepath.Join(sys, "devices", "system", "cpu", "cpu0", "topology", "thread_siblings_list"), "0,2\n")
	writeFile(t, filepath.Join(sys, "devices", "system", "cpu", "cpu2", "topology", "thread_siblings_list"), "0,2\n")
	withRoots(t, "", "", sys)

	got := readSiblings(NewCPUSet(0, 1, 2))
	if len(got[0]) != 1 || got[0][0] != 2 {
		t.Errorf("siblings[0] = %v, want [2]", got[0])
	}
	if len(got[2]) != 1 || got[2][0] != 0 {
		t.Errorf("siblings[2] = %v, want [0]", got[2])
	}
	if _, exists := got[1]; exists {
		t.Errorf("siblings[1] should be absent (no topology file)")
	}
}

func TestBootMaskCaptured(t *testing.T) {
	if bootMaskErr != nil {
		t.Fatalf("boot mask capture failed: %v", bootMaskErr)
	}
	if bootMask.IsEmpty() {
		t.Fatal("boot mask empty")
	}
}
