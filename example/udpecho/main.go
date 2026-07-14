//go:build linux

// Command udpecho is go-cpupin's end-to-end smoke rig: N
// SO_REUSEPORT sockets bound in index order, one pinned reader per socket,
// cpu→index steering, per-socket counters. Uneven counters under multi-source
// load = steering is not working; see also -iface for the alignment report.
//
// Usage: udpecho -addr :9999 -readers 4 -iface eth0
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	cpupin "github.com/sjagannath05/go-cpupin"
)

func main() {
	addr := flag.String("addr", ":9999", "UDP listen address")
	readers := flag.Int("readers", 4, "reader sockets/goroutines")
	iface := flag.String("iface", "", "interface for CheckAlignment report (optional)")
	flag.Parse()

	plan, err := cpupin.Build(cpupin.Spec{Roles: []cpupin.Role{
		{Name: "readers", Threads: *readers, Exclusive: true},
		{Name: "housekeeping", Housekeeping: true},
	}})
	if err != nil {
		log.Fatalf("build plan: %v", err)
	}
	if err := plan.Apply(); err != nil {
		log.Fatalf("apply plan: %v", err)
	}
	fmt.Print(plan)

	if *iface != "" {
		rep, err := cpupin.CheckAlignment(*iface, plan.Cores("readers"))
		if err != nil {
			log.Printf("alignment check: %v", err)
		} else {
			fmt.Printf("iface %s: %d rx queues, %d IRQs tracked\n", rep.Iface, rep.RSSQueues, len(rep.IRQAffinity))
			for _, m := range rep.Misaligned {
				fmt.Printf("  misaligned: %s\n", m)
			}
		}
	}

	lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		var soErr error
		if err := c.Control(func(fd uintptr) {
			soErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
		}); err != nil {
			return err
		}
		return soErr
	}}

	// Bind order = socket index order — load-bearing for steering.
	conns := make([]*net.UDPConn, *readers)
	counts := make([]atomic.Uint64, *readers)
	for i := range conns {
		pc, err := lc.ListenPacket(context.Background(), "udp", *addr)
		if err != nil {
			log.Fatalf("bind socket %d: %v", i, err)
		}
		conns[i] = pc.(*net.UDPConn)
	}

	raw, err := conns[0].SyscallConn()
	if err != nil {
		log.Fatal(err)
	}
	var steerErr error
	if err := raw.Control(func(fd uintptr) {
		steerErr = cpupin.SteerReuseport(fd, plan.Cores("readers"))
	}); err != nil {
		log.Fatal(err)
	}
	if steerErr != nil {
		log.Fatalf("steer: %v", steerErr)
	}

	for i, conn := range conns {
		go func(idx int, c *net.UDPConn) {
			unpin, err := plan.Pin("readers", idx)
			if err != nil {
				log.Fatalf("pin reader %d: %v", idx, err)
			}
			defer unpin()
			buf := make([]byte, 2048)
			for {
				n, peer, err := c.ReadFromUDP(buf)
				if err != nil {
					return
				}
				counts[idx].Add(1)
				_, _ = c.WriteToUDP(buf[:n], peer)
			}
		}(i, conn)
	}

	tick := time.NewTicker(2 * time.Second)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	for {
		select {
		case <-tick.C:
			line := "pkts/socket:"
			for i := range counts {
				line += fmt.Sprintf(" [%d]=%d", i, counts[i].Load())
			}
			fmt.Println(line)
		case <-sig:
			fmt.Println("bye")
			return
		}
	}
}
