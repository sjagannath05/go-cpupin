//go:build linux

package cpupin

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// SteerReuseport attaches the cpu→index steering program to one fd of a
// SO_REUSEPORT group; the kernel applies it to the whole group. Packets
// softirq'd on readerCores.List()[i] are delivered to the i-th BOUND socket —
// consumers must bind reader sockets in index order (DESIGN §4.4).
//
// Unprivileged: SO_ATTACH_REUSEPORT_CBPF needs no capability and passes the
// default docker seccomp profile — this is why CBPF is the primary path.
func SteerReuseport(fd uintptr, readerCores CPUSet) error {
	if readerCores.IsEmpty() {
		return errors.New("cpupin: SteerReuseport: empty reader core set")
	}
	insns := buildSteerProgram(readerCores.List())
	filters := make([]unix.SockFilter, len(insns))
	for i, in := range insns {
		filters[i] = unix.SockFilter{Code: in.Code, Jt: in.Jt, Jf: in.Jf, K: in.K}
	}
	prog := unix.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	if err := unix.SetsockoptSockFprog(int(fd), unix.SOL_SOCKET, unix.SO_ATTACH_REUSEPORT_CBPF, &prog); err != nil {
		return fmt.Errorf("cpupin: SteerReuseport: attach cbpf (%d cores): %w", readerCores.Size(), err)
	}
	return nil
}

// SetIncomingCPU sets SO_INCOMING_CPU as a cheaper, hint-level alternative to
// full CBPF steering (DESIGN §4.4).
func SetIncomingCPU(fd uintptr, cpu int) error {
	if err := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_INCOMING_CPU, cpu); err != nil {
		return fmt.Errorf("cpupin: SetIncomingCPU(%d): %w", cpu, err)
	}
	return nil
}
