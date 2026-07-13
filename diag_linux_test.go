//go:build linux

package cpupin

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckAlignmentReportsFindings(t *testing.T) {
	proc, sys := t.TempDir(), t.TempDir()
	// eth0: 2 RSS queues; IRQ 24 pinned to core 0, IRQ 25 pinned to core 9
	// (outside readers) → must produce a misalignment finding.
	writeFile(t, filepath.Join(sys, "class", "net", "eth0", "queues", "rx-0", ".keep"), "")
	writeFile(t, filepath.Join(sys, "class", "net", "eth0", "queues", "rx-1", ".keep"), "")
	writeFile(t, filepath.Join(proc, "interrupts"), sampleInterrupts)
	writeFile(t, filepath.Join(proc, "irq", "24", "smp_affinity_list"), "0\n")
	writeFile(t, filepath.Join(proc, "irq", "25", "smp_affinity_list"), "9\n")
	writeFile(t, filepath.Join(proc, "irq", "26", "smp_affinity_list"), "0\n")
	withRoots(t, "", proc, sys)

	readers := NewCPUSet(0, 1)
	rep, err := CheckAlignment("eth0", readers)
	if err != nil {
		t.Fatal(err)
	}
	if rep.RSSQueues != 2 {
		t.Errorf("RSSQueues = %d, want 2", rep.RSSQueues)
	}
	if got := rep.IRQAffinity[25]; !got.Equal(NewCPUSet(9)) {
		t.Errorf("IRQAffinity[25] = %s, want 9", got)
	}
	joined := strings.Join(rep.Misaligned, "\n")
	if !strings.Contains(joined, "25") {
		t.Errorf("findings must flag IRQ 25 outside reader cores:\n%s", joined)
	}
	// eth0 has no device/ symlink in the fixture → virtual-iface finding
	// (veth/bridge — SKF_AD_CPU locality not guaranteed, DESIGN §4.4).
	if !strings.Contains(joined, "virtual") {
		t.Errorf("findings must flag virtual interface:\n%s", joined)
	}
}

func TestCheckAlignmentFlagsUnreadableIRQAffinity(t *testing.T) {
	// /proc/interrupts is readable but /proc/irq/<n>/smp_affinity_list is
	// masked → partial report with an explicit finding, NOT silence or error.
	proc, sys := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(proc, "interrupts"), sampleInterrupts)
	withRoots(t, "", proc, sys)

	rep, err := CheckAlignment("eth0", NewCPUSet(0))
	if err != nil {
		t.Fatalf("masked /proc/irq must not error: %v", err)
	}
	if len(rep.IRQAffinity) != 0 {
		t.Errorf("IRQAffinity = %v, want empty", rep.IRQAffinity)
	}
	joined := strings.Join(rep.Misaligned, "\n")
	if !strings.Contains(joined, "unreadable") {
		t.Errorf("findings must flag unreadable IRQ affinities:\n%s", joined)
	}
}

func TestCheckAlignmentBestEffortWhenMasked(t *testing.T) {
	// Fully masked procfs/sysfs: partial report with findings, NOT an error.
	withRoots(t, "", t.TempDir(), t.TempDir())
	rep, err := CheckAlignment("eth0", NewCPUSet(0))
	if err != nil {
		t.Fatalf("masked fs must not error: %v", err)
	}
	if len(rep.Misaligned) == 0 {
		t.Error("masked fs should produce best-effort findings")
	}
}
