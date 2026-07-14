# Benchmark results: what pinning does and when it pays

Measured with the tools in this repo (`example/udpecho`, `example/udploadgen`,
`bench/docker/`) on a 16-vCPU cloud VM (Xen-class virtual NIC with 2 RX queues,
kernel 5.15), running the echo server inside a container on a **Docker bridge
network** — the deployment shape where "does any of this survive veth?" is an open
question. Load came from three same-datacenter hosts (flood mode, fire-and-forget
pacing, plus a low-rate lock-step probe flow measuring RTT-under-load). Configs were
interleaved (baseline, then pinned, per rate) so each comparison shares its
background-noise window. The host was shared and busy — numbers below are
comparative, not absolute.

## Result 1 — steering's CPU signal survives bridge networking

`SKF_AD_CPU`, as seen by the SO_REUSEPORT classic-BPF program, is the genuine NIC
RSS softirq CPU even after the packet crosses bridge + veth into the container
netns. Proof was deterministic rather than statistical: with reader cores chosen to
mismatch the IRQ CPUs, 100% of ~90k packets followed the program's `cpu % n`
fallthrough to one exact socket; with reader cores matching the IRQ CPUs, 100%
followed the direct jump-table mapping. A rewritten or randomized delivering CPU
cannot produce perfect predicted splits twice. Conclusion: `SteerReuseport` works as
designed under bridge networking — no host networking required.

## Result 2 — check your NIC's UDP hashing before trusting RSS spread

This VM's virtual NIC hashed UDP by **IP pair only** (no ports): sixteen flows from
one source IP — and even flows from several source IPs — all landed on a single RX
queue, i.e. a single softirq CPU. On such hosts, steering by delivering CPU
concentrates everything onto one reader (worse than the kernel's default 4-tuple
socket hash), and no reader count fixes it. Check `ethtool -l` and
`ethtool -n <nic> rx-flow-hash udp4` per host class; software RPS
(`rps_cpus`) restores per-flow spread if needed (verified: with RPS enabled,
steering distributed per-flow deterministically across both sockets).

## Result 3 — pinning is a small tax at idle and a categorical win under load

The rate ladder (aggregate offered load, UDP echo round-trip, 2 reader sockets;
"pin-away" = readers pinned to dedicated cores away from the NIC IRQ CPUs +
housekeeping fenced via `Plan.Apply()`; loss is end-to-end):

| Offered load | Unpinned baseline | Pinned (pin-away, no steering) |
|---|---|---|
| ~6k pps | p99 ≈ 2.7 ms — baseline wins | p99 ≈ 3.1 ms (+12% tail tax) |
| 20k pps | 0% loss, p99 ≈ 9 ms | 0% loss, **p99 ≈ 3.6 ms** |
| **50k pps** | **16–19% packet loss** (socket buffers overflow — readers can't keep up) | **0% loss**, p99 ≈ 18 ms |
| 100k pps | 53% loss, heavy socket-level drops | 38% loss, **socket-level drops ≈ 0** |
| 150k pps | — | 66% loss, socket-level drops = 0 |

Reading the table:

- **The crossover sits around 10–20k pps.** Below it, an over-provisioned host
  absorbs everything and pinning only removes scheduler freedom (the tail tax).
  Past it, unpinned readers start losing to scheduler migrations and contention
  with softirq processing, then collapse outright.
- **Pinning moved the reader bottleneck out of the picture entirely.** From 100k
  offered upward, all remaining loss happened *upstream* of the sockets (the
  single-queue NIC→bridge→veth path saturated at roughly 60k pps delivered on this
  VM); pinned readers consumed essentially every packet that reached them at every
  rate tested. A control with the same dedicated cores but *no* pinning still lost
  more with 3.5× the socket drops — the win is pinning itself, not core selection.
- **Steering was harmful at every rate on this single-queue host** (see Result 2),
  including with RPS enabled onto the reader cores (that co-locates softirq work
  with the readers — the worst of both).

## Practical guidance

1. Ship with everything off (the library is off-by-default by design); enable
   pinning where sustained per-host packet rates reach the ~tens-of-kpps regime or
   where storm resilience matters — that's where "readers never fall over" is
   bought.
2. Pick reader cores **away** from the NIC IRQ CPUs — and mind SMT: a core that is
   the hyperthread sibling of an IRQ CPU is not "away" (`Plan.String()` flags
   sibling collisions).
3. Enable `SteerReuseport` only on hosts where ingress genuinely spreads across
   multiple RX queues (or after configuring RPS deliberately), and verify with a
   10-minute A/B using `bench/docker/run-ab.sh` — per host class, not fleet-wide.
4. Trust socket-level drop counters (`FINAL udpstats` from `example/udpecho`,
   RcvbufErrors) to attribute loss: they separate "readers too slow" (pinning's
   domain) from "ingress path saturated" (host/NIC domain, which no userspace
   config fixes).
