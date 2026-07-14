//go:build linux

package cpupin

import (
	"net"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func reuseportUDPSocket(t *testing.T, addr *net.UDPAddr) (int, *net.UDPAddr) {
	t.Helper()
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEPORT, 1); err != nil {
		t.Fatal(err)
	}
	sa := &unix.SockaddrInet4{Port: addr.Port}
	copy(sa.Addr[:], addr.IP.To4())
	if err := unix.Bind(fd, sa); err != nil {
		t.Fatal(err)
	}
	got, err := unix.Getsockname(fd)
	if err != nil {
		t.Fatal(err)
	}
	bound := got.(*unix.SockaddrInet4)
	return fd, &net.UDPAddr{IP: net.IP(bound.Addr[:]), Port: bound.Port}
}

func TestSteerReuseportAttaches(t *testing.T) {
	base := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
	fd0, bound := reuseportUDPSocket(t, base)
	_, _ = reuseportUDPSocket(t, bound) // second group member, same port

	if err := SteerReuseport(uintptr(fd0), NewCPUSet(0, 1)); err != nil {
		t.Fatalf("SteerReuseport attach: %v", err)
	}
}

func TestSteerReuseportRejectsEmpty(t *testing.T) {
	if err := SteerReuseport(0, CPUSet{}); err == nil {
		t.Fatal("empty reader core set must error")
	}
}

func TestSetIncomingCPU(t *testing.T) {
	base := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
	fd, _ := reuseportUDPSocket(t, base)
	if err := SetIncomingCPU(uintptr(fd), 0); err != nil {
		t.Fatalf("SetIncomingCPU: %v", err)
	}
}

func TestSteerReuseportDeliversByCPU(t *testing.T) {
	avail, err := Available()
	if err != nil {
		t.Fatal(err)
	}
	if avail.Size() < 2 {
		t.Skip("needs >= 2 cores")
	}
	// Two reader cores, discovered not hardcoded — deliberately take the LAST
	// two so on most hosts core ID != socket index (kills modulo regressions).
	cores := avail.List()[avail.Size()-2:]
	readerSet := NewCPUSet(cores...)

	base := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
	fd0, bound := reuseportUDPSocket(t, base)
	fd1, _ := reuseportUDPSocket(t, bound)
	fds := []int{fd0, fd1}

	if err := SteerReuseport(uintptr(fd0), readerSet); err != nil {
		t.Fatal(err)
	}
	for _, fd := range fds {
		tv := unix.Timeval{Sec: 0, Usec: 200_000}
		_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
	}

	for idx, core := range cores {
		idx, core := idx, core
		done := make(chan error, 1)
		go func() {
			// Loopback: sender's CPU == delivering softirq CPU.
			unpin, err := PinSelf(core)
			if err != nil {
				done <- err
				return
			}
			defer unpin()
			conn, err := net.DialUDP("udp", nil, bound)
			if err != nil {
				done <- err
				return
			}
			defer conn.Close()
			for i := 0; i < 5; i++ {
				if _, err := conn.Write([]byte{byte(idx)}); err != nil {
					done <- err
					return
				}
				time.Sleep(time.Millisecond)
			}
			done <- nil
		}()
		if err := <-done; err != nil {
			t.Fatal(err)
		}

		buf := make([]byte, 16)
		// SO_RCVTIMEO reads are never auto-restarted after a signal (Go's
		// SIGURG lands here under load) — retry on EINTR instead of flaking.
		var n int
		var err error
		for {
			n, _, err = unix.Recvfrom(fds[idx], buf, 0)
			if err != unix.EINTR {
				break
			}
		}
		if err != nil {
			t.Fatalf("socket %d (core %d) received nothing: %v — steering failed", idx, core, err)
		}
		if n < 1 || buf[0] != byte(idx) {
			t.Fatalf("socket %d got payload %v, want marker %d", idx, buf[:n], idx)
		}
		// The other socket must NOT have this marker waiting.
		other := fds[1-idx]
		if n, _, err := unix.Recvfrom(other, buf, unix.MSG_DONTWAIT); err == nil && n > 0 && buf[0] == byte(idx) {
			t.Fatalf("marker %d leaked to socket %d — mis-steered", idx, 1-idx)
		}
	}
}
