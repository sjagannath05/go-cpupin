package cpupin

import (
	"sort"
	"strconv"
	"strings"
)

// AlignmentReport is the read-only RSS/IRQ/reader alignment snapshot.
// Consumers log it at startup so "pinning is on but IRQs point
// elsewhere" is visible instead of silent.
type AlignmentReport struct {
	Iface       string
	RSSQueues   int
	IRQAffinity map[int]CPUSet // irq → cores (smp_affinity_list)
	ReaderCores CPUSet
	Misaligned  []string // human-readable findings
}

// parseInterruptsIRQs returns the IRQ numbers whose /proc/interrupts line
// mentions dev (substring match on any device-name field, so "eth0" matches
// "eth0-rx-3"). Pure; best-effort by construction.
func parseInterruptsIRQs(interrupts, dev string) []int {
	var irqs []int
	for _, line := range strings.Split(interrupts, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasSuffix(fields[0], ":") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(fields[0], ":"))
		if err != nil {
			continue // NMI:, LOC:, etc.
		}
		for _, f := range fields[1:] {
			if strings.Contains(f, dev) {
				irqs = append(irqs, n)
				break
			}
		}
	}
	sort.Ints(irqs)
	return irqs
}
