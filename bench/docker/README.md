# Docker A/B benchmark harness

A/B-compares the go-cpupin UDP echo example with and without CPU pinning +
`SO_REUSEPORT` cBPF steering, using two containers built from this repo.

## What it measures

For each arm — **A (baseline)**: `-pin=false -steer=false`, **B (pinned)**:
`-pin=true -steer=true` — the harness runs `example/udpecho` in a container,
drives UDP load at it, then stops the server with SIGTERM so it prints its
`FINAL` lines. It reports:

- **Loadgen metrics** (`RESULT` line): achieved pps, loss %, RTT p50/p95/p99.
- **Per-socket packet spread**: `FINAL socket=<i> pkts=<n>` counts and the
  max/min ratio. Near 1.00 in arm B means cBPF steering distributed flows
  evenly across the `SO_REUSEPORT` sockets.
- **Delivering-CPU histograms** (`cpus=<cpu>:<count>,...` via
  `SO_INCOMING_CPU`): which softirq CPU delivered each socket's packets.
  This is the cross-netns locality probe — in arm B each socket should be
  dominated by exactly one CPU, matching its reader's pinned core. If arm
  B's histograms look like arm A's (mixed CPUs per socket), locality is
  broken and steering has degraded to a plain load spreader.

## Prerequisites

- Linux host with Docker (the pin/steer syscalls are Linux-only).
- At least `readers + 1` cores available in the cpuset you give the server
  (readers each get a core; leave headroom for the writer/main goroutines).
- No special container privileges are needed.

## Usage

```sh
# From anywhere; the script resolves the repo root itself.

# Local mode (default): loadgen runs in a second container on the same bridge.
bench/docker/run-ab.sh --cpuset 2-5 --readers 2

# More load, bigger payloads, dedicated pre-existing network:
bench/docker/run-ab.sh --cpuset 2-5 --readers 4 --flows 16 --pps 20000 \
    --size 512 --duration 60 --network my-bench-net

# External mode: harness starts each server, prints its IP and the exact
# udploadgen command, and waits for Enter while you run it from another host.
bench/docker/run-ab.sh --cpuset 2-5 --loadgen external
```

Logs are written to `bench/docker/results/<timestamp>/arm-{A,B}-{server,loadgen}.log`
and a side-by-side summary is printed at the end. Every `docker` invocation
is echoed so runs are auditable.

The image is built from the repo root:

```sh
docker build -f bench/docker/Dockerfile -t go-cpupin-bench:latest .
```

## Caveat: local mode does not exercise NIC RSS

With `--loadgen local`, traffic flows loadgen container → veth → Linux
bridge → veth → server container, entirely on-host. That validates flow
steering and per-socket spread, but the physical NIC's RSS hashing and IRQ
path never run, so it cannot answer whether steering aligns with NIC RSS.
For the real locality answer, use `--loadgen external` and send traffic
from a different host so packets arrive through the NIC. Note the printed
target is the server *container* IP: it must be reachable from the load
host (e.g. a routed or macvlan network); a default isolated bridge is not.

## Cleanup behavior

- Containers created by the harness are removed on exit (including on
  failure, via an EXIT trap).
- The Docker network is deleted **only if this run created it**. If you
  point `--network` at an existing bridge, it is used as-is and left alone.
