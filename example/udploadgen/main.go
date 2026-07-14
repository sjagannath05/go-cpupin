// Command udploadgen is a portable UDP echo load generator for benchmarking
// udpecho. It spawns -flows goroutines, each on its own ephemeral source port
// (so the 4-tuple varies per flow), paces the aggregate -pps across them, and
// measures echo RTT per packet.
//
// Payload: -size bytes (min 16); the first 16 bytes are big-endian
// flowID(4) + seq(4) + sendUnixNano(8). The echo server must return the
// datagram verbatim.
//
// At the end it prints exactly one machine-readable line:
//
//	RESULT sent=<n> recv=<n> loss_pct=<f> pps=<f> rtt_p50_us=<f> rtt_p95_us=<f> rtt_p99_us=<f>
//
// Usage: udploadgen -addr host:port -flows 8 -pps 5000 -size 128 -duration 30s -timeout 250ms
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"sort"
	"sync"
	"time"
)

const (
	headerSize = 16
	// minInterval is the per-flow pacing floor; requested rates above it are
	// capped (noted on stderr).
	minInterval = 50 * time.Microsecond
	// maxSamples caps the total RTT samples kept across all flows.
	maxSamples = 1_000_000
)

// flowResult is written only by its owning flow goroutine and read by main
// after the WaitGroup completes.
type flowResult struct {
	sent uint64
	recv uint64
	rtts []float64 // microseconds; append stops at cap (sample cap)
}

func main() {
	addr := flag.String("addr", "127.0.0.1:9999", "target UDP echo host:port")
	flows := flag.Int("flows", 8, "concurrent flows, each with its own source port")
	pps := flag.Float64("pps", 5000, "aggregate packets per second across all flows")
	size := flag.Int("size", 128, "payload bytes (min 16)")
	duration := flag.Duration("duration", 30*time.Second, "send duration")
	timeout := flag.Duration("timeout", 250*time.Millisecond, "per-packet echo read deadline")
	flag.Parse()

	if *flows < 1 {
		log.Fatalf("-flows must be >= 1 (got %d)", *flows)
	}
	if *pps <= 0 {
		log.Fatalf("-pps must be > 0 (got %g)", *pps)
	}
	if *duration <= 0 {
		log.Fatalf("-duration must be > 0 (got %v)", *duration)
	}
	if *timeout <= 0 {
		log.Fatalf("-timeout must be > 0 (got %v)", *timeout)
	}
	payloadSize := *size
	if payloadSize < headerSize {
		fmt.Fprintf(os.Stderr, "udploadgen: -size %d below minimum %d; using %d\n", *size, headerSize, headerSize)
		payloadSize = headerSize
	}

	perFlowPPS := *pps / float64(*flows)
	interval := time.Duration(float64(time.Second) / perFlowPPS)
	if interval < minInterval {
		fmt.Fprintf(os.Stderr, "udploadgen: requested %.0f pps/flow exceeds pacing floor; capping at %.0f pps/flow (interval %v)\n",
			perFlowPPS, float64(time.Second)/float64(minInterval), minInterval)
		interval = minInterval
	}

	raddr, err := net.ResolveUDPAddr("udp", *addr)
	if err != nil {
		log.Fatalf("resolve %s: %v", *addr, err)
	}

	// Preallocate per-flow RTT sample slices: estimated sends per flow,
	// bounded so the total stays under maxSamples.
	estimate := int(*duration/interval) + 16
	perFlowCap := maxSamples / *flows
	if perFlowCap < 1 {
		perFlowCap = 1
	}
	if estimate > perFlowCap {
		estimate = perFlowCap
	}
	results := make([]flowResult, *flows)
	for i := range results {
		results[i].rtts = make([]float64, 0, estimate)
	}

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < *flows; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			runFlow(id, raddr, interval, *duration, *timeout, payloadSize, &results[id])
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	var sent, recv uint64
	all := make([]float64, 0, len(results)*estimate)
	for i := range results {
		sent += results[i].sent
		recv += results[i].recv
		all = append(all, results[i].rtts...)
	}
	sort.Float64s(all)

	lossPct := 0.0
	if sent > 0 {
		lossPct = float64(sent-recv) / float64(sent) * 100
	}
	achieved := float64(recv) / elapsed.Seconds()
	fmt.Printf("RESULT sent=%d recv=%d loss_pct=%.3f pps=%.0f rtt_p50_us=%.0f rtt_p95_us=%.0f rtt_p99_us=%.0f\n",
		sent, recv, lossPct, achieved, percentile(all, 0.50), percentile(all, 0.95), percentile(all, 0.99))
}

// runFlow sends paced datagrams for duration and, after each send, tries to
// read the matching echo within timeout. A stale (mismatched) packet gets one
// extra read; a deadline miss counts as a loss.
func runFlow(id int, raddr *net.UDPAddr, interval, duration, timeout time.Duration, size int, res *flowResult) {
	conn, err := net.DialUDP("udp", nil, raddr) // nil laddr = distinct ephemeral source port
	if err != nil {
		log.Fatalf("flow %d: dial %s: %v", id, raddr, err)
	}
	defer conn.Close()

	payload := make([]byte, size)
	rbuf := make([]byte, size+64)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	end := time.Now().Add(duration)
	var seq uint32
	for time.Now().Before(end) {
		<-ticker.C
		seq++
		sendTime := time.Now()
		binary.BigEndian.PutUint32(payload[0:4], uint32(id))
		binary.BigEndian.PutUint32(payload[4:8], seq)
		binary.BigEndian.PutUint64(payload[8:16], uint64(sendTime.UnixNano()))
		if _, err := conn.Write(payload); err != nil {
			continue
		}
		res.sent++

		matched := false
		for attempt := 0; attempt < 2; attempt++ {
			if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
				break
			}
			n, err := conn.Read(rbuf)
			if err != nil {
				break // timeout (or socket error): count as loss
			}
			if n >= headerSize &&
				binary.BigEndian.Uint32(rbuf[0:4]) == uint32(id) &&
				binary.BigEndian.Uint32(rbuf[4:8]) == seq {
				matched = true
				break
			}
			// Stale packet (old seq): read again once.
		}
		if matched {
			res.recv++
			if len(res.rtts) < cap(res.rtts) {
				res.rtts = append(res.rtts, float64(time.Since(sendTime).Nanoseconds())/1e3)
			}
		}
	}
}

// percentile returns the p-th percentile (0..1) of an ascending-sorted slice,
// or 0 when empty.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
