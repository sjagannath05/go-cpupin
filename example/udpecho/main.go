//go:build linux

// Command udpecho is go-cpupin's end-to-end smoke rig: N
// SO_REUSEPORT sockets bound in index order, one pinned reader per socket,
// cpu→index steering, per-socket counters. Uneven counters under multi-source
// load = steering is not working; see also -iface for the alignment report.
//
// For A/B benchmarking, -pin and -steer toggle the arms independently:
// baseline (-pin=false -steer=false), pinned-only (-pin -steer=false), and
// full (-pin -steer). -cpustats samples SO_INCOMING_CPU every 64th packet per
// reader to build a delivering-CPU histogram (the locality signal).
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
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	cpupin "github.com/sjagannath05/go-cpupin"
)

// readerStats holds the per-reader histogram of delivering CPUs sampled via
// SO_INCOMING_CPU. Guarded by mu because the main goroutine reads it at
// shutdown while the reader is still running.
type readerStats struct {
	mu   sync.Mutex
	cpus map[int]uint64
}

func (s *readerStats) record(cpu int) {
	s.mu.Lock()
	s.cpus[cpu]++
	s.mu.Unlock()
}

// format renders the histogram as "cpu:count,cpu:count" with cpu keys sorted
// ascending, or "-" when empty.
func (s *readerStats) format() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cpus) == 0 {
		return "-"
	}
	keys := make([]int, 0, len(s.cpus))
	for cpu := range s.cpus {
		keys = append(keys, cpu)
	}
	sort.Ints(keys)
	out := ""
	for i, cpu := range keys {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf("%d:%d", cpu, s.cpus[cpu])
	}
	return out
}

func main() {
	addr := flag.String("addr", ":9999", "UDP listen address")
	readers := flag.Int("readers", 4, "reader sockets/goroutines")
	iface := flag.String("iface", "", "interface for CheckAlignment report (requires -pin)")
	pin := flag.Bool("pin", true, "pin reader goroutines to exclusive cores (false = no cpupin at all)")
	steer := flag.Bool("steer", true, "attach SO_REUSEPORT cpu→socket steering; requires -pin (steering targets the pinned-core map), auto-disabled otherwise")
	cpustats := flag.Bool("cpustats", true, "sample SO_INCOMING_CPU every 64th packet per reader into a delivering-CPU histogram")
	flag.Parse()

	steerOn := *steer
	if !*pin && steerOn {
		log.Printf("warning: -steer requires -pin (steering needs the pinned-core map to mean anything); disabling steering")
		steerOn = false
	}

	var plan *cpupin.Plan
	cores := "-"
	if *pin {
		var err error
		plan, err = cpupin.Build(cpupin.Spec{Roles: []cpupin.Role{
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
		cores = plan.Cores("readers").String()
	}

	fmt.Printf("MODE pin=%v steer=%v readers=%d cores=%s\n", *pin, steerOn, *readers, cores)

	if *iface != "" && plan != nil {
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
	stats := make([]readerStats, *readers)
	for i := range conns {
		pc, err := lc.ListenPacket(context.Background(), "udp", *addr)
		if err != nil {
			log.Fatalf("bind socket %d: %v", i, err)
		}
		conns[i] = pc.(*net.UDPConn)
		stats[i].cpus = make(map[int]uint64)
	}

	if steerOn {
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
	}

	for i, conn := range conns {
		go func(idx int, c *net.UDPConn) {
			if plan != nil {
				unpin, err := plan.Pin("readers", idx)
				if err != nil {
					log.Fatalf("pin reader %d: %v", idx, err)
				}
				defer unpin()
			}
			var raw syscall.RawConn
			if *cpustats {
				r, err := c.SyscallConn()
				if err != nil {
					log.Fatalf("syscall conn reader %d: %v", idx, err)
				}
				raw = r
			}
			buf := make([]byte, 2048)
			var pkts uint64
			for {
				n, peer, err := c.ReadFromUDP(buf)
				if err != nil {
					return
				}
				counts[idx].Add(1)
				pkts++
				if *cpustats && pkts%64 == 0 {
					var cpu int
					var soErr error
					if ctlErr := raw.Control(func(fd uintptr) {
						cpu, soErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_INCOMING_CPU)
					}); ctlErr == nil && soErr == nil {
						stats[idx].record(cpu)
					}
				}
				_, _ = c.WriteToUDP(buf[:n], peer)
			}
		}(i, conn)
	}

	tick := time.NewTicker(2 * time.Second)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	for {
		select {
		case <-tick.C:
			line := "pkts/socket:"
			for i := range counts {
				line += fmt.Sprintf(" [%d]=%d", i, counts[i].Load())
			}
			fmt.Println(line)
		case <-sig:
			printUDPStats()
			var total uint64
			for i := range counts {
				n := counts[i].Load()
				total += n
				fmt.Printf("FINAL socket=%d pkts=%d cpus=%s\n", i, n, stats[i].format())
			}
			fmt.Printf("FINAL total pkts=%d\n", total)
			return
		}
	}
}

// printUDPStats best-effort parses the netns-wide UDP counters from
// /proc/net/snmp (the "Udp:" header/value line pair, matched by field name,
// not position) and prints one FINAL udpstats line. Inside a container's own
// netns the absolute values are container-lifetime deltas, so rcvbuferrors
// climbing is the drop-onset signal. Any read/parse problem skips the line
// silently.
func printUDPStats() {
	data, err := os.ReadFile("/proc/net/snmp")
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	var header, values []string
	for i := 0; i+1 < len(lines); i++ {
		if strings.HasPrefix(lines[i], "Udp:") && strings.HasPrefix(lines[i+1], "Udp:") {
			header = strings.Fields(lines[i])
			values = strings.Fields(lines[i+1])
			break
		}
	}
	if header == nil || len(header) != len(values) {
		return
	}
	stats := make(map[string]uint64, len(header))
	for i := 1; i < len(header); i++ { // [0] is the "Udp:" tag
		v, err := strconv.ParseUint(values[i], 10, 64)
		if err != nil {
			return
		}
		stats[header[i]] = v
	}
	for _, name := range []string{"InDatagrams", "InErrors", "RcvbufErrors", "OutDatagrams"} {
		if _, ok := stats[name]; !ok {
			return
		}
	}
	fmt.Printf("FINAL udpstats indatagrams=%d inerrors=%d rcvbuferrors=%d outdatagrams=%d\n",
		stats["InDatagrams"], stats["InErrors"], stats["RcvbufErrors"], stats["OutDatagrams"])
}
