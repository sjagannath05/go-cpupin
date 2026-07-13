//go:build linux

package cpupin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CheckAlignment inspects RSS queue count, IRQ affinities, and the reader core
// set for one interface. Best-effort: masked procfs/sysfs in containers yields
// a partial report with findings, never an error (DESIGN §4.5).
func CheckAlignment(iface string, readerCores CPUSet) (*AlignmentReport, error) {
	rep := &AlignmentReport{
		Iface:       iface,
		IRQAffinity: map[int]CPUSet{},
		ReaderCores: readerCores,
	}
	ifaceDir := filepath.Join(sysRoot, "class", "net", iface)

	// RSS queue count.
	if ents, err := os.ReadDir(filepath.Join(ifaceDir, "queues")); err == nil {
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "rx-") {
				rep.RSSQueues++
			}
		}
		if rep.RSSQueues != readerCores.Size() {
			rep.Misaligned = append(rep.Misaligned,
				fmt.Sprintf("%d RSS rx queues vs %d reader cores — spread will be uneven", rep.RSSQueues, readerCores.Size()))
		}
	} else {
		rep.Misaligned = append(rep.Misaligned,
			fmt.Sprintf("cannot read %s/queues (%v) — sysfs masked or unknown iface", ifaceDir, err))
	}

	// Virtual-interface detection: physical NICs have a device/ symlink;
	// veth/bridge/loopback don't → SKF_AD_CPU locality not guaranteed (DESIGN §4.4).
	if _, err := os.Stat(filepath.Join(ifaceDir, "device")); err != nil && iface != "lo" {
		rep.Misaligned = append(rep.Misaligned,
			fmt.Sprintf("%s looks like a virtual interface (no device/ entry) — SKF_AD_CPU locality through veth/bridge is not guaranteed; verify empirically", iface))
	}

	// IRQ affinities.
	data, err := os.ReadFile(filepath.Join(procRoot, "interrupts"))
	if err != nil {
		rep.Misaligned = append(rep.Misaligned,
			fmt.Sprintf("cannot read /proc/interrupts (%v) — IRQ alignment unknown", err))
		return rep, nil
	}
	irqs := parseInterruptsIRQs(string(data), iface)
	if len(irqs) == 0 {
		rep.Misaligned = append(rep.Misaligned,
			fmt.Sprintf("no IRQs matched %q in /proc/interrupts (virtio/renamed driver?) — IRQ alignment unknown", iface))
	}
	for _, irq := range irqs {
		aff, err := os.ReadFile(filepath.Join(procRoot, "irq", fmt.Sprint(irq), "smp_affinity_list"))
		if err != nil {
			continue
		}
		set, perr := ParseCPUSet(string(aff))
		if perr != nil {
			continue
		}
		rep.IRQAffinity[irq] = set
		if out := set.Difference(readerCores); !out.IsEmpty() {
			rep.Misaligned = append(rep.Misaligned,
				fmt.Sprintf("IRQ %d affinity %s includes cores outside reader set %s", irq, set, readerCores))
		}
	}
	return rep, nil
}
