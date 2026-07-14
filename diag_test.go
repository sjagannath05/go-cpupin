package cpupin

import (
	"errors"
	"reflect"
	"testing"
)

const sampleInterrupts = `           CPU0       CPU1
  24:    1234000          0   PCI-MSI 524288-edge      eth0-rx-0
  25:          0    5678000   PCI-MSI 524289-edge      eth0-rx-1
  26:        100        100   PCI-MSI 524290-edge      eth0-tx-0
  27:         50         50   PCI-MSI 999999-edge      nvme0q1
 NMI:          0          0   Non-maskable interrupts
`

func TestParseInterruptsIRQs(t *testing.T) {
	got := parseInterruptsIRQs(sampleInterrupts, "eth0")
	if want := []int{24, 25, 26}; !reflect.DeepEqual(got, want) {
		t.Errorf("parseInterruptsIRQs(eth0) = %v, want %v", got, want)
	}
	if got := parseInterruptsIRQs(sampleInterrupts, "wlan0"); len(got) != 0 {
		t.Errorf("parseInterruptsIRQs(wlan0) = %v, want none", got)
	}
	// NMI line (non-numeric IRQ) must not crash or match.
	if got := parseInterruptsIRQs(sampleInterrupts, "maskable"); len(got) != 0 {
		t.Errorf("named-line match = %v, want none", got)
	}
}

func TestCheckAlignmentOffLinux(t *testing.T) {
	if Supported() {
		t.Skip("off-Linux contract")
	}
	if _, err := CheckAlignment("eth0", NewCPUSet(0)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("CheckAlignment off-Linux = %v, want ErrUnsupported", err)
	}
}
